package activity

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestGateFailurePhaseLifecycle(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	event := func(at int, kind, payload string) Event {
		return Event{At: start.Add(time.Duration(at) * time.Second), Kind: kind, Payload: json.RawMessage(CaptureGateEvidence(kind, payload))}
	}
	run := func(prefix []Event, command string) Timing {
		events := append(slices.Clone(prefix), event(10, "tool_call", `{"tool":"Bash","call_id":"red","input":{"command":"go test ./internal/db"}}`), event(20, "tool_result", `{"call_id":"red","is_error":true}`), event(21, "mark", `{"kind":"failure","type":"test","suite":"db","severity":"high"}`), event(25, "status_changed", `{"status":"running"}`), event(30, "tool_call", `{"tool":"Bash","call_id":"green","input":{"command":"`+command+`"}}`), event(40, "tool_result", `{"call_id":"green","is_error":false}`))
		return SplitOrderedWithHours(start, start.Add(50*time.Second), slices.Values(events), nil)
	}
	initial := []Event{event(0, "status_changed", `{"status":"running"}`)}
	implemented := append(slices.Clone(initial), event(0, "tool_call", `{"tool":"Edit","call_id":"edit","input":{"file_path":"widget.go","old_string":"a","new_string":"b"}}`), event(5, "tool_result", `{"call_id":"edit","is_error":false}`))
	testWritten := append(slices.Clone(initial), event(0, "tool_call", `{"tool":"Write","call_id":"edit","input":{"file_path":"widget_test.go"}}`), event(5, "tool_result", `{"call_id":"edit","is_error":false}`))
	for _, tc := range []struct {
		name           string
		prefix         []Event
		command        string
		fixing, rework float64
	}{
		{"passing same command clears failure", implemented, "go test ./internal/db", 10, 10},
		{"different command leaves failure unresolved", implemented, "go test ./internal/server", 20, 20},
		{"test-first red is first build", initial, "go test ./internal/db", 0, 0},
		{"writing test before red is first build", testWritten, "go test ./internal/db", 0, 0},
		{"broader passing run clears failure", implemented, "go test ./...", 10, 10},
		{"review phase survives failed gate and running status", append(slices.Clone(implemented), event(5, "review_started", `{}`), event(5, "status_changed", `{"status":"running"}`)), "go test ./internal/db", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(tc.prefix, tc.command)
			var rework float64
			for _, h := range got.Hours {
				rework += h.Detail["building.rework"]
			}
			if got.Phases["fixing"] != tc.fixing || rework != tc.rework {
				t.Fatalf("phase/stage drift: phases=%v rework=%v", got.Phases, rework)
			}
			if tc.name == "review phase survives failed gate and running status" && got.Phases["reviewing"] != 25 {
				t.Fatalf("review time lost: %v", got.Phases)
			}
		})
	}
}

func TestUnknownSuiteIntervalDoesNotOverrideRecordedTool(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	events := []Event{{At: start, Kind: "gate_interval", Payload: json.RawMessage(`{"started_at":"2026-11-01T00:00:00Z","ended_at":"2026-11-01T00:01:00Z","timing_gate":{"suite":"old run"}}`)}, {At: start, Kind: "tool_call", Payload: json.RawMessage(CaptureGateEvidence("tool_call", `{"tool":"Bash","call_id":"a","input":{"command":"go test ./internal/db"}}`))}, {At: start.Add(time.Minute), Kind: "tool_result", Payload: json.RawMessage(`{"call_id":"a","is_error":false}`)}}
	got := SplitOrderedWithHours(start, start.Add(time.Minute), slices.Values(events), nil)
	if got.Hours[0].Detail["gates.tests"] != 60 || got.Hours[0].Detail["gates.unknown"] != 0 {
		t.Fatalf("recorded test overwritten: %+v", got.Hours)
	}
}

func TestPassingBackgroundGateClearsOnlyAtCompletion(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	event := func(at int, kind, payload string) Event {
		return Event{At: start.Add(time.Duration(at) * time.Second), Kind: kind, Payload: json.RawMessage(CaptureGateEvidence(kind, payload))}
	}
	events := []Event{event(0, "status_changed", `{"status":"running"}`), event(0, "tool_call", `{"tool":"Edit","call_id":"edit","input":{"file_path":"widget.go"}}`), event(5, "tool_result", `{"call_id":"edit","is_error":false}`), event(10, "tool_call", `{"tool":"Bash","call_id":"red","input":{"command":"go test ./..."}}`), event(20, "tool_result", `{"call_id":"red","exit_code":1}`), event(30, "tool_call", `{"tool":"Bash","call_id":"green","input":{"command":"go test ./..."}}`), event(31, "background_task_started", `{"task_id":"green-bg","call_id":"green","command":"go test ./..."}`), event(31, "tool_result", `{"call_id":"green","exit_code":0}`), event(40, "background_task_done", `{"task_id":"green-bg","status":"completed"}`)}
	got := SplitOrderedWithHours(start, start.Add(50*time.Second), slices.Values(events), nil)
	if got.Phases["fixing"] != 10 || got.Hours[0].Detail["building.rework"] != 10 {
		t.Fatalf("background rerun drift: %+v", got)
	}
	// Without completion, the original failure is still pending even though
	// launching the background command succeeded.
	stillRunning := SplitOrderedWithHours(start, start.Add(50*time.Second), slices.Values(events[:len(events)-1]), nil)
	var passed int
	for _, run := range stillRunning.GateRuns {
		passed += run.Passed
	}
	if passed != 0 {
		t.Fatal("background launch counted as a passed gate")
	}
}

