package db_test

import (
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestMigration128ConvertsAlertsButKeepsRealQuestions(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	daemon := mustDaemon(t, pool)
	session := mustSession(t, pool, daemon, "owner-notices")
	insert := func(kind, status string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO messages(session_id,kind,body,status,owner_only,no_wait) VALUES($1,$2,'Check the saved work',$3,true,true) RETURNING id`, session, kind, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	question := insert("ask", "open")
	for _, status := range []string{"open", "answered", "withdrawn"} {
		id := insert("ask", status)
		if _, err := pool.Exec(ctx, `INSERT INTO session_failure_alerts(session_id,occurrence,turn_seq,message_id) VALUES($1,$2,0,$3)`, session, status, id); err != nil {
			t.Fatal(err)
		}
	}
	// Replay the upgrade over populated data, rather than testing only fresh installs.
	if _, err := pool.Exec(ctx, `ALTER TABLE session_failure_alerts DROP COLUMN notice_event_id; DELETE FROM schema_migrations WHERE filename='128_failure_notices.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q, err := db.GetMessage(ctx, pool, question)
	if err != nil || q.Kind != "ask" || q.Status != "open" {
		t.Fatalf("real question changed: %+v %v", q, err)
	}
	var notices, events, asks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE session_id=$1 AND kind='update' AND status<>'open' AND NOT owner_only`, session).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='update'`, session).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE session_id=$1 AND kind='ask' AND status='open'`, session).Scan(&asks); err != nil {
		t.Fatal(err)
	}
	if notices != 3 || events != 3 || asks != 1 {
		t.Fatalf("notices=%d events=%d asks=%d", notices, events, asks)
	}
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var again int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='update'`, session).Scan(&again); err != nil || again != events {
		t.Fatalf("migration duplicated notices: %d %v", again, err)
	}
}
