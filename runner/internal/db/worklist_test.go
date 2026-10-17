package db_test

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
)

// The plan read repeats 055's predicate word for word (so the planner proves the partial index), and
// its tools are the reducer's.
func TestPlanIndexMatchesTheRead(t *testing.T) {
	sql, err := os.ReadFile("migrations/055_session_worklist_idx.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sql), "WHERE "+strings.TrimSuffix(strings.TrimPrefix(db.PlanToolsSQL, "("), ")")+";") {
		t.Errorf("055 must index exactly %s", db.PlanToolsSQL)
	}
	read := db.PlanToolsSQL
	for _, tool := range livestate.PlanTools {
		if !strings.Contains(read, "'"+tool+"'") {
			t.Errorf("PlanToolsSQL lacks %s", tool)
		}
	}
}

func TestWorklistReads(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid, other := reelSession(t, pool), reelSession(t, pool)
	putEvent(t, pool, sid, 1, "checklist", `{"items":[{"text":"a","done":false}],"op":"set"}`)
	putEvent(t, pool, sid, 2, "tool_call", `{"tool":"TaskCreate","call_id":"c1","input":{"subject":"s"}}`)
	putEvent(t, pool, sid, 3, "tool_result", `{"call_id":"c1","output":"Task #1 created successfully: s"}`)
	putEvent(t, pool, sid, 4, "tool_call", `{"tool":"Bash","call_id":"b1","input":{}}`)
	putEvent(t, pool, sid, 5, "tool_result", `{"call_id":"b1","output":"ok"}`)
	putEvent(t, pool, sid, 6, "ticket", `{"action":"pin","ticket":{"source":"jira","key":"A-1","title":"T","status":"Open","url":"https://x.example/A-1","description":"long"}}`)
	putEvent(t, pool, sid, 7, "checklist", `{"items":[{"text":"a","done":true}],"op":"tick"}`)
	putEvent(t, pool, sid, 8, "tool_call", `{"tool":"TaskUpdate","call_id":"u1","input":{"taskId":"1","status":"completed"}}`)
	putEvent(t, pool, other, 1, "checklist", `{"items":[],"op":"clear"}`)

	c, err := db.LatestChecklist(ctx, pool, sid, 6)
	if err != nil || c == nil || c.Seq != 1 {
		t.Fatalf("checklist through 6: %+v %v", c, err)
	}
	c, err = db.LatestChecklist(ctx, pool, sid, 8)
	if err != nil || c == nil || c.Seq != 7 {
		t.Fatalf("checklist: %+v %v", c, err)
	}
	if n, err := db.CountSessionKind(ctx, pool, sid, "checklist"); err != nil || n != 2 {
		t.Fatalf("count %d %v", n, err)
	}
	rows, err := db.ListTicketRows(ctx, pool, sid, 8)
	if err != nil || len(rows) != 1 || rows[0].Key != "A-1" || rows[0].Action != "pin" || rows[0].URL != "https://x.example/A-1" || rows[0].Seq != 6 {
		t.Fatalf("tickets: %+v %v", rows, err)
	}
	raw, err := db.TicketAt(ctx, pool, sid, 6)
	if err != nil || !strings.Contains(string(raw), `"description": "long"`) && !strings.Contains(string(raw), `"description":"long"`) {
		t.Fatalf("ticket: %s %v", raw, err)
	}
	plan, err := db.ListPlanEvents(ctx, pool, sid, 8)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	for _, r := range plan {
		seqs = append(seqs, r.Seq)
	}
	if !reflect.DeepEqual(seqs, []int64{2, 3, 8}) {
		t.Fatalf("plan seqs %v", seqs)
	}
	if c, err := db.LatestChecklist(ctx, pool, reelSession(t, pool), 100); err != nil || c != nil {
		t.Fatalf("none: %+v %v", c, err)
	}
}

// Each read walks its partial index.
func TestWorklistReadsUseTheirIndexes(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"agent_events_checklist_idx": `EXPLAIN SELECT seq FROM agent_events WHERE session_id = '0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d01' AND kind = 'checklist' AND seq <= 9 ORDER BY seq DESC LIMIT 1`,
		"agent_events_ticket_idx":    `EXPLAIN SELECT seq FROM agent_events WHERE session_id = '0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d01' AND kind = 'ticket' AND seq <= 9 ORDER BY seq`,
		"agent_events_plan_idx":      `EXPLAIN SELECT seq FROM agent_events WHERE session_id = '0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d01' AND seq <= 9 AND ` + db.PlanToolsSQL + ` ORDER BY seq`,
	} {
		rows, err := pool.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var line string
			_ = rows.Scan(&line)
			plan.WriteString(line + "\n")
		}
		rows.Close()
		if !strings.Contains(plan.String(), name) {
			t.Errorf("%s not used:\n%s", name, plan.String())
		}
	}
}
