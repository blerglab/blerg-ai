package db_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// endTestPool is a single-connection pool on a fresh schema with every
// migration applied twice (the second pass must be a no-op), so the
// search_path setupSchema sets is the one every statement sees.
func endTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	setupSchema(t, pool)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := db.RunMigrations(ctx, pool); err != nil {
			t.Fatalf("RunMigrations pass %d: %v", i+1, err)
		}
	}
	return pool
}

func endTestSession(t *testing.T, pool *pgxpool.Pool, id, status string) {
	t.Helper()
	ctx := context.Background()
	const daemonID = "00000000-0000-0000-0000-0000000000e0"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "d", "local", "/r"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, id, daemonID, status, "/r/p", "p", "T", ""); err != nil {
		t.Fatal(err)
	}
}

func endOf(t *testing.T, pool *pgxpool.Pool, id string) (string, string, string) {
	t.Helper()
	row, err := db.GetSession(context.Background(), pool, id)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	s := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return s(row.EndReason), s(row.EndedByKind), s(row.EndedByAccount)
}

// The columns exist after migration and a row that predates any attribution
// reads back as NULL everywhere — through GetSession and the list queries.
func TestSessionEndMigrationRoundTrip(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	id := "00000000-0000-0000-0000-0000000000e1"
	endTestSession(t, pool, id, "running")

	if r, k, a := endOf(t, pool, id); r != "" || k != "" || a != "" {
		t.Fatalf("fresh row attribution = %q/%q/%q, want all NULL", r, k, a)
	}
	now := time.Now()
	if err := db.EndSessionStatus(ctx, pool, id, "stopped", &now,
		db.SessionEnd{Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman, ByAccount: "acct-1"}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListSessions(ctx, pool)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListSessions: %v (%d rows)", err, len(rows))
	}
	got := rows[0]
	if got.Status != "stopped" || got.EndedAt == nil ||
		*got.EndReason != db.EndReasonStoppedByUser || *got.EndedByKind != "human" || *got.EndedByAccount != "acct-1" {
		t.Errorf("listed row = %+v", got)
	}
}

