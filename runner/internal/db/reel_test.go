package db_test

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The migration's predicate is the very list the query writes out (so the planner can prove the
// query implies it), and it is the reducer's list.
func TestReelIndexMatchesTheKinds(t *testing.T) {
	sql, err := os.ReadFile("migrations/041_agent_events_reel_idx.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sql), "WHERE kind IN "+db.ReelKindsSQL) {
		t.Errorf("041 must index exactly %s", db.ReelKindsSQL)
	}
	var kinds []string
	for _, k := range strings.Split(strings.Trim(db.ReelKindsSQL, "()"), ",") {
		kinds = append(kinds, strings.Trim(strings.TrimSpace(k), "'"))
	}
	if !reflect.DeepEqual(kinds, topics.Kinds) {
		t.Errorf("ReelKindsSQL %v != topics.Kinds %v", kinds, topics.Kinds)
	}
}

func reelTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	ctx := context.Background()
	const schema = "reel_dbreel"
	boot, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema), fmt.Sprintf("CREATE SCHEMA %s", schema)} {
		if _, err := boot.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = boot.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
		boot.Close()
	})
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return pool
}

func reelSession(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	const daemon = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0e"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, id, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	return id
}

func putEvent(t *testing.T, pool *pgxpool.Pool, sid string, seq int64, kind, payload string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload) VALUES ($1, $2, gen_random_uuid(), $3, $4)`,
		sid, seq, kind, payload); err != nil {
		t.Fatal(err)
	}
}

func TestListReelEventsReadsTheReelKindsInOrder(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid, other := reelSession(t, pool), reelSession(t, pool)
	for i, k := range []string{"user_message", "tool_call", "topic", "assistant_text", "artifact", "subagent_started",
		"subagent_update", "subagent_done", "turn_done", "status_changed", "user_message"} {
		putEvent(t, pool, sid, int64(i+1), k, `{"n":1}`)
	}
	putEvent(t, pool, other, 1, "user_message", `{}`)
	rows, err := db.ListReelEvents(ctx, pool, sid, 10)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	for _, r := range rows {
		seqs = append(seqs, r.Seq)
		if r.Payload != `{"n": 1}` || r.ClientEventID == "" || r.Ts.IsZero() {
			t.Errorf("row %+v", r)
		}
	}
	if !reflect.DeepEqual(seqs, []int64{1, 3, 5, 6, 7, 8, 9, 10}) {
		t.Errorf("seqs = %v: the reel kinds up to through_seq, in order", seqs)
	}
	if n, err := db.MaxAgentEventSeq(ctx, pool, sid); err != nil || n != 11 {
		t.Errorf("MaxAgentEventSeq = %d, %v; want 11", n, err)
	}
	if n, err := db.MaxAgentEventSeq(ctx, pool, reelSession(t, pool)); err != nil || n != 0 {
		t.Errorf("a session with no events = %d, %v", n, err)
	}
}

func TestArtifactStamp(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	n, newest, err := db.ArtifactStamp(ctx, pool, sid)
	if err != nil || n != 0 || !newest.Equal(time.Unix(0, 0)) {
		t.Fatalf("no files = %d %v %v; want 0 and the epoch", n, newest, err)
	}
	var last db.ArtifactRow
	for i, name := range []string{"a.md", "b.md"} {
		last, err = db.InsertArtifact(ctx, pool, db.ArtifactRow{ID: fmt.Sprintf("art%d", i), SessionID: sid, Name: name, Size: 1, ContentType: "text/markdown", SHA256: "x"}, 50)
		if err != nil {
			t.Fatal(err)
		}
	}
	n, newest, err = db.ArtifactStamp(ctx, pool, sid)
	if err != nil || n != 2 || !newest.Equal(last.CreatedAt) {
		t.Errorf("two files = %d %v %v; want 2 and %v", n, newest, err, last.CreatedAt)
	}
	if _, err := db.DeleteArtifact(ctx, pool, sid, "art0"); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := db.ArtifactStamp(ctx, pool, sid); n != 1 {
		t.Errorf("a deletion moves the count: %d", n)
	}
}

func TestAppendAgentEventTxAndTopicState(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	putEvent(t, pool, sid, 5, "user_message", `{}`)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockTopics(ctx, tx, sid); err != nil {
		t.Fatal(err)
	}
	if latest, n, err := db.TopicState(ctx, tx, sid); err != nil || latest != nil || n != 0 {
		t.Fatalf("no topic yet = %+v %d %v", latest, n, err)
	}
	const id1, id2 = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	seq, ins, err := db.AppendAgentEventTx(ctx, tx, sid, id1, "topic", `{"id":"tp_1","action":"start"}`, time.Now())
	if err != nil || !ins || seq != 6 {
		t.Fatalf("first append = %d %v %v; want seq 6 after the existing 5", seq, ins, err)
	}
	again, ins, err := db.AppendAgentEventTx(ctx, tx, sid, id1, "topic", `{"id":"tp_1","action":"start"}`, time.Now())
	if err != nil || ins || again != 6 {
		t.Errorf("the same client id = %d %v %v; want the existing seq, not inserted", again, ins, err)
	}
	if _, _, err := db.AppendAgentEventTx(ctx, tx, sid, id2, "topic", `{"id":"tp_1","action":"done"}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	latest, n, err := db.TopicState(ctx, tx, sid)
	if err != nil || n != 2 || latest == nil || latest.Seq != 7 || !strings.Contains(latest.Payload, `"done"`) {
		t.Errorf("TopicState = %+v %d %v; want the done at seq 7 and a count of 2", latest, n, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// The bar of spec §5: 100k events in one session, and the reel query reads the partial index.
func TestReelQueryUsesThePartialIndex(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload)
		SELECT $1, g, gen_random_uuid(),
		       CASE WHEN g % 50 = 0 THEN 'user_message' WHEN g % 50 = 1 THEN 'subagent_started' ELSE 'tool_result' END,
		       CASE WHEN g % 50 = 0 THEN jsonb_build_object('text', 'do ' || g, 'source', 'chat')
		            WHEN g % 50 = 1 THEN jsonb_build_object('child_id', 'A' || g)
		            ELSE jsonb_build_object('call_id', 'c' || g, 'output', repeat('x', 200)) END
		  FROM generate_series(1, 100000) AS g`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE agent_events`); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `EXPLAIN `+db.ReelEventsSQL, sid, int64(100000))
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	rows.Close()
	if !strings.Contains(strings.Join(plan, "\n"), "agent_events_reel_idx") {
		t.Errorf("the reel query does not use agent_events_reel_idx:\n%s", strings.Join(plan, "\n"))
	}
	start := time.Now()
	got, err := db.ListReelEvents(ctx, pool, sid, 100000)
	if err != nil || len(got) != 4000 {
		t.Fatalf("ListReelEvents = %d rows, %v; want 4000", len(got), err)
	}
	t.Logf("read 4000 reel events of 100k in %v", time.Since(start))
}

