package db_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestAgentSubagentContextIsBoundedFencedAndSessionScoped(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	const own = "00000000-0000-4000-8000-0000000000e1"
	const other = "00000000-0000-4000-8000-0000000000e2"
	endTestSession(t, p, own, "idle")
	endTestSession(t, p, other, "idle")
	for i, event := range []struct{ session, kind, payload string }{
		{own, "subagent_started", `{"child_id":"child"}`},
		{own, "subagent_update", `{"child_id":"child","summary":"Old progress"}`},
		{own, "subagent_update", `{"child_id":"child","summary":"Newest progress"}`},
		{own, "subagent_done", `{"child_id":"child"}`},
		{other, "subagent_done", `{"child_id":"child","summary":"Other session"}`},
	} {
		if _, _, err := db.AppendAgentEvent(ctx, p, event.session, fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), event.kind, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.ListAgentSubagentContext(ctx, p, own, []string{"child"}, 3)
	if err != nil || len(rows) != 2 || rows[0].Seq != 1 || rows[1].Seq != 3 {
		t.Fatalf("context crossed fence or did not choose latest metadata: %+v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.SessionID != own {
			t.Fatal("context crossed session authorization boundary")
		}
	}
	rows, err = db.ListAgentSubagentContext(ctx, p, own, []string{"child"}, 4)
	if err != nil || len(rows) != 3 || rows[2].Kind != "subagent_done" {
		t.Fatalf("complete context = %+v err=%v", rows, err)
	}
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = "child"
	}
	rows, err = db.ListAgentSubagentContext(ctx, p, own, ids, 4)
	if err != nil || len(rows) != 1500 {
		t.Fatalf("context lookup exceeded its 500-child bound: %d err=%v", len(rows), err)
	}
	// A lookup uses the child-specific partial index, rather than walking all
	// lifecycle updates from a long session. Disable seqscan for this tiny fixture.
	if _, err = p.Exec(ctx, "SET enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	plan, err := p.Query(ctx, `EXPLAIN SELECT seq FROM agent_events WHERE session_id=$1 AND payload->>'child_id'=$2 AND kind='subagent_update' AND seq<=4 ORDER BY seq DESC LIMIT 1`, own, "child")
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	var lines []string
	for plan.Next() {
		var line string
		if err = plan.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err = plan.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "agent_events_child_lifecycle_idx") {
		t.Fatal("context lookup did not use the child lifecycle index")
	}
}