func TestGateIdentityIgnoresShellFlagsAndCommitMessages(t *testing.T) {
	for _, tc := range []struct {
		red, green string
		clears     bool
	}{
		{"make test", "make test", true},
		{"cd runner && make lint", "cd runner && make lint", true},
		{"make test", "make lint", false},
		{"make lint && make test", "make lint", false},
		{"cd runner && make lint && make test", "cd runner && make lint && make test", true},
		{"npx vitest run -t TestX", "npx vitest run", true},
		{"npx vitest run", "npx vitest run -t TestX", false},
		{"node scripts/test.mjs", "node scripts/test.mjs", true},
		{"node node_modules/vitest/vitest.mjs run", "node node_modules/vitest/vitest.mjs run -t TestX", false},
		{"scripts/pre-merge.sh", "scripts/pre-merge.sh", true},
		{"go test ./internal/db", "go test ./internal/db -run TestX", false},
		{"go test ./internal/db -run TestX", "go test ./internal/db -run TestY", false},
		{"go test ./internal/db -run TestX 2>&1 | tail -30", "go test ./...", true},
		{"cd runner && go test ./internal/db -run TestX", "cd runner && go test ./...", true},
		{"cd runner && go test ./internal/db", "cd board && go test ./...", false},
		{"git commit -m 'First message'", "git commit -m 'New message'", true},
		{"git commit -m 'fix; go test ./internal/db'", "git commit -m 'New message'", true},
		{"npm run test:browser", "npm run test:unit", false},
		{"npm test", "go test ./...", false},
		{"go test net/http -run TestX", "go test net/url", false},
		{"bash scripts/gate.sh --verbose", "bash scripts/gate.sh", true},
		{"bash scripts/tests.sh", "bash scripts/build.sh", false},
	} {
		t.Run(tc.red, func(t *testing.T) {
			a, b := gateIdentity(json.RawMessage(`{"command":`+mustJSON(tc.red)+`}`)), gateIdentity(json.RawMessage(`{"command":`+mustJSON(tc.green)+`}`))
			if got := gateCovers(b, a); got != tc.clears {
				t.Fatalf("coverage %v -> %v = %v", a, b, got)
			}
		})
	}
}

func TestGateIdentityAlwaysHasSignature(t *testing.T) {
	for _, cmd := range []string{"make test", "cd runner && make lint", "npx vitest run", "scripts/pre-merge.sh", "custom-check --quiet ./x"} {
		input := json.RawMessage(`{"command":` + mustJSON(cmd) + `}`)
		if commandSignature(input) == "" {
			t.Errorf("missing signature for %s", cmd)
		}
	}
}

func TestRepositoryGateLifecycle(t *testing.T) {
	for _, cmd := range []string{"scripts/pre-merge.sh", "make test", "cd runner && make lint", "npx vitest run", "node node_modules/vitest/vitest.mjs run", "go test ./internal/db -run TestX"} {
		t.Run(cmd, func(t *testing.T) {
			start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			e := func(at int, kind, payload string) Event {
				return Event{At: start.Add(time.Duration(at) * time.Second), Kind: kind, Payload: json.RawMessage(CaptureGateEvidence(kind, payload))}
			}
			call := func(id string) string {
				return `{"tool":"Bash","call_id":` + mustJSON(id) + `,"input":{"command":` + mustJSON(cmd) + `}}`
			}
			events := []Event{e(0, "status_changed", `{"status":"running"}`), e(0, "tool_call", `{"tool":"Edit","call_id":"edit","input":{"file_path":"widget.go"}}`), e(1, "tool_result", `{"call_id":"edit","exit_code":0}`), e(2, "tool_call", call("red")), e(3, "tool_result", `{"call_id":"red","exit_code":1}`), e(5, "tool_call", call("green")), e(6, "tool_result", `{"call_id":"green","exit_code":0}`)}
			got := SplitOrderedWithHours(start, start.Add(8*time.Second), slices.Values(events), nil)
			if got.Phases["fixing"] != 2 {
				t.Fatalf("failure lifecycle: %v", got.Phases)
			}
			var repeated, reruns int
			for _, run := range got.GateRuns {
				repeated += run.Repeated
				reruns += run.Reruns
			}
			if repeated != 1 || reruns != 1 {
				t.Fatalf("repeat=%d reruns=%d: %+v", repeated, reruns, got.GateRuns)
			}
		})
	}
}

func TestUnknownGateIsNotOverriddenByGenericTool(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, command := range []string{"bash scripts/gate.sh", "cat README.md"} {
		events := []Event{{At: start, Kind: "gate_interval", Payload: json.RawMessage(`{"started_at":"2026-11-01T00:00:00Z","ended_at":"2026-11-01T00:01:00Z","timing_gate":{"suite":"old run"}}`)}, {At: start, Kind: "tool_call", Payload: json.RawMessage(`{"tool":"Bash","call_id":"a","input":{"command":` + mustJSON(command) + `}}`)}, {At: start.Add(time.Minute), Kind: "tool_result", Payload: json.RawMessage(`{"call_id":"a","is_error":false}`)}}
		got := SplitOrderedWithHours(start, start.Add(time.Minute), slices.Values(events), nil)
		if got.Hours[0].Detail["gates.unknown"] != 60 {
			t.Fatalf("%s overwrote gate: %+v", command, got.Hours)
		}
	}
}
