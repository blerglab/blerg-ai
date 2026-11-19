package db_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type insightLockBarrier struct{ locked, release chan struct{} }
type insightLockKey struct{}

func (b *insightLockBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, insightLockKey{}, strings.HasPrefix(d.SQL, "SELECT coalesce(spawning_account_id"))
}
func (b *insightLockBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if locked, _ := ctx.Value(insightLockKey{}).(bool); locked {
		close(b.locked)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
}

func TestInsightRefreshAllowsForeignKeyEventInserts(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	id := mustSession(t, pool, daemon, "nonblocking-owner")
	barrier := &insightLockBarrier{make(chan struct{}), make(chan struct{})}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = barrier
	traced, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- db.RefreshInsightTiming(ctx, traced, id) }()
	select {
	case <-barrier.locked:
	case <-ctx.Done():
		t.Fatal("materializer did not acquire row lock")
	}
	insertCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	_, insertErr := pool.Exec(insertCtx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload) VALUES($1,1,gen_random_uuid(),'output','{}')`, id)
	stop()
	close(barrier.release)
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	if insertErr != nil {
		t.Fatalf("materializer blocked event foreign-key insert: %v", insertErr)
	}
}

func TestInsightBackfillFailureDoesNotStarveOlderHistory(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	old := mustSession(t, pool, daemon, "retry-owner")
	bad := mustSession(t, pool, daemon, "retry-owner")
	_, err := pool.Exec(t.Context(), `UPDATE sessions SET ended_at=now(),started_at=now()-interval '2 hours' WHERE id=$1`, old)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `CREATE FUNCTION reject_bad_insight() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (SELECT title FROM sessions WHERE id=NEW.session_id)='bad-history' THEN RAISE EXCEPTION 'fixture rejection'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_bad_insight BEFORE INSERT ON session_insight_timings FOR EACH ROW EXECUTE FUNCTION reject_bad_insight();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE sessions SET ended_at=now(),started_at=now()-interval '1 hour',title='bad-history' WHERE id=$1`, bad); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err = db.BackfillInsightTimings(t.Context(), pool, "retry-owner"); err == nil {
			t.Fatal("expected fixture failure")
		}
		var attempts int
		var pending, delayed bool
		if err = pool.QueryRow(t.Context(), `SELECT insight_attempts,insight_pending,insight_retry_at>now() FROM sessions WHERE id=$1`, bad).Scan(&attempts, &pending, &delayed); err != nil {
			t.Fatal(err)
		}
		if attempts != attempt || pending != (attempt < 3) || !delayed {
			t.Fatalf("attempt=%d pending=%v delayed=%v", attempts, pending, delayed)
		}
		if attempt == 1 {
			if n, e := db.BackfillInsightTimings(t.Context(), pool, "retry-owner"); e != nil || n != 1 {
				t.Fatalf("older history starved: n=%d err=%v", n, e)
			}
		}
		if _, err = pool.Exec(t.Context(), `UPDATE sessions SET insight_retry_at=now()-interval '1 second' WHERE id=$1`, bad); err != nil {
			t.Fatal(err)
		}
	}
	if n, e := db.BackfillInsightTimings(t.Context(), pool, "retry-owner"); e != nil || n != 0 {
		t.Fatalf("exhausted history retried: n=%d err=%v", n, e)
	}
}

func TestInsightEndedHistoryIsPreparedOffRequest(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	id := mustSession(t, pool, daemon, "cache-round3")
	foreign := mustSession(t, pool, daemon, "foreign-round3")
	_, err := pool.Exec(t.Context(), `UPDATE sessions SET started_at=now()-interval '1 hour',ended_at=now() WHERE id IN($1,$2)`, id, foreign)
	if err != nil {
		t.Fatal(err)
	}
	read := func() ([]byte, int) {
		t.Helper()
		raw, err := db.InsightActivityTimings(t.Context(), pool, "cache-round3", []string{id, foreign})
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			Pending  int `json:"pending_sessions"`
			Sessions []json.RawMessage
		}
		if err = json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if len(d.Sessions) != 1 {
			t.Fatal("foreign history leaked")
		}
		return raw, d.Pending
	}
	raw, pending := read()
	if pending != 1 || !strings.Contains(string(raw), `"timing":null`) {
		t.Fatalf("missing history must be pending: %s", raw)
	}
	var count int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM session_insight_timings`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("request warmed history: %d %v", count, err)
	}
	if n, err := db.BackfillInsightTimings(t.Context(), pool, "cache-round3"); err != nil || n != 1 {
		t.Fatalf("backfill %d %v", n, err)
	}
	raw, pending = read()
	if pending != 0 || strings.Contains(string(raw), `"timing":null`) {
		t.Fatalf("backfill missing: %s", raw)
	}
	if _, _, err = db.AppendAgentEventAt(t.Context(), pool, id, newUUID(t, pool), "status_changed", `{"status":"running"}`, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, p := read(); p != 1 {
		t.Fatal("late evidence did not invalidate ended cache")
	}
	if err = db.RefreshInsightTiming(t.Context(), pool, id); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err = pool.QueryRow(t.Context(), `SELECT fingerprint_inputs->>'version'='5' AND fingerprint_inputs->>'event_seq'='1' AND NOT initial_rework FROM session_insight_timings WHERE session_id=$1`, id).Scan(&valid); err != nil || !valid {
		t.Fatalf("fingerprint inputs not stored %v %v", valid, err)
	}
}

