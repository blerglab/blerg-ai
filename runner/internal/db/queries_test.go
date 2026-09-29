package db_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func connect(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// setupSchema drops and recreates a fresh test schema so tests are isolated.
func setupSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	schema := "test_blerg_runner"

	_, err := pool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
	if err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	_, err = pool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", schema))
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	_, err = pool.Exec(ctx, fmt.Sprintf("SET search_path TO %s", schema))
	if err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	t.Cleanup(func() {
		bCtx := context.Background()
		pool.Exec(bCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
	})
}

func TestMigrationsAndQueries(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()

	setupSchema(t, pool)

	// Run migrations — first pass.
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations (first): %v", err)
	}

	// Run migrations again — must be idempotent.
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations (second/idempotent): %v", err)
	}

	// ── UpsertDaemon ──────────────────────────────────────────────────────────
	daemonID := "00000000-0000-0000-0000-000000000001"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "test-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	// Upsert again to exercise ON CONFLICT path.
	if err := db.UpsertDaemon(ctx, pool, daemonID, "test-daemon-updated", "pod", "/repos2"); err != nil {
		t.Fatalf("UpsertDaemon (update): %v", err)
	}

	// Verify the ON CONFLICT path actually wrote the new values.
	var gotName, gotMode, gotReposRoot string
	if err := pool.QueryRow(ctx,
		`SELECT name, mode, repos_root FROM daemons WHERE id = $1`, daemonID,
	).Scan(&gotName, &gotMode, &gotReposRoot); err != nil {
		t.Fatalf("SELECT daemon after upsert update: %v", err)
	}
	if gotName != "test-daemon-updated" {
		t.Errorf("upsert update: name = %q, want %q", gotName, "test-daemon-updated")
	}
	if gotMode != "pod" {
		t.Errorf("upsert update: mode = %q, want %q", gotMode, "pod")
	}
	if gotReposRoot != "/repos2" {
		t.Errorf("upsert update: repos_root = %q, want %q", gotReposRoot, "/repos2")
	}

	// ── SetDaemonStatus ───────────────────────────────────────────────────────
	if err := db.SetDaemonStatus(ctx, pool, daemonID, "connected"); err != nil {
		t.Fatalf("SetDaemonStatus: %v", err)
	}

	// ── InsertSession ─────────────────────────────────────────────────────────
	sessionID := "00000000-0000-0000-0000-000000000002"
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "starting", "/repos/myapp", "myapp", "My Session", "claude-sonnet-4-6"); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Insert a second session with an empty title and no model (tests NULLIF).
	sessionID2 := "00000000-0000-0000-0000-000000000003"
	if err := db.InsertSession(ctx, pool, sessionID2, daemonID, "running", "/repos/other", "other", "", ""); err != nil {
		t.Fatalf("InsertSession (no title): %v", err)
	}

	// ── UpdateSessionStatus ───────────────────────────────────────────────────
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatalf("UpdateSessionStatus (no endedAt): %v", err)
	}
	endedAt := time.Now().UTC()
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "ended", &endedAt); err != nil {
		t.Fatalf("UpdateSessionStatus (with endedAt): %v", err)
	}

	// ── AppendSessionEvent ────────────────────────────────────────────────────
	events := []struct {
		eType string
		data  string
		seq   int64
	}{
		{"output", "hello", 1},
		{"output", "world", 2},
		{"state_change", "running", 3},
	}
	for _, e := range events {
		if err := db.AppendSessionEvent(ctx, pool, sessionID, e.eType, e.data, e.seq); err != nil {
			t.Fatalf("AppendSessionEvent seq=%d: %v", e.seq, err)
		}
	}

	// ── GetSessionEvents ──────────────────────────────────────────────────────
	evRows, err := db.GetSessionEvents(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("GetSessionEvents: %v", err)
	}
	if len(evRows) != 3 {
		t.Fatalf("GetSessionEvents: expected 3 rows, got %d", len(evRows))
	}
	for i, r := range evRows {
		if r.Seq != int64(i+1) {
			t.Errorf("GetSessionEvents[%d].Seq = %d, want %d", i, r.Seq, i+1)
		}
	}

	// ── ListSessions ─────────────────────────────────────────────────────────
	allSessions, err := db.ListSessions(ctx, pool)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(allSessions) != 2 {
		t.Fatalf("ListSessions: expected 2 rows, got %d", len(allSessions))
	}

	// ── ListSessionsByStatus ──────────────────────────────────────────────────
	runningSessions, err := db.ListSessionsByStatus(ctx, pool, "running")
	if err != nil {
		t.Fatalf("ListSessionsByStatus: %v", err)
	}
	if len(runningSessions) != 1 {
		t.Fatalf("ListSessionsByStatus(running): expected 1 row, got %d", len(runningSessions))
	}
	if runningSessions[0].ID != sessionID2 {
		t.Errorf("ListSessionsByStatus(running): unexpected session ID %s", runningSessions[0].ID)
	}

	// ── ListSessionsByDaemon ──────────────────────────────────────────────────
	daemonSessions, err := db.ListSessionsByDaemon(ctx, pool, daemonID)
	if err != nil {
		t.Fatalf("ListSessionsByDaemon: %v", err)
	}
	if len(daemonSessions) != 2 {
		t.Fatalf("ListSessionsByDaemon: expected 2 rows, got %d", len(daemonSessions))
	}

	// ── UpsertPushSubscription ────────────────────────────────────────────────
	ep := "https://push.example.com/sub/abc"
	if err := db.UpsertPushSubscription(ctx, pool, ep, "key1", "auth1"); err != nil {
		t.Fatalf("UpsertPushSubscription: %v", err)
	}
	// Update the same endpoint.
	if err := db.UpsertPushSubscription(ctx, pool, ep, "key2", "auth2"); err != nil {
		t.Fatalf("UpsertPushSubscription (update): %v", err)
	}

	// ── ListPushSubscriptions ─────────────────────────────────────────────────
	subs, err := db.ListPushSubscriptions(ctx, pool)
	if err != nil {
		t.Fatalf("ListPushSubscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("ListPushSubscriptions: expected 1 row, got %d", len(subs))
	}
	if subs[0].P256dh != "key2" || subs[0].Auth != "auth2" {
		t.Errorf("ListPushSubscriptions: unexpected values p256dh=%s auth=%s", subs[0].P256dh, subs[0].Auth)
	}
}

