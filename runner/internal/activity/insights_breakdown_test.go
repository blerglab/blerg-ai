package activity

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestInsightsBreakdownsPartitionTheRecordedTime(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	event := func(at int, kind, payload string) Event {
		return Event{At: start.Add(time.Duration(at) * time.Second), Kind: kind, Payload: json.RawMessage(payload)}
	}
	events := []Event{
		event(0, "status_changed", `{"status":"running"}`),
		event(10, "tool_call", `{"tool":"Bash","call_id":"test","input":{"command":"go test ./internal/db"},"timing_gate":{"kind":"tests","suite":"./internal/db","signature":"test"}}`),
		event(40, "tool_result", `{"call_id":"test","exit_code":1}`),
		event(40, "tool_call", `{"tool":"Bash","call_id":"lint","input":{"command":"go vet ./..."}}`),
		event(50, "tool_result", `{"call_id":"lint","exit_code":0}`),
		event(50, "tool_call", `{"tool":"Bash","call_id":"commit","input":{"command":"git commit -m fix"}}`),
		event(60, "tool_result", `{"call_id":"commit","exit_code":0}`),
		event(60, "tool_call", `{"tool":"Edit","call_id":"edit1"}`), event(70, "tool_result", `{"call_id":"edit1"}`),
		event(70, "mark", `{"kind":"review","verdict":"changes"}`),
		event(80, "tool_call", `{"tool":"Edit","call_id":"edit2"}`), event(90, "tool_result", `{"call_id":"edit2"}`),
		event(100, "tool_call", `{"tool":"wait_agent","call_id":"wait"}`), event(110, "tool_result", `{"call_id":"wait"}`),
	}
	timing := SplitOrderedWithHours(start, start.Add(120*time.Second), slices.Values(events), nil)
	raw, _ := json.Marshal(timing)
	var observed struct {
		Hours []struct {
			Detail map[string]float64 `json:"detail"`
		} `json:"hours"`
	}
	if err := json.Unmarshal(raw, &observed); err != nil {
		t.Fatal(err)
	}
	detail := map[string]float64{}
	for _, h := range observed.Hours {
		for k, n := range h.Detail {
			detail[k] += n
		}
	}
	if detail["gates.tests"]+detail["gates.build_lint"]+detail["gates.commit_hooks"]+detail["gates.unknown"] != timing.Phases["gates"] {
		t.Fatalf("Gates breakdown does not add up: %v parent %v", detail, timing.Phases["gates"])
	}
	if detail["building.model"] != 40 || detail["building.tools"] != 20 || detail["building.waiting"] != 10 || detail["building.first"] != 20 || detail["building.rework"] != 50 {
		t.Fatalf("Building breakdown: %v", detail)
	}
	var runs, passed, failed int
	var failedSeconds, runSeconds float64
	for _, r := range timing.GateRuns {
		runs += r.Runs
		passed += r.Passed
		failed += r.Failed
		failedSeconds += r.FailedSeconds
		runSeconds += r.Seconds
	}
	if runs != 3 || passed != 2 || failed != 1 || failedSeconds != 30 || runSeconds != timing.Phases["gates"] {
		t.Fatalf("run evidence: %d %d %d %v %v", runs, passed, failed, failedSeconds, runSeconds)
	}
}

