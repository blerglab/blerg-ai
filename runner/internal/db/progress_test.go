package db_test

import (
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
	"testing"
	"time"
)

func TestScopeProgressReadsSeparateLatestSnapshotsAndResumedFloor(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	events := []struct{ kind, payload string }{
		{"plan_updated", `{"items":[{"text":"Root","status":"completed"}]}`},
		{"plan_updated", `{"items":[{"text":"Child","status":"pending"}],"child_id":"child"}`},
		{"progress_updated", `{"completed":2,"total":7,"label":"Root script"}`},
		{"progress_updated", `{"completed":3,"total":9,"label":"Child script","child_id":"child"}`},
		{"plan_updated", `{"items":null,"child_id":"child"}`},
		{"tool_call", `{"tool":"TodoWrite","parent_call_id":"child","input":{"todos":[{"content":"Resumed child","status":"completed"}]}}`},
	}
	for i, e := range events {
		putEvent(t, pool, sid, int64(i+1), e.kind, e.payload)
	}
	for _, tc := range []struct {
		key              string
		since, through   int64
		label            string
		completed, total int
	}{{"", 0, 6, "Root script", 2, 7}, {"child", 0, 5, "Child script", 3, 9}, {"child", 4, 6, "Resumed child", 1, 1}} {
		rows, err := db.ListScopeProgressEvents(t.Context(), pool, sid, tc.through, tc.key, tc.since)
		if err != nil {
			t.Fatal(err)
		}
		var input []livestate.Event
		for _, r := range rows {
			input = append(input, livestate.Event{Seq: r.Seq, Kind: r.Kind, Payload: json.RawMessage(r.Payload), Ts: r.Ts})
		}
		p := livestate.ProgressFor(input, tc.key, tc.since)
		if p == nil || p.Label != tc.label || p.Completed != tc.completed || p.Total != tc.total {
			t.Fatalf("scope %q floor%d = %+v", tc.key, tc.since, p)
		}
	}
}

func TestChildProgressAuthorityRejectsEndedReplacedAndPreviousIncarnations(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	var daemon string
	if err := pool.QueryRow(t.Context(), `SELECT daemon_id FROM sessions WHERE id=$1`, sid).Scan(&daemon); err != nil {
		t.Fatal(err)
	}
	freshID := func() string {
		var id string
		if err := pool.QueryRow(t.Context(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	at := time.Now().UTC()
	appendLife := func(kind string, when time.Time) {
		t.Helper()
		if _, _, err := db.AppendAgentEventAt(t.Context(), pool, sid, freshID(), kind, `{"child_id":"child"}`, when); err != nil {
			t.Fatal(err)
		}
	}
	progress := func(child, owner string, when time.Time) error {
		_, _, err := db.AppendChildProgressAt(t.Context(), pool, sid, freshID(), "progress_updated", `{"completed":1,"total":2,"label":"Work","child_id":"child"}`, child, owner, when)
		return err
	}
	if err := progress("child", daemon, at); !errors.Is(err, db.ErrBackgroundTaskAuthority) {
		t.Fatal("unknown child admitted")
	}
	appendLife("subagent_started", at)
	if err := progress("child", "wrong-daemon", at.Add(time.Second)); !errors.Is(err, db.ErrBackgroundTaskAuthority) {
		t.Fatal("wrong daemon admitted")
	}
	if err := progress("child", daemon, at.Add(-time.Second)); !errors.Is(err, db.ErrBackgroundTaskAuthority) {
		t.Fatal("pre-start event admitted")
	}
	if err := progress("child", daemon, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	appendLife("subagent_done", at.Add(2*time.Second))
	if err := progress("child", daemon, at.Add(3*time.Second)); !errors.Is(err, db.ErrBackgroundTaskAuthority) {
		t.Fatal("ended child admitted")
	}
	appendLife("subagent_started", at.Add(4*time.Second))
	if err := progress("child", daemon, at.Add(3*time.Second)); !errors.Is(err, db.ErrBackgroundTaskAuthority) {
		t.Fatal("previous incarnation admitted")
	}
	if err := progress("child", daemon, at.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET status='ended' WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if err := progress("child", daemon, at.Add(6*time.Second)); !errors.Is(err, db.ErrBackgroundTaskAuthority) {
		t.Fatal("ended session admitted")
	}
}