// TestSetSessionUnread verifies that SetSessionUnread toggles the unread column
// and that GetSession reflects the updated value.
func TestSetSessionUnread(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)

	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-0000000000b1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "unread-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	sessionID := "00000000-0000-0000-0000-0000000000b2"
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Unread Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Unread should default to false.
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("GetSession (initial): %v", err)
	}
	if row == nil {
		t.Fatal("GetSession (initial): got nil")
	}
	if row.Unread {
		t.Errorf("Unread after insert = true, want false (DEFAULT)")
	}

	// Mark unread = true.
	if err := db.SetSessionUnread(ctx, pool, sessionID, true); err != nil {
		t.Fatalf("SetSessionUnread(true): %v", err)
	}
	row, err = db.GetSession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("GetSession (after true): %v", err)
	}
	if !row.Unread {
		t.Errorf("Unread after SetSessionUnread(true) = false, want true")
	}

	// Mark unread = false.
	if err := db.SetSessionUnread(ctx, pool, sessionID, false); err != nil {
		t.Fatalf("SetSessionUnread(false): %v", err)
	}
	row, err = db.GetSession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("GetSession (after false): %v", err)
	}
	if row.Unread {
		t.Errorf("Unread after SetSessionUnread(false) = true, want false")
	}
}

// TestSetSessionStarred verifies that SetSessionStarred toggles the starred column
// and that GetSession reflects the updated value.
func TestSetSessionStarred(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)

	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-0000000000c1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "starred-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	sessionID := "00000000-0000-0000-0000-0000000000c2"
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Starred Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Starred should default to false.
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("GetSession (initial): %v", err)
	}
	if row == nil {
		t.Fatal("GetSession (initial): got nil")
	}
	if row.Starred {
		t.Errorf("Starred after insert = true, want false (DEFAULT)")
	}

	// Mark starred = true.
	if err := db.SetSessionStarred(ctx, pool, sessionID, true); err != nil {
		t.Fatalf("SetSessionStarred(true): %v", err)
	}
	row, err = db.GetSession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("GetSession (after true): %v", err)
	}
	if !row.Starred {
		t.Errorf("Starred after SetSessionStarred(true) = false, want true")
	}

	// Mark starred = false.
	if err := db.SetSessionStarred(ctx, pool, sessionID, false); err != nil {
		t.Fatalf("SetSessionStarred(false): %v", err)
	}
	row, err = db.GetSession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("GetSession (after false): %v", err)
	}
	if row.Starred {
		t.Errorf("Starred after SetSessionStarred(false) = true, want false")
	}
}