func TestGateEvidenceAndRerunsUseRecordedSelectorsAndOutcomes(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	captured := CaptureGateEvidence("tool_call", `{"tool":"Bash","call_id":"a","input":{"command":"npm test --workspace runner/frontend -- --project unit src/components/Insights.test.tsx"}}`)
	var p struct {
		Gate GateEvidence `json:"timing_gate"`
	}
	if err := json.Unmarshal([]byte(captured), &p); err != nil {
		t.Fatal(err)
	}
	if p.Gate.Region != "runner/frontend" || p.Gate.Suite == "" || len(p.Gate.Signature) != 64 {
		t.Fatalf("capture: %+v", p.Gate)
	}
	second := CaptureGateEvidence("tool_call", `{"tool":"Bash","call_id":"b","input":{"command":"npm test --workspace runner/frontend -- --project unit src/components/Insights.test.tsx"}}`)
	events := []Event{{At: start, Kind: "tool_call", Payload: json.RawMessage(captured)}, {At: start.Add(10 * time.Second), Kind: "tool_result", Payload: json.RawMessage(`{"call_id":"a","exit_code":1}`)}, {At: start.Add(20 * time.Second), Kind: "tool_call", Payload: json.RawMessage(second)}, {At: start.Add(30 * time.Second), Kind: "tool_result", Payload: json.RawMessage(`{"call_id":"b","exit_code":0}`)}}
	timing := SplitOrderedWithHours(start, start.Add(40*time.Second), slices.Values(events), nil)
	if len(timing.GateRuns) != 1 {
		t.Fatalf("run groups: %+v", timing.GateRuns)
	}
	r := timing.GateRuns[0]
	if r.Runs != 2 || r.Repeated != 1 || r.Reruns != 1 || r.Failed != 1 || r.Passed != 1 || r.FailedSeconds != 10 || r.Seconds != 20 {
		t.Fatalf("reruns: %+v", r)
	}
}

func TestBreakdownEmptyUnknownAndSingleKind(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		events []Event
		key    string
		total  float64
	}{
		{name: "empty", events: nil, key: "gates.unknown", total: 0},
		{name: "all unknown", events: []Event{{At: start, Kind: "gate_interval", Payload: json.RawMessage(`{"started_at":"2026-11-01T00:00:00Z","ended_at":"2026-11-01T00:01:00Z"}`)}}, key: "gates.unknown", total: 60},
		{name: "only tests", events: []Event{{At: start, Kind: "tool_call", Payload: json.RawMessage(`{"tool":"Bash","call_id":"a","input":{"command":"go test ./..."}}`)}, {At: start.Add(time.Minute), Kind: "tool_result", Payload: json.RawMessage(`{"call_id":"a"}`)}}, key: "gates.tests", total: 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timing := SplitOrderedWithHours(start, start.Add(time.Minute), slices.Values(tc.events), nil)
			var sum float64
			for _, h := range timing.Hours {
				sum += h.Detail[tc.key]
			}
			if sum != tc.total || timing.Phases["gates"] != tc.total {
				t.Fatalf("partition %v parent %v", sum, timing.Phases["gates"])
			}
		})
	}
}

func TestGateBackgroundRunDoesNotPassAtLaunch(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, done := range []bool{false, true} {
		t.Run(map[bool]string{false: "still running", true: "failed"}[done], func(t *testing.T) {
			events := []Event{{At: start, Kind: "tool_call", Payload: json.RawMessage(CaptureGateEvidence("tool_call", `{"tool":"Bash","call_id":"a","input":{"command":"go test ./..."}}`))}, {At: start.Add(time.Second), Kind: "background_task_started", Payload: json.RawMessage(`{"task_id":"bg","call_id":"a","command":"go test ./..."}`)}, {At: start.Add(time.Second), Kind: "tool_result", Payload: json.RawMessage(`{"call_id":"a","is_error":false}`)}}
			if done {
				events = append(events, Event{At: start.Add(20 * time.Second), Kind: "background_task_done", Payload: json.RawMessage(`{"task_id":"bg","status":"failed"}`)})
			}
			timing := SplitOrderedWithHours(start, start.Add(30*time.Second), slices.Values(events), nil)
			if len(timing.GateRuns) != 1 {
				t.Fatalf("duplicate launch/run: %+v", timing.GateRuns)
			}
			run := timing.GateRuns[0]
			if run.Runs != 1 || run.Passed != 0 {
				t.Fatalf("launch marked passed: %+v", run)
			}
			if done && (run.Failed != 1 || run.FailedSeconds != 20) {
				t.Fatalf("failed background run: %+v", run)
			}
			if !done && run.UnknownOutcome != 1 {
				t.Fatalf("missing outcome invented: %+v", run)
			}
		})
	}
}