func TestInsightBackfillIsBoundedAndNewestFirst(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "backfill-round3")
	_, err := pool.Exec(t.Context(), `INSERT INTO sessions(daemon_id,project_path,repo,title,spawning_account_id,started_at,ended_at) SELECT daemon_id,project_path,repo,'History '||n,spawning_account_id,now()-n*interval '1 hour',now()-n*interval '1 hour'+interval '1 minute' FROM sessions CROSS JOIN generate_series(1,53) n WHERE id=$1`, parent)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := db.BackfillInsightTimings(t.Context(), pool, "backfill-round3"); err != nil || n != 50 {
		t.Fatalf("bounded batch %d %v", n, err)
	}
	var oldest string
	if err = pool.QueryRow(t.Context(), `SELECT title FROM sessions WHERE ended_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM session_insight_timings c WHERE c.session_id=sessions.id) ORDER BY started_at DESC LIMIT 1`).Scan(&oldest); err != nil || oldest != "History 51" {
		t.Fatalf("newest first %q %v", oldest, err)
	}
	if n, err := db.BackfillInsightTimings(t.Context(), pool, "backfill-round3"); err != nil || n != 3 {
		t.Fatalf("remaining batch %d %v", n, err)
	}
}

func TestInsightEndStatusMaterializesTiming(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	id := mustSession(t, pool, daemon, "end-round3")
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), pool, id, "stopped", &now, db.SessionEnd{Reason: db.EndReasonProcessExited}); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM session_insight_timings WHERE session_id=$1)`, id).Scan(&exists); err != nil || !exists {
		t.Fatalf("end did not materialize %v %v", exists, err)
	}
}

func TestInsightMarkInvalidatesOnlyLaterOwnedCardWork(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "mark-round3")
	before := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "mark-round3", ParentSessionID: parent})
	after := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "mark-round3", ParentSessionID: parent})
	foreign := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "foreign-mark-round3", ParentSessionID: parent})
	now := time.Now().UTC().Truncate(time.Second)
	_, err := pool.Exec(t.Context(), `UPDATE sessions SET started_at=$2,ended_at=$2::timestamptz+interval '1 minute' WHERE id=$1`, before, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `UPDATE sessions SET started_at=$3,ended_at=$3::timestamptz+interval '1 minute' WHERE id IN($1,$2)`, after, foreign, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO session_card_links VALUES($1,'board-a',42,'pin',now()),($2,'board-a',42,'pin',now())`, parent, foreign)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{before, after, foreign} {
		if err = db.RefreshInsightTiming(t.Context(), pool, id); err != nil {
			t.Fatal(err)
		}
	}
	markAt := now.Add(-90 * time.Minute)
	appendMark := func(kind string) {
		t.Helper()
		payload := `{"kind":"failure","type":"test","board_id":"board-a","card":42,"at":"` + markAt.Format(time.RFC3339) + `"}`
		if kind == "review" {
			payload = `{"kind":"review","verdict":"approved","board_id":"board-a","card":42,"at":"` + markAt.Add(time.Minute).Format(time.RFC3339) + `"}`
		}
		if _, _, err = db.AppendAgentEventAt(t.Context(), pool, parent, newUUID(t, pool), "mark", payload, now); err != nil {
			t.Fatal(err)
		}
	}
	appendMark("failure")
	var retained int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM session_insight_timings WHERE session_id=ANY($1::uuid[])`, []string{before, foreign}).Scan(&retained); err != nil || retained != 2 {
		t.Fatalf("unrelated caches invalidated %d %v", retained, err)
	}
	var exists bool
	if err = pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM session_insight_timings WHERE session_id=$1)`, after).Scan(&exists); err != nil || exists {
		t.Fatalf("later card cache retained %v %v", exists, err)
	}
	if n, e := db.BackfillInsightTimings(t.Context(), pool, "mark-round3"); e != nil || n != 1 {
		t.Fatalf("targeted recompute %d %v", n, e)
	}
	var rework bool
	if err = pool.QueryRow(t.Context(), `SELECT initial_rework FROM session_insight_timings WHERE session_id=$1`, after).Scan(&rework); err != nil || !rework {
		t.Fatalf("unresolved mark not applied %v %v", rework, err)
	}
	appendMark("review")
	if _, err = db.BackfillInsightTimings(t.Context(), pool, "mark-round3"); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT initial_rework FROM session_insight_timings WHERE session_id=$1`, after).Scan(&rework); err != nil || rework {
		t.Fatalf("approval did not resolve prior failure %v %v", rework, err)
	}
}

func TestInsightBackfillSkipsClaimedSessions(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	a := mustSession(t, pool, daemon, "claimed-round3")
	b := mustSession(t, pool, daemon, "claimed-round3")
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET ended_at=now() WHERE id IN($1,$2)`, a, b); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, a); err != nil {
		t.Fatal(err)
	}
	if n, err := db.BackfillInsightTimings(t.Context(), pool, "claimed-round3"); err != nil || n != 1 {
		t.Fatalf("claimed row was not skipped %d %v", n, err)
	}
	var id string
	if err = pool.QueryRow(t.Context(), `SELECT session_id::text FROM session_insight_timings`).Scan(&id); err != nil || id != b {
		t.Fatalf("wrong row prepared %q %v", id, err)
	}
}