// TestAppendSessionEventAllowsDuplicateSeq pins the deliberate post-004 contract:
// AppendSessionEvent does NOT deduplicate on (session_id, seq). Migration
// 004_remove_seq_unique dropped the unique index and AppendSessionEvent stopped
// using ON CONFLICT DO NOTHING, because a reattached daemon restarts seq at 1 —
// dropping those "duplicate" events left history stuck at the pre-restart state and
// garbled the terminal on session switch. So duplicates must be appended, not
// dropped. (Replaces the old TestAppendSessionEventIdempotent, which asserted the
// removed dedup behavior and silently skipped — DB tests skip without
// TEST_DATABASE_URL — so it went stale unnoticed.)
func TestAppendSessionEventAllowsDuplicateSeq(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)

	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-0000000000a1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "dupe-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	sessionID := "00000000-0000-0000-0000-0000000000a2"
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Dupe", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// First write of seq 1.
	if err := db.AppendSessionEvent(ctx, pool, sessionID, "output", "aGVsbG8=", 1); err != nil {
		t.Fatalf("AppendSessionEvent (first): %v", err)
	}

	// Re-append the SAME (session_id, seq) — must NOT error and must NOT be dropped
	// (mirrors a reattached daemon whose seq restarted at 1).
	if err := db.AppendSessionEvent(ctx, pool, sessionID, "output", "aGVsbG8=", 1); err != nil {
		t.Fatalf("AppendSessionEvent (duplicate seq) returned error, want nil: %v", err)
	}

	// A different seq also inserts.
	if err := db.AppendSessionEvent(ctx, pool, sessionID, "output", "d29ybGQ=", 2); err != nil {
		t.Fatalf("AppendSessionEvent (seq 2): %v", err)
	}

	// All three rows must exist — duplicates are kept, not deduplicated.
	evRows, err := db.GetSessionEvents(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("GetSessionEvents: %v", err)
	}
	if len(evRows) != 3 {
		t.Fatalf("expected 3 events (duplicates kept), got %d", len(evRows))
	}
	// GetSessionEvents orders by id ASC (insertion order): seq1, seq1, seq2.
	want := []struct {
		seq  int64
		data string
	}{{1, "aGVsbG8="}, {1, "aGVsbG8="}, {2, "d29ybGQ="}}
	for i, w := range want {
		if evRows[i].Seq != w.seq || evRows[i].Data != w.data {
			t.Errorf("event[%d] = {seq:%d data:%q}, want {seq:%d data:%q}",
				i, evRows[i].Seq, evRows[i].Data, w.seq, w.data)
		}
	}
}

