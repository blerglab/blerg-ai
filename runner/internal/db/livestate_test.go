package db_test

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
)

// The migration's predicate is the very list LiveKindsSQL writes out, and it is the reducer's list.
func TestLiveIndexMatchesTheKinds(t *testing.T) {
	sql, err := os.ReadFile("migrations/042_agent_events_live_idx.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sql), "WHERE kind IN "+db.LiveKindsSQL) {
		t.Errorf("042 must index exactly %s", db.LiveKindsSQL)
	}
	var kinds []string
	for _, k := range strings.Split(strings.Trim(db.LiveKindsSQL, "()"), ",") {
		kinds = append(kinds, strings.Trim(strings.TrimSpace(k), "'"))
	}
	if !reflect.DeepEqual(kinds, livestate.Kinds) {
		t.Errorf("LiveKindsSQL %v != livestate.Kinds %v", kinds, livestate.Kinds)
	}
}

func kindsOf(rows []db.AgentEventRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Kind)
	}
	return out
}

func TestListLiveEventsReadsTheLiveKindsButUpdates(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	for i, k := range []string{"ask", "tool_call", "user_message", "subagent_started", "subagent_update", "subagent_done",
		"background_task_started", "background_task_done", "turn_done", "status_changed", "assistant_text"} {
		putEvent(t, pool, sid, int64(i+1), k, `{}`)
	}
	got, err := db.ListLiveEvents(ctx, pool, sid, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ask", "user_message", "subagent_started", "subagent_done", "background_task_started", "background_task_done", "turn_done"}
	if !reflect.DeepEqual(kindsOf(got), want) {
		t.Errorf("kinds = %v, want %v", kindsOf(got), want)
	}
}

// A running subagent's tail: its two newest updates and its newest own row after the older of them.
func TestListSubagentTails(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	ev := []struct{ kind, payload string }{
		{"subagent_started", `{"child_id":"A"}`},                 // 1
		{"subagent_update", `{"child_id":"A","model":"m1"}`},     // 2
		{"tool_call", `{"call_id":"x1","parent_call_id":"A"}`},   // 3
		{"subagent_update", `{"child_id":"A","model":"m2"}`},     // 4
		{"assistant_text", `{"text":"hi","parent_call_id":"A"}`}, // 5
		{"subagent_update", `{"child_id":"A","model":"m3"}`},     // 6
		{"tool_call", `{"call_id":"y1","parent_call_id":"B"}`},   // 7: another subagent's
		{"tool_call", `{"call_id":"main"}`},                      // 8: the main agent's
		{"subagent_started", `{"child_id":"B"}`},                 // 9
		{"tool_call", `{"call_id":"y2","parent_call_id":"B"}`},   // 10
		{"tool_call", `{"call_id":"x2","parent_call_id":"A"}`},   // 11: past through
		{"subagent_update", `{"child_id":"A","model":"m4"}`},     // 12: past through
	}
	for i, e := range ev {
		putEvent(t, pool, sid, int64(i+1), e.kind, e.payload)
	}
	got, err := db.ListSubagentTails(ctx, pool, sid, 10, []livestate.Tail{{Key: "A", Seq: 1}, {Key: "B", Seq: 9}, {Key: "C", Seq: 1}})
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	for _, r := range got {
		seqs = append(seqs, r.Seq)
	}
	// A: updates 6 and 4, its newest row after 4 (5); B: no update, its newest row after its start (10).
	if want := []int64{4, 5, 6, 10}; !reflect.DeepEqual(seqs, want) {
		t.Errorf("seqs = %v, want %v", seqs, want)
	}
	if got, err := db.ListSubagentTails(ctx, pool, sid, 10, nil); err != nil || len(got) != 0 {
		t.Errorf("no tails = %v %v", got, err)
	}
}

func TestAskRowStates(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	open, err := db.CreateMessage(ctx, pool, sid, "ask", "Ship it?")
	if err != nil {
		t.Fatal(err)
	}
	answered, _ := db.CreateMessage(ctx, pool, sid, "ask", "Which branch?")
	if _, err := db.AnswerMessage(ctx, pool, answered, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateMessage(ctx, pool, sid, "note", "fyi"); err != nil {
		t.Fatal(err)
	}
	st, err := db.ReadAskRows(ctx, pool, sid)
	if err != nil {
		t.Fatal(err)
	}
	if st.Open != 1 || st.Total != 2 || !st.Closed[answered] || st.Closed[open] {
		t.Errorf("ask rows = %+v (open %s, answered %s)", st, open, answered)
	}
}

// The bar: 100k events in one session, 56 % of them subagent updates (a real session's mix), and the
// live query reads the partial index with kind as an index condition.
func TestLiveQueryUsesThePartialIndex(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload)
		SELECT $1, g, gen_random_uuid(),
		       CASE WHEN g % 50 = 0 THEN 'user_message' WHEN g % 50 = 1 THEN 'background_task_started'
		            WHEN g % 50 BETWEEN 2 AND 29 THEN 'subagent_update' ELSE 'tool_result' END,
		       CASE WHEN g % 50 = 0 THEN jsonb_build_object('text', 'do ' || g, 'source', 'chat')
		            WHEN g % 50 = 1 THEN jsonb_build_object('task_id', 'b' || g)
		            WHEN g % 50 BETWEEN 2 AND 29 THEN jsonb_build_object('child_id', 'A' || (g / 50), 'usage', jsonb_build_object('output_tokens', g))
		            ELSE jsonb_build_object('call_id', 'c' || g, 'output', repeat('x', 200)) END
		  FROM generate_series(1, 100000) AS g`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE agent_events`); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `EXPLAIN `+db.LiveEventsSQL, sid, int64(100000))
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
	if !strings.Contains(strings.Join(plan, "\n"), "agent_events_live_idx") {
		t.Errorf("the live query does not use agent_events_live_idx:\n%s", strings.Join(plan, "\n"))
	}
	// kind is an index column: the subagent_update entries (56 % of this session) are never fetched
	// from the heap to be rejected by a Filter.
	for _, line := range plan {
		if strings.Contains(line, "Filter:") && strings.Contains(line, "kind") {
			t.Errorf("the live query filters kind on the heap:\n%s", strings.Join(plan, "\n"))
		}
	}
	start := time.Now()
	got, err := db.ListLiveEvents(ctx, pool, sid, 100000)
	if err != nil || len(got) != 4000 {
		t.Fatalf("ListLiveEvents = %d rows, %v; want 4000", len(got), err)
	}
	t.Logf("read 4000 live events of 100k in %v", time.Since(start))
}