func TestInsightMaterializerUsesOneConnection(t *testing.T) {
	pool := migratedPool(t, 1)
	daemon := mustDaemon(t, pool)
	id := mustSession(t, pool, daemon, "one-connection")
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET ended_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshInsightTiming(t.Context(), pool, id); err != nil {
		t.Fatal(err)
	}
}

func TestInsightLateCardLinkInvalidatesCachedStage(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "link-cache-round3")
	worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "link-cache-round3", ParentSessionID: parent})
	author := mustSession(t, pool, daemon, "link-cache-round3")
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET started_at=$2,ended_at=$2::timestamptz+interval '1 minute' WHERE id=$1`, worker, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(t.Context(), pool, author, newUUID(t, pool), "mark", `{"kind":"failure","type":"test","board_id":"board-a","card":42,"at":"`+now.Add(-2*time.Hour).Format(time.RFC3339)+`"}`, now); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshInsightTiming(t.Context(), pool, worker); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO session_card_links VALUES($1,'board-a',42,'pin',now())`, parent); err != nil {
		t.Fatal(err)
	}
	var pending bool
	if err := pool.QueryRow(t.Context(), `SELECT NOT EXISTS(SELECT 1 FROM session_insight_timings WHERE session_id=$1)`, worker).Scan(&pending); err != nil || !pending {
		t.Fatalf("late link kept stale timing %v %v", pending, err)
	}
	if _, err := db.BackfillInsightTimings(t.Context(), pool, "link-cache-round3"); err != nil {
		t.Fatal(err)
	}
	var rework bool
	if err := pool.QueryRow(t.Context(), `SELECT initial_rework FROM session_insight_timings WHERE session_id=$1`, worker).Scan(&rework); err != nil || !rework {
		t.Fatalf("late link stage not rebuilt %v %v", rework, err)
	}
}