// TestMessages exercises the messages table: update is auto-answered, ask is open,
// AnswerMessage is conditional (double-answer is a no-op), and
// CloseOpenMessagesForSession closes remaining open rows. DB-gated.
func TestMessages(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-0000000000d1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "msg-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	sessionID := "00000000-0000-0000-0000-0000000000d2"
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Msg Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	updID, err := db.CreateMessage(ctx, pool, sessionID, "update", "task 2 done")
	if err != nil {
		t.Fatalf("CreateMessage update: %v", err)
	}
	askID, err := db.CreateMessage(ctx, pool, sessionID, "ask", "green or amber?")
	if err != nil {
		t.Fatalf("CreateMessage ask: %v", err)
	}

	upd, _ := db.GetMessage(ctx, pool, updID)
	if upd == nil || upd.Status != "answered" || upd.Answer != nil {
		t.Errorf("update row = %+v, want status=answered answer=nil", upd)
	}
	ask, _ := db.GetMessage(ctx, pool, askID)
	if ask == nil || ask.Status != "open" {
		t.Errorf("ask row = %+v, want status=open", ask)
	}

	list, err := db.ListMessages(ctx, pool, 100)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(list) != 2 || list[0].ID != askID {
		t.Errorf("ListMessages = %d rows, newest %q; want 2, newest=ask", len(list), firstID(list))
	}

	changed, err := db.AnswerMessage(ctx, pool, askID, "amber")
	if err != nil || !changed {
		t.Fatalf("AnswerMessage open: changed=%v err=%v, want true", changed, err)
	}
	again, err := db.AnswerMessage(ctx, pool, askID, "green")
	if err != nil || again {
		t.Errorf("AnswerMessage already-answered: changed=%v err=%v, want false", again, err)
	}
	ask, _ = db.GetMessage(ctx, pool, askID)
	if ask.Status != "answered" || ask.Answer == nil || *ask.Answer != "amber" {
		t.Errorf("after answer: %+v, want answered/amber", ask)
	}

	// A second open message, then close all open for the session.
	noteID, _ := db.CreateMessage(ctx, pool, sessionID, "note", "do X later?")
	closed, err := db.CloseOpenMessagesForSession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("CloseOpenMessagesForSession: %v", err)
	}
	if len(closed) != 1 || closed[0].ID != noteID || closed[0].Answer == nil || *closed[0].Answer != "" {
		t.Errorf("closed = %+v, want the open note with answer=''", closed)
	}
}

