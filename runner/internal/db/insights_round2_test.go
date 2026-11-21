package db_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestInsightTimingFromPersistedEngineEvents(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "round2-owner")
	for _, tc := range []struct {
		name, tool, edit, field, fail, pass string
		first, review                       bool
	}{
		{"claude", "Bash", "Edit", "command", `"is_error":true`, `"is_error":false`, false, false},
		{"codex", "functions.exec_command", "apply_patch", "cmd", `"exit_code":1`, `"exit_code":0`, false, false},
		{"test-first", "Bash", "Edit", "command", `"is_error":true`, `"is_error":false`, true, false},
		{"review-worker", "Bash", "Edit", "command", `"is_error":true`, `"is_error":false`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "round2-owner", ParentSessionID: parent})
			start := time.Now().UTC().Add(-time.Hour).Truncate(time.Hour)
			if _, err := pool.Exec(t.Context(), `UPDATE sessions SET started_at=$2,ended_at=$2::timestamptz+interval '50 seconds' WHERE id=$1`, worker, start); err != nil {
				t.Fatal(err)
			}
			event := func(at int, kind, payload string) {
				t.Helper()
				if _, _, err := db.AppendAgentEventAt(t.Context(), pool, worker, newUUID(t, pool), kind, payload, start.Add(time.Duration(at)*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			event(0, "status_changed", `{"status":"running"}`)
			if !tc.first {
				payload := fmt.Sprintf(`{"tool":%q,"call_id":"edit","input":{"file_path":"widget.go","old_string":"a","new_string":"b"}}`, tc.edit)
				if tc.edit == "apply_patch" {
					payload = `{"tool":"apply_patch","call_id":"edit","input":[{"path":"widget.go","kind":{"type":"update"},"diff":"@@ -1 +1 @@\n-a\n+b"}]}`
				}
				event(0, "tool_call", payload)
				event(5, "tool_result", `{"call_id":"edit",`+tc.pass+`}`)
			} else {
				event(0, "tool_call", `{"tool":"Write","call_id":"test-edit","input":{"file_path":"widget_test.go"}}`)
				event(5, "tool_result", `{"call_id":"test-edit","is_error":false}`)
			}
			call := func(id string) string {
				command := "go test ./internal/db -run TestX 2>&1 | tail -30"
				if id == "green" {
					command = "go test ./..."
				}
				return fmt.Sprintf(`{"tool":%q,"call_id":%q,"input":{%q:%q}}`, tc.tool, id, tc.field, command)
			}
			event(10, "tool_call", call("red"))
			event(20, "tool_result", `{"call_id":"red",`+tc.fail+`}`)
			event(21, "mark", `{"kind":"failure","type":"test","suite":"db","severity":"high"}`)
			event(25, "status_changed", `{"status":"running"}`)
			event(30, "tool_call", call("green"))
			event(40, "tool_result", `{"call_id":"green",`+tc.pass+`}`)
			if tc.review {
				event(49, "mark", `{"kind":"review","verdict":"approved","by":"codex","round":1}`)
			}
			if err := db.RefreshInsightTiming(t.Context(), pool, worker); err != nil {
				t.Fatal(err)
			}
			raw, err := db.InsightActivityTimings(t.Context(), pool, "round2-owner", []string{worker})
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Sessions []struct{ Timing activity.Timing }
			}
			if err = json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			timing := result.Sessions[0].Timing
			wantFix := 10.0
			if tc.first || tc.review {
				wantFix = 0
			}
			if timing.Phases["fixing"] != wantFix || timing.Hours[0].Detail["building.rework"] != wantFix {
				t.Fatalf("persisted phase/stage: %+v", timing)
			}
			if tc.review && timing.Phases["reviewing"] != 29 {
				t.Fatalf("review time lost: %v", timing.Phases)
			}
			var runs, passed, failed int
			for _, r := range timing.GateRuns {
				runs += r.Runs
				passed += r.Passed
				failed += r.Failed
			}
			if runs != 2 || passed != 1 || failed != 1 || timing.Phases["gates"] != 20 {
				t.Fatalf("gate evidence lost: %+v", timing)
			}
		})
	}
}