// First write wins across both writers, and a revive clears.
func TestSessionEndFirstWriteWinsAndRevive(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	id := "00000000-0000-0000-0000-0000000000e2"
	endTestSession(t, pool, id, "running")

	user := db.SessionEnd{Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman, ByAccount: "acct-1"}
	if ok, err := db.RecordSessionEnd(ctx, pool, id, user); err != nil || !ok {
		t.Fatalf("RecordSessionEnd = %v, %v; want recorded", ok, err)
	}
	// A second record does nothing.
	if ok, err := db.RecordSessionEnd(ctx, pool, id, db.SessionEnd{Reason: db.EndReasonStoppedByAgent, ByKind: db.EndedByAgent}); err != nil || ok {
		t.Fatalf("second RecordSessionEnd = %v, %v; want not recorded", ok, err)
	}
	// The exit the stop causes keeps the stop's attribution — kind and
	// account included, not just the reason.
	now := time.Now()
	if err := db.EndSessionStatus(ctx, pool, id, "stopped", &now, db.SessionEnd{Reason: db.EndReasonProcessExited}); err != nil {
		t.Fatal(err)
	}
	if r, k, a := endOf(t, pool, id); r != db.EndReasonStoppedByUser || k != "human" || a != "acct-1" {
		t.Errorf("after exit = %q/%q/%q, want the stop kept", r, k, a)
	}

	// A revive (the daemon re-sends session_started) forgets it.
	endTestSession(t, pool, id, "running")
	if r, k, a := endOf(t, pool, id); r != "" || k != "" || a != "" {
		t.Errorf("after revive = %q/%q/%q, want cleared", r, k, a)
	}
	// ...and so does a revive through UpdateSessionStatus (terminal → live),
	// however fresh the attribution is.
	if err := db.EndSessionStatus(ctx, pool, id, "stopped", &now, db.SessionEnd{Reason: db.EndReasonProcessExited}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSessionStatus(ctx, pool, id, "running", nil); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := endOf(t, pool, id); r != "" {
		t.Errorf("after a revive = %q, want cleared", r)
	}
}

// backdateEnd makes a recorded attribution older than the pending-stop grace.
func backdateEnd(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE sessions SET end_recorded_at = now() - interval '5 minutes' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// A stop recorded at request time survives the session reporting a last live
// status while the kill lands, but not a session that is still alive long
// after — whether it shows that by a transition or just by still being there.
func TestPendingSessionEndGrace(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	user := db.SessionEnd{Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman, ByAccount: "acct-1"}

	id := "00000000-0000-0000-0000-0000000000f1"
	endTestSession(t, pool, id, "running")
	if _, err := db.RecordSessionEnd(ctx, pool, id, user); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSessionStatus(ctx, pool, id, "idle", nil); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := endOf(t, pool, id); r != db.EndReasonStoppedByUser {
		t.Errorf("within grace: %q, want the stop kept", r)
	}
	if ok, err := db.ExpirePendingSessionEnd(ctx, pool, id); err != nil || ok {
		t.Errorf("ExpirePendingSessionEnd within grace = %v, %v; want nothing expired", ok, err)
	}
	backdateEnd(t, pool, id)
	// Non-live transitions never clear.
	if err := db.UpdateSessionStatus(ctx, pool, id, "disconnected", nil); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := endOf(t, pool, id); r == "" {
		t.Error("a move to a non-live status cleared the stop")
	}
	if err := db.UpdateSessionStatus(ctx, pool, id, "waiting", nil); err != nil {
		t.Fatal(err)
	}
	if r, k, a := endOf(t, pool, id); r != "" || k != "" || a != "" {
		t.Errorf("stale stop after a live transition = %q/%q/%q, want cleared", r, k, a)
	}

	// No transition at all: the heartbeat path expires it.
	id2 := "00000000-0000-0000-0000-0000000000f2"
	endTestSession(t, pool, id2, "running")
	if _, err := db.RecordSessionEnd(ctx, pool, id2, user); err != nil {
		t.Fatal(err)
	}
	backdateEnd(t, pool, id2)
	if ok, err := db.ExpirePendingSessionEnd(ctx, pool, id2); err != nil || !ok {
		t.Fatalf("ExpirePendingSessionEnd = %v, %v; want expired", ok, err)
	}
	if r, _, _ := endOf(t, pool, id2); r != "" {
		t.Errorf("after expiry = %q, want cleared", r)
	}
	// ...but never on a session that has ended.
	now := time.Now()
	if err := db.EndSessionStatus(ctx, pool, id2, "stopped", &now, user); err != nil {
		t.Fatal(err)
	}
	backdateEnd(t, pool, id2)
	if ok, _ := db.ExpirePendingSessionEnd(ctx, pool, id2); ok {
		t.Error("expired the reason of an ended session")
	}
}

// RecordSessionEnd never attributes a row that already ended — including a
// pre-migration row with a NULL reason.
func TestRecordSessionEndSkipsEndedRows(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	for _, status := range []string{"stopped", "ended"} {
		id := "00000000-0000-0000-0000-0000000000f3"
		endTestSession(t, pool, id, status)
		if ok, err := db.RecordSessionEnd(ctx, pool, id, db.SessionEnd{Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman}); err != nil || ok {
			t.Errorf("%s row: RecordSessionEnd = %v, %v; want refused", status, ok, err)
		}
	}
}

// RetractSessionEnd only takes back exactly what it was given, on a session
// that has not ended.
func TestRetractSessionEndIsCompareAndSwap(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	id := "00000000-0000-0000-0000-0000000000f4"
	endTestSession(t, pool, id, "running")
	mine := db.SessionEnd{Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman, ByAccount: "acct-1"}
	if _, err := db.RecordSessionEnd(ctx, pool, id, mine); err != nil {
		t.Fatal(err)
	}
	for _, other := range []db.SessionEnd{
		{Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman, ByAccount: "acct-2"},
		{Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman},
		{Reason: db.EndReasonStoppedByAgent, ByKind: db.EndedByAgent, ByAccount: "acct-1"},
	} {
		if ok, err := db.RetractSessionEnd(ctx, pool, id, other); err != nil || ok {
			t.Errorf("retract %+v = %v, %v; want no match", other, ok, err)
		}
	}
	if r, _, a := endOf(t, pool, id); r != db.EndReasonStoppedByUser || a != "acct-1" {
		t.Fatalf("a non-matching retract changed the row: %q/%q", r, a)
	}
	// Ended in between: the reason is now the session's real ending.
	now := time.Now()
	if err := db.EndSessionStatus(ctx, pool, id, "stopped", &now, db.SessionEnd{Reason: db.EndReasonProcessExited}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.RetractSessionEnd(ctx, pool, id, mine); ok {
		t.Error("retracted the attribution of an ended session")
	}
	// Still live and still mine: taken back.
	id2 := "00000000-0000-0000-0000-0000000000f5"
	endTestSession(t, pool, id2, "running")
	if _, err := db.RecordSessionEnd(ctx, pool, id2, mine); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.RetractSessionEnd(ctx, pool, id2, mine); err != nil || !ok {
		t.Fatalf("retract own = %v, %v; want retracted", ok, err)
	}
	if r, _, _ := endOf(t, pool, id2); r != "" {
		t.Errorf("after retract = %q, want cleared", r)
	}
}

// A core account id (a UUID) is stored as is.
func TestSessionEndStoresAUUIDAccount(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	id := "00000000-0000-0000-0000-0000000000f6"
	endTestSession(t, pool, id, "running")
	const acct = "3f2b8c1e-9a4d-4e7b-8f10-2c6d5a9b0e71"
	if _, err := db.RecordSessionEnd(ctx, pool, id, db.SessionEnd{
		Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman, ByAccount: acct,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, a := endOf(t, pool, id); a != acct {
		t.Errorf("account = %q, want %q", a, acct)
	}
}

// Only the vocabulary is stored: an unknown reason is refused, an unknown
// actor kind or an account id that is not a plain identifier is dropped.
func TestSessionEndValidation(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	id := "00000000-0000-0000-0000-0000000000e3"
	endTestSession(t, pool, id, "running")

	if _, err := db.RecordSessionEnd(ctx, pool, id, db.SessionEnd{Reason: "because I said so"}); err == nil {
		t.Error("an unknown reason was accepted")
	}
	now := time.Now()
	if err := db.EndSessionStatus(ctx, pool, id, "stopped", &now, db.SessionEnd{Reason: "<b>x</b>"}); err == nil {
		t.Error("EndSessionStatus accepted an unknown reason")
	}
	if _, err := db.FinishClusterSession(ctx, pool, id, "error", "x", "nope", nil); err == nil {
		t.Error("FinishClusterSession accepted an unknown end reason")
	}
	if r, _, _ := endOf(t, pool, id); r != "" {
		t.Fatalf("a refused write left end_reason %q", r)
	}

	if _, err := db.RecordSessionEnd(ctx, pool, id, db.SessionEnd{
		Reason: db.EndReasonStoppedByAgent, ByKind: "root", ByAccount: "acct-1",
	}); err != nil {
		t.Fatal(err)
	}
	if r, k, a := endOf(t, pool, id); r != db.EndReasonStoppedByAgent || k != "" || a != "" {
		t.Errorf("unknown kind: stored %q/%q/%q, want the reason only", r, k, a)
	}

	for _, bad := range []string{"<script>", strings.Repeat("a", 200), "has space", "new\nline",
		"someone@example.com", "first.last", "scheme:thing"} {
		id2 := "00000000-0000-0000-0000-0000000000e4"
		endTestSession(t, pool, id2, "running") // upsert clears the previous attempt
		if _, err := db.RecordSessionEnd(ctx, pool, id2, db.SessionEnd{
			Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman, ByAccount: bad,
		}); err != nil {
			t.Fatal(err)
		}
		if r, k, a := endOf(t, pool, id2); r != db.EndReasonStoppedByUser || k != "human" || a != "" {
			t.Errorf("account %q: stored %q/%q/%q, want the account dropped", bad, r, k, a)
		}
	}
}

// The single-statement writers carry their own reasons and never overwrite
// one already recorded.
func TestSessionEndSingleStatementWriters(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()

	auto := "00000000-0000-0000-0000-0000000000e5"
	endTestSession(t, pool, auto, "running")
	if err := db.SetSessionAutoStop(ctx, pool, auto, true); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimSessionAutoStop(ctx, pool, auto); err != nil || !ok {
		t.Fatalf("ClaimSessionAutoStop = %v, %v", ok, err)
	}
	if r, _, _ := endOf(t, pool, auto); r != db.EndReasonAutoStopped {
		t.Errorf("auto-stop reason = %q", r)
	}

	lost := "00000000-0000-0000-0000-0000000000e6"
	endTestSession(t, pool, lost, "error")
	if _, err := pool.Exec(ctx, `UPDATE sessions SET daemon_lost_at = now() - interval '2 hours' WHERE id = $1`, lost); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.FinishLostDaemonSession(ctx, pool, lost, "daemon did not return", time.Now().Add(-time.Hour)); err != nil || !ok {
		t.Fatalf("FinishLostDaemonSession = %v, %v", ok, err)
	}
	if r, _, _ := endOf(t, pool, lost); r != db.EndReasonDaemonLost {
		t.Errorf("lost-daemon reason = %q", r)
	}

	// A stop recorded while the Job was still running is kept when the
	// reconciler later finds the Job gone.
	job := "00000000-0000-0000-0000-0000000000e7"
	endTestSession(t, pool, job, "running")
	if _, err := db.RecordSessionEnd(ctx, pool, job, db.SessionEnd{Reason: db.EndReasonStoppedByUser, ByKind: db.EndedByHuman}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.FinishClusterSession(ctx, pool, job, "error", "cluster job disappeared", db.EndReasonJobDisappeared, nil); err != nil || !ok {
		t.Fatalf("FinishClusterSession = %v, %v", ok, err)
	}
	if r, k, _ := endOf(t, pool, job); r != db.EndReasonStoppedByUser || k != "human" {
		t.Errorf("reconciled stop = %q/%q, want the stop kept", r, k)
	}
}