// TestGetSessionEventsTail exercises the bounded tail query used to avoid
// replaying a heavy session's entire event log: (a) a session under budget
// returns all events ascending; (b) a session over budget returns only the
// newest tail, with the budget counted in decoded bytes (data is
// base64-encoded) and the event that crosses the budget included whole; (c)
// non-output events don't count toward the budget but tail rows still come
// back in id order.
func TestGetSessionEventsTail(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-0000000000e1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "tail-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	t.Run("under budget returns all events ascending", func(t *testing.T) {
		sessionID := "00000000-0000-0000-0000-0000000000e2"
		if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Tail Under", ""); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		data1 := base64.StdEncoding.EncodeToString([]byte("hello"))
		data2 := base64.StdEncoding.EncodeToString([]byte("world!"))
		if err := db.AppendSessionEvent(ctx, pool, sessionID, "output", data1, 1); err != nil {
			t.Fatalf("AppendSessionEvent seq=1: %v", err)
		}
		if err := db.AppendSessionEvent(ctx, pool, sessionID, "output", data2, 2); err != nil {
			t.Fatalf("AppendSessionEvent seq=2: %v", err)
		}

		rows, err := db.GetSessionEventsTail(ctx, pool, sessionID, 100000)
		if err != nil {
			t.Fatalf("GetSessionEventsTail: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("expected 2 rows, got %d", len(rows))
		}
		if rows[0].Seq != 1 || rows[1].Seq != 2 {
			t.Errorf("expected ascending seq order [1,2], got [%d,%d]", rows[0].Seq, rows[1].Seq)
		}
	})

	t.Run("over budget returns newest tail with crossing event included whole", func(t *testing.T) {
		sessionID := "00000000-0000-0000-0000-0000000000e3"
		if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Tail Over", ""); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		// Each payload decodes to exactly 10 bytes.
		payload := base64.StdEncoding.EncodeToString([]byte("0123456789"))
		for seq := int64(1); seq <= 4; seq++ {
			if err := db.AppendSessionEvent(ctx, pool, sessionID, "output", payload, seq); err != nil {
				t.Fatalf("AppendSessionEvent seq=%d: %v", seq, err)
			}
		}

		// Newest-first walk: seq4 (10) total=10, seq3 (10) total=20, seq2 (10)
		// total=30 >= 25 -> crosses budget, included whole, walk stops. seq1
		// excluded. Ascending: seq2, seq3, seq4.
		rows, err := db.GetSessionEventsTail(ctx, pool, sessionID, 25)
		if err != nil {
			t.Fatalf("GetSessionEventsTail: %v", err)
		}
		wantSeqs := []int64{2, 3, 4}
		if len(rows) != len(wantSeqs) {
			t.Fatalf("expected %d rows, got %d: %+v", len(wantSeqs), len(rows), rows)
		}
		for i, want := range wantSeqs {
			if rows[i].Seq != want {
				t.Errorf("rows[%d].Seq = %d, want %d", i, rows[i].Seq, want)
			}
		}
	})

	t.Run("non-output events do not count toward budget but are included in tail order", func(t *testing.T) {
		sessionID := "00000000-0000-0000-0000-0000000000e4"
		if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Tail Mixed", ""); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		payload := base64.StdEncoding.EncodeToString([]byte("12345")) // 5 bytes decoded
		if err := db.AppendSessionEvent(ctx, pool, sessionID, "output", payload, 1); err != nil {
			t.Fatalf("AppendSessionEvent seq=1: %v", err)
		}
		if err := db.AppendSessionEvent(ctx, pool, sessionID, "state_change", "running", 2); err != nil {
			t.Fatalf("AppendSessionEvent seq=2: %v", err)
		}
		if err := db.AppendSessionEvent(ctx, pool, sessionID, "output", payload, 3); err != nil {
			t.Fatalf("AppendSessionEvent seq=3: %v", err)
		}
		if err := db.AppendSessionEvent(ctx, pool, sessionID, "state_change", "idle", 4); err != nil {
			t.Fatalf("AppendSessionEvent seq=4: %v", err)
		}
		if err := db.AppendSessionEvent(ctx, pool, sessionID, "output", payload, 5); err != nil {
			t.Fatalf("AppendSessionEvent seq=5: %v", err)
		}

		// Newest-first walk: seq5 (output, 5) total=5, seq4 (state_change,
		// doesn't count) total=5, seq3 (output, 5) total=10 >= 8 -> crosses
		// budget, included whole, walk stops. seq1/seq2 excluded. Ascending:
		// seq3, seq4, seq5.
		rows, err := db.GetSessionEventsTail(ctx, pool, sessionID, 8)
		if err != nil {
			t.Fatalf("GetSessionEventsTail: %v", err)
		}
		wantSeqs := []int64{3, 4, 5}
		if len(rows) != len(wantSeqs) {
			t.Fatalf("expected %d rows, got %d: %+v", len(wantSeqs), len(rows), rows)
		}
		for i, want := range wantSeqs {
			if rows[i].Seq != want {
				t.Errorf("rows[%d].Seq = %d, want %d", i, rows[i].Seq, want)
			}
		}
	})
}

