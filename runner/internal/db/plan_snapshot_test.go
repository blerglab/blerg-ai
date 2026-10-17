package db_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestPlanReadLatestValidSnapshotAndLaterTools(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	events := []struct{ kind, payload string }{
		{"tool_call", `{"tool":"TaskCreate","call_id":"old","input":{"subject":"Old"}}`},
		{"plan_updated", `{"items":[{"text":"First","status":"pending"}]}`},
		{"plan_updated", `{"items":[{"text":"Newest","status":"in_progress"}]}`},
		{"tool_call", `{"tool":"TaskCreate","call_id":"new","input":{"subject":"Later"}}`},
		{"tool_result", `{"call_id":"new","output":"Task #1 created successfully: Later"}`},
		{"plan_updated", `{"items":null}`},
		{"plan_updated", `{"items":[{"text":"Child","status":"completed"}],"child_id":"child"}`},
		{"plan_updated", `{"items":[]}`},
	}
	for i, event := range events {
		putEvent(t, pool, sid, int64(i+1), event.kind, event.payload)
	}
	for _, tc := range []struct {
		through int64
		seqs    []int64
	}{{1, []int64{1}}, {2, []int64{2}}, {3, []int64{3}}, {7, []int64{3, 4, 5}}, {8, []int64{8}}} {
		rows, err := db.ListPlanEvents(t.Context(), pool, sid, tc.through)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, row := range rows {
			got = append(got, row.Seq)
		}
		if !reflect.DeepEqual(got, tc.seqs) {
			t.Fatalf("through%d seqs%v want%v", tc.through, got, tc.seqs)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := db.ListPlanEvents(ctx, pool, sid, 100); err == nil {
		t.Fatal("canceled snapshot scan ignored context")
	}
}

func TestPlanSnapshotReadUsesNewestFirstPartialIndex(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	if _, err := pool.Exec(t.Context(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload)
 SELECT $1,g,gen_random_uuid(),CASE WHEN g%10=0 THEN 'plan_updated' ELSE 'tool_result' END,'{"items":[]}'::jsonb FROM generate_series(1,10000) g`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `ANALYZE agent_events`); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(t.Context(), `EXPLAIN `+db.LatestPlanSnapshotSQL, sid, int64(10000))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "agent_events_latest_plan_updated_idx") || !strings.Contains(plan.String(), "Limit") || strings.Contains(plan.String(), "Sort") {
		t.Fatalf("latest snapshot query lacks bounded index plan:\n%s", plan.String())
	}
}
