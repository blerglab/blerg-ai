package db_test

import (
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Migration 101 on a database that already holds questions: open child asks stay with the person
// they already reached, without a second notification; everything else keeps its meaning.
func TestMigration101KeepsExistingQuestions(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-101")
	child := mustSession(t, pool, daemon, "owner-101")
	if _, err := pool.Exec(ctx, `UPDATE sessions SET parent_session_id=$1 WHERE id=$2`, parent, child); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE messages DROP COLUMN child_escalated, DROP COLUMN child_parent_notified, DROP COLUMN child_person_notified,
		DROP COLUMN person_required, DROP COLUMN answered_by, DROP COLUMN escalated_by, DROP COLUMN escalation_reason, DROP COLUMN waiter_ended, DROP COLUMN host_generation;
		DELETE FROM schema_migrations WHERE filename='101_child_question_escalation.sql'`); err != nil {
		t.Fatal(err)
	}
	insert := func(session, status string, ownerOnly, noWait bool) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO messages(session_id,kind,body,status,owner_only,no_wait) VALUES ($1,'ask','Question?',$2,$3,$4) RETURNING id`, session, status, ownerOnly, noWait).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	plain := insert(child, "open", false, false)
	approval := insert(child, "open", true, false)
	noWait := insert(child, "open", true, true)
	closed := insert(child, "answered", false, false)
	topLevel := insert(parent, "open", false, false)
	alert := insert(child, "open", false, false)
	if _, err := pool.Exec(ctx, `INSERT INTO session_failure_alerts(session_id,occurrence,turn_seq,message_id,parent_session_id) VALUES ($1,'fixture',1,$2,$3)`, child, alert, parent); err != nil {
		t.Fatal(err)
	}

	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		id        string
		escalated bool
		required  bool
	}{
		"open child ask":      {plain, true, false},
		"open child approval": {approval, true, true},
		"open no_wait":        {noWait, true, false},
		"closed child ask":    {closed, false, false},
		"top-level ask":       {topLevel, false, false},
		"failure alert":       {alert, false, false},
	} {
		row, err := db.GetMessage(ctx, pool, tc.id)
		if err != nil || row == nil {
			t.Fatalf("%s: %v", name, err)
		}
		if row.ChildEscalated != tc.escalated || row.PersonRequired != tc.required {
			t.Errorf("%s: escalated=%t required=%t", name, row.ChildEscalated, row.PersonRequired)
		}
		if tc.escalated && (row.EscalatedBy != "system" || row.EscalationReason != "upgrade") {
			t.Errorf("%s: audit %q %q", name, row.EscalatedBy, row.EscalationReason)
		}
		var notified bool
		if err := pool.QueryRow(ctx, `SELECT child_person_notified FROM messages WHERE id=$1`, tc.id).Scan(&notified); err != nil {
			t.Fatal(err)
		}
		if notified != tc.escalated {
			t.Errorf("%s: person notified=%t", name, notified)
		}
		if row.WaiterEnded || row.AnsweredBy != "" {
			t.Errorf("%s: invented state: %+v", name, row)
		}
	}
}