// TestGetSessionEventsTailMultiBatch exercises the keyset pagination inside
// GetSessionEventsTail across a session with more events than one page
// (sessionEventsTailBatchSize = 256), verifying results are identical to
// what the old single-query newest-first walk would have produced: (a) an
// under-budget session spanning multiple pages still returns every event in
// ascending order, and (b) an over-budget session whose budget crosses
// exactly on a page boundary still returns the correct tail with the
// crossing event included whole.
func TestGetSessionEventsTailMultiBatch(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-0000000000f1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "tail-multibatch-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	// Each event decodes to exactly 1 byte, so seq N contributes N bytes to
	// the running total counted from the newest event backward.
	const totalEvents = 300 // > sessionEventsTailBatchSize (256): forces 2 pages
	payload := base64.StdEncoding.EncodeToString([]byte("x"))

	seedSession := func(t *testing.T, sessionID, title string) {
		t.Helper()
		if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", title, ""); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		for seq := int64(1); seq <= totalEvents; seq++ {
			if err := db.AppendSessionEvent(ctx, pool, sessionID, "output", payload, seq); err != nil {
				t.Fatalf("AppendSessionEvent seq=%d: %v", seq, err)
			}
		}
	}

	t.Run("under budget spans multiple pages and returns everything ascending", func(t *testing.T) {
		sessionID := "00000000-0000-0000-0000-0000000000f2"
		seedSession(t, sessionID, "Tail Multibatch Under")

		rows, err := db.GetSessionEventsTail(ctx, pool, sessionID, 1_000_000)
		if err != nil {
			t.Fatalf("GetSessionEventsTail: %v", err)
		}
		if len(rows) != totalEvents {
			t.Fatalf("expected %d rows, got %d", totalEvents, len(rows))
		}
		for i, r := range rows {
			wantSeq := int64(i + 1)
			if r.Seq != wantSeq {
				t.Fatalf("rows[%d].Seq = %d, want %d (out of order)", i, r.Seq, wantSeq)
			}
		}
	})

	t.Run("budget crossing exactly on a page boundary returns the correct tail", func(t *testing.T) {
		sessionID := "00000000-0000-0000-0000-0000000000f3"
		seedSession(t, sessionID, "Tail Multibatch Boundary")

		// Each event is 1 byte. The first page (batch size 256) covers seq
		// 300 down to seq 45 (256 rows), accumulating 256 bytes total. A
		// budget of 256 is met exactly by the last row of the first page
		// (seq 45), so pagination must stop there without fetching a second
		// page. Expect ascending seq 45..300 (256 rows).
		rows, err := db.GetSessionEventsTail(ctx, pool, sessionID, 256)
		if err != nil {
			t.Fatalf("GetSessionEventsTail: %v", err)
		}
		if len(rows) != 256 {
			t.Fatalf("expected 256 rows, got %d: first seq=%d last seq=%d", len(rows), rows[0].Seq, rows[len(rows)-1].Seq)
		}
		if rows[0].Seq != 45 || rows[len(rows)-1].Seq != totalEvents {
			t.Fatalf("expected ascending seq [45..%d], got [%d..%d]", totalEvents, rows[0].Seq, rows[len(rows)-1].Seq)
		}

		// A budget requiring one extra byte beyond the first page must pull
		// a second page and include exactly one more (older) row: seq 44.
		rows2, err := db.GetSessionEventsTail(ctx, pool, sessionID, 257)
		if err != nil {
			t.Fatalf("GetSessionEventsTail: %v", err)
		}
		if len(rows2) != 257 {
			t.Fatalf("expected 257 rows, got %d", len(rows2))
		}
		if rows2[0].Seq != 44 || rows2[len(rows2)-1].Seq != totalEvents {
			t.Fatalf("expected ascending seq [44..%d], got [%d..%d]", totalEvents, rows2[0].Seq, rows2[len(rows2)-1].Seq)
		}
	})
}

func firstID(l []db.MessageRow) string {
	if len(l) == 0 {
		return ""
	}
	return l[0].ID
}