// A seq collision inside the caller's transaction is retried under a savepoint: the failed attempt
// does not abort the transaction, and the retry takes the next seq.
func TestAppendAgentEventTxRetriesASeqCollision(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	putEvent(t, pool, sid, 5, "user_message", `{}`)
	other, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	if _, _, err := db.AppendAgentEventTx(ctx, other, sid, "33333333-3333-4333-8333-333333333333", "topic", `{}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var txPid int // the backend tx runs on: the one whose lock wait to watch (the test DB is shared)
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&txPid); err != nil {
		t.Fatal(err)
	}
	type result struct {
		seq int64
		ins bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		seq, ins, err := db.AppendAgentEventTx(ctx, tx, sid, "44444444-4444-4444-8444-444444444444", "topic", `{}`, time.Time{})
		done <- result{seq, ins, err}
	}()
	// Wait until tx's insert of seq 6 waits on other's uncommitted 6, then commit other.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE NOT granted AND pid = $1`, txPid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second append never waited on the first")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := other.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || !r.ins || r.seq != 7 {
		t.Fatalf("append after a collision = %d %v %v; want seq 7, inserted", r.seq, r.ins, r.err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("the transaction must survive the failed attempt: %v", err)
	}
	if n, err := db.MaxAgentEventSeq(ctx, pool, sid); err != nil || n != 7 {
		t.Errorf("MaxAgentEventSeq = %d, %v; want 7", n, err)
	}
}