func TestAgentEvents(t *testing.T) {
	pool := connect(t)
	setupSchema(t, pool)
	ctx := context.Background()
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.UpsertDaemon(ctx, pool, "11111111-1111-4111-8111-111111111111", "d", "local", "/r"); err != nil {
		t.Fatal(err)
	}
	sessionID := "22222222-2222-4222-8222-222222222222"
	if err := db.InsertSession(ctx, pool, sessionID, "11111111-1111-4111-8111-111111111111", "running", "/r/x", "x", "t", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionKind(ctx, pool, sessionID, "agent"); err != nil {
		t.Fatal(err)
	}

	// Sequential appends get seq 1, 2.
	ce1 := "33333333-3333-4333-8333-333333333331"
	seq1, ins1, err := db.AppendAgentEvent(ctx, pool, sessionID, ce1, "user_message", `{"text":"hi"}`)
	if err != nil || !ins1 || seq1 != 1 {
		t.Fatalf("seq1=%d ins=%v err=%v", seq1, ins1, err)
	}
	ce2 := "33333333-3333-4333-8333-333333333332"
	seq2, ins2, err := db.AppendAgentEvent(ctx, pool, sessionID, ce2, "turn_done", `{}`)
	if err != nil || !ins2 || seq2 != 2 {
		t.Fatalf("seq2=%d ins=%v err=%v", seq2, ins2, err)
	}

	// Idempotent re-append: same client_event_id → same seq, inserted=false.
	seqDup, insDup, err := db.AppendAgentEvent(ctx, pool, sessionID, ce1, "user_message", `{"text":"hi"}`)
	if err != nil || insDup || seqDup != 1 {
		t.Fatalf("dup: seq=%d ins=%v err=%v", seqDup, insDup, err)
	}

	// Windowed listing.
	events, err := db.ListAgentEvents(ctx, pool, sessionID, 1, 10)
	if err != nil || len(events) != 1 || events[0].Seq != 2 || events[0].Kind != "turn_done" {
		t.Fatalf("events=%+v err=%v", events, err)
	}

	// Model/effort mirroring.
	if err := db.UpdateSessionModelEffort(ctx, pool, sessionID, "claude-opus-5", "high"); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil || row == nil || row.Model == nil || *row.Model != "claude-opus-5" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}

func TestAgentDataPlane(t *testing.T) {
	pool := connect(t)
	setupSchema(t, pool)
	ctx := context.Background()
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Memories: upsert, re-upsert, list, delete.
	if err := db.UpsertAgentMemory(ctx, pool, "proj", "toolchain", "project", "use go 1.25", []float32{0.1, 0.2}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertAgentMemory(ctx, pool, "proj", "toolchain", "project", "use go 1.26", []float32{0.3, 0.4}); err != nil {
		t.Fatal(err)
	}
	mems, err := db.ListAgentMemories(ctx, pool, "proj")
	if err != nil || len(mems) != 1 {
		t.Fatalf("mems=%v err=%v", mems, err)
	}
	if mems[0].Content != "use go 1.26" || len(mems[0].Embedding) != 2 {
		t.Fatalf("row=%+v", mems[0])
	}
	if err := db.DeleteAgentMemory(ctx, pool, "proj", "toolchain"); err != nil {
		t.Fatal(err)
	}

	// Rules: insert (disabled), enable, filter, delete.
	id, err := db.InsertAgentRule(ctx, pool, "proj", "always run make test")
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := db.ListAgentRules(ctx, pool, "proj", true)
	if err != nil || len(enabled) != 0 {
		t.Fatalf("new rule must be disabled: %v err=%v", enabled, err)
	}
	if err := db.SetAgentRuleEnabled(ctx, pool, id, true); err != nil {
		t.Fatal(err)
	}
	enabled, err = db.ListAgentRules(ctx, pool, "proj", true)
	if err != nil || len(enabled) != 1 {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	if err := db.DeleteAgentRule(ctx, pool, id); err != nil {
		t.Fatal(err)
	}
}

// TestListSessionsByDaemonMode reproduces the exact cluster-runtime sequence
// that used to break the status dashboard's session list: a placeholder
// daemon row is created first (mode "runner", the fixed clusterDaemonID),
// then the real session pod connects as its OWN ephemeral per-session daemon
// (also mode "runner") and InsertSession's ON CONFLICT DO UPDATE rewrites
// the session's daemon_id away from the placeholder. Listing by the fixed
// daemon_id would then find nothing; listing by mode must still find it.
func TestListSessionsByDaemonMode(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	const placeholderDaemonID = "00000000-0000-4000-8000-00000000f00d"
	const podDaemonID = "00000000-0000-4000-8000-000000000001"
	const localDaemonID = "00000000-0000-4000-8000-000000000002"
	sessionID := "00000000-0000-4000-8000-0000000000aa"

	if err := db.UpsertDaemon(ctx, pool, placeholderDaemonID, "cluster", "runner", ""); err != nil {
		t.Fatalf("UpsertDaemon placeholder: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, placeholderDaemonID, "starting",
		"/workspace/repo", "org/repo", "Title", ""); err != nil {
		t.Fatalf("InsertSession (pre-connect): %v", err)
	}

	// A desktop daemon session, to prove mode filtering excludes it.
	if err := db.UpsertDaemon(ctx, pool, localDaemonID, "workstation", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon local: %v", err)
	}
	if err := db.InsertSession(ctx, pool, "00000000-0000-4000-8000-0000000000bb", localDaemonID, "running",
		"/repos/other", "org/other", "Other", ""); err != nil {
		t.Fatalf("InsertSession (local): %v", err)
	}

	rows, err := db.ListSessionsByDaemonMode(ctx, pool, "runner")
	if err != nil {
		t.Fatalf("ListSessionsByDaemonMode (pre-connect): %v", err)
	}
	if len(rows) != 1 || rows[0].ID != sessionID {
		t.Fatalf("pre-connect rows = %+v, want just %s", rows, sessionID)
	}

	// Simulate the real session pod's own hello registering a fresh daemon,
	// then its session_started rewriting the session's daemon_id — the
	// exact sequence handled by internal/server/daemon_conn.go's hello
	// handling + sessions.go's HandleSessionStarted -> InsertSession.
	if err := db.UpsertDaemon(ctx, pool, podDaemonID, "runner-"+sessionID, "runner", ""); err != nil {
		t.Fatalf("UpsertDaemon pod: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, podDaemonID, "starting",
		"/workspace/repo", "org/repo", "Title", ""); err != nil {
		t.Fatalf("InsertSession (post-connect, ON CONFLICT): %v", err)
	}

	// The old, broken approach: listing by the fixed placeholder daemon_id
	// now finds nothing, since InsertSession's ON CONFLICT overwrote it.
	byFixedID, err := db.ListSessionsByDaemon(ctx, pool, placeholderDaemonID)
	if err != nil {
		t.Fatalf("ListSessionsByDaemon: %v", err)
	}
	if len(byFixedID) != 0 {
		t.Fatalf("byFixedID = %+v, want empty (this is the bug ListSessionsByDaemonMode fixes)", byFixedID)
	}

	// The fix: listing by mode still finds it, regardless of which specific
	// per-pod daemon UUID currently owns the session.
	rows, err = db.ListSessionsByDaemonMode(ctx, pool, "runner")
	if err != nil {
		t.Fatalf("ListSessionsByDaemonMode (post-connect): %v", err)
	}
	if len(rows) != 1 || rows[0].ID != sessionID || rows[0].DaemonID != podDaemonID {
		t.Fatalf("post-connect rows = %+v, want just %s owned by %s", rows, sessionID, podDaemonID)
	}
}

// The sessions API labels each session with where it runs and what kind it is,
// so both the list queries and the single-row read must carry kind alongside
// runtime — a column that exists on every row (NOT NULL DEFAULT 'tmux').
func TestSessionRowsCarryKindAndRuntime(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	const daemonID = "00000000-0000-4000-8000-0000000000d1"
	const agentID = "00000000-0000-4000-8000-0000000000a1"
	const tmuxID = "00000000-0000-4000-8000-0000000000b1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "workstation", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	for _, id := range []string{agentID, tmuxID} {
		if err := db.InsertSession(ctx, pool, id, daemonID, "running", "/repos/app", "app", "T", ""); err != nil {
			t.Fatalf("InsertSession %s: %v", id, err)
		}
	}
	if err := db.SetSessionKind(ctx, pool, agentID, "agent"); err != nil {
		t.Fatalf("SetSessionKind: %v", err)
	}
	if err := db.SetSessionPosture(ctx, pool, agentID, "docker", false); err != nil {
		t.Fatalf("SetSessionPosture: %v", err)
	}

	row, err := db.GetSession(ctx, pool, agentID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v (row %v)", err, row)
	}
	if row.Kind != "agent" {
		t.Errorf("GetSession kind = %q, want agent", row.Kind)
	}

	rows, err := db.ListSessions(ctx, pool)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	kinds := map[string]string{}
	for _, r := range rows {
		kinds[r.ID] = r.Kind
	}
	if kinds[agentID] != "agent" {
		t.Errorf("ListSessions kind for the agent session = %q, want agent", kinds[agentID])
	}
	if kinds[tmuxID] != "tmux" {
		t.Errorf("ListSessions kind for the terminal session = %q, want tmux", kinds[tmuxID])
	}
}
