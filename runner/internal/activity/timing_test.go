package activity

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

func TestTimingBucketsCrossMidnight(t *testing.T) {
	start := time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC)
	events := []Event{{At: start, Kind: "status_changed", Payload: json.RawMessage(`{"status":"running"}`)}}
	result := SplitOrderedWithHours(start, start.Add(time.Hour), slices.Values(events), nil)
	if got := SplitOrdered(start, start.Add(time.Hour), slices.Values(events), nil); len(got.Hours) > 0 {
		t.Fatal("totals-only consumer built trend buckets")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Hours []struct {
			At     time.Time
			Phases map[string]float64
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Hours) != 2 {
		t.Fatalf("want two hourly buckets, got %s", raw)
	}
	for _, h := range got.Hours {
		if h.Phases["building"] != 1800 {
			t.Fatalf("bucket: %+v", h)
		}
	}
	long := SplitOrderedWithHours(start, start.Add(200*24*time.Hour), slices.Values(events), nil)
	var dated float64
	for _, h := range long.Hours {
		dated += h.Phases["building"]
	}
	if len(long.Hours) > 4321 || dated != 180*24*3600 || long.Phases["building"] != 200*24*3600 {
		t.Fatal("hour retention changed lifetime totals or exceeded 180 days")
	}
}

func TestClassification(t *testing.T) {
	for _, v := range []struct{ tool, input, want string }{
		{"Bash", `{"command":"go test -race ./..."}`, "tests"},
		{"exec_command", `{"cmd":"git commit -m fix"}`, "git"},
		{"Bash", `{"command":"pre-commit run --all-files"}`, "build_lint"},
		{"Bash", `{"command":"npm run lint"}`, "build_lint"},
		{"Read", `{"file_path":"test.go"}`, "read"},
		{"apply_patch", `{}`, "edit"},
		{"Bash", `{"command":"echo 'go test'"}`, "other"},
		{"Bash", `{"command":"echo 'text; go test'"}`, "other"},
		{"exec_command", `{"cmd":"GOWORK=off $HOME/go/bin/go -C runner test -race ./internal/db"}`, "tests"},
		{"exec_command", `{"cmd":"flock /tmp/cx/ui-suite.lock npm test -- --project unit"}`, "tests"},
	} {
		if got := Classify(v.tool, json.RawMessage(v.input)); got != v.want {
			t.Errorf("%s %s: %s want %s", v.tool, v.input, got, v.want)
		}
	}
}
func TestTimingUnionClipsLongToolsAndCountsCommitHooksAsGates(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ev := func(n int, kind, p string) Event {
		return Event{At: start.Add(time.Duration(n) * time.Second), Kind: kind, Payload: json.RawMessage(p)}
	}
	events := []Event{
		ev(0, "status_changed", `{"status":"running"}`),
		ev(1, "tool_call", `{"tool":"Bash","call_id":"a","input":{"command":"go test ./..."}}`),
		ev(2, "tool_call", `{"tool":"Bash","call_id":"b","input":{"command":"npm run lint"}}`),
		ev(6, "tool_result", `{"call_id":"a","duration_ms":5000}`),
		ev(8, "tool_result", `{"call_id":"b","duration_ms":6000}`),
		ev(8, "tool_call", `{"tool":"Bash","call_id":"c","input":{"command":"git commit -m fix"}}`),
		ev(12, "tool_result", `{"call_id":"c","duration_ms":4000}`),
		ev(13, "status_changed", `{"status":"idle"}`),
	}
	got := Split(start, start.Add(15*time.Second), events, nil)
	if got.Seconds["tests"] != 5 || got.Seconds["build_lint"] != 2 || got.Seconds["git"] != 4 || got.Seconds["model"] != 2 || got.Seconds["idle"] != 2 || got.Phases["gates"] != 11 {
		t.Fatalf("%+v", got)
	}
	var sum float64
	for _, v := range got.Seconds {
		sum += v
	}
	if sum != 15 {
		t.Fatalf("overlap counted twice: %v", sum)
	}
}

func TestTimingIncompleteUnknownAndFixing(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	event := func(n int, kind, p string) Event {
		return Event{At: start.Add(time.Duration(n) * time.Second), Kind: kind, Payload: json.RawMessage(p)}
	}
	events := []Event{
		event(2, "status_changed", `{"status":"running"}`),
		event(3, "mark", `{"kind":"failure","rerun_passed":false}`),
		event(4, "tool_call", `{"call_id":"edit","tool":"functions.apply_patch","input":{}}`),
		event(6, "tool_result", `{"call_id":"edit"}`),
		event(7, "mark", `{"kind":"failure","rerun_passed":true}`),
		event(8, "tool_call", `{"call_id":"commit","tool":"exec_command","input":{"cmd":"git -C repo commit -m fix"}}`),
	}
	got := Split(start, start.Add(10*time.Second), events, []Interval{{start.Add(5 * time.Second), start.Add(7 * time.Second)}})
	if got.Seconds["unknown"] != 2 || got.Phases["fixing"] != 2 || got.Phases["waiting_on_person"] != 2 || got.Phases["gates"] != 2 || got.IncompleteTools != 1 {
		t.Fatalf("%+v", got)
	}
	var total float64
	for _, v := range got.Seconds {
		total += v
	}
	if total != 10 {
		t.Fatal(total)
	}
}

func TestReviewChangesStartFixingUntilApproved(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	event := func(n int, kind, p string) Event {
		return Event{At: start.Add(time.Duration(n) * time.Second), Kind: kind, Payload: json.RawMessage(p)}
	}
	events := []Event{event(0, "status_changed", `{"status":"running"}`), event(2, "mark", `{"kind":"review","verdict":"changes"}`), event(3, "tool_call", `{"call_id":"edit","tool":"Edit"}`), event(4, "tool_result", `{"call_id":"edit"}`), event(5, "mark", `{"kind":"review","verdict":"approved"}`), event(6, "status_changed", `{"status":"idle"}`)}
	got := Split(start, start.Add(7*time.Second), events, nil)
	if got.Phases["fixing"] != 3 || got.Phases["building"] != 3 || got.Phases["idle"] != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestRecordedShellWrappersAndGateCommands(t *testing.T) {
	for _, cmd := range []string{
		`python3 scripts/pre_merge.py`, `scripts/gate_record.py --suite unit -- go test ./...`,
		`make -C runner test`, `timeout -k 5 300 go test ./...`, `flock -x l npm test`,
		`env -u X go test ./...`, `time go test ./...`, `nice go test ./...`, `npm --prefix x test`,
		`/bin/bash -lc "cd /workspace/project && GOWORK=off go test -race ./internal/server"`,
		`/bin/bash -lc 'timeout -k 5 300 env -u X nice go test ./...'`,
	} {
		input, _ := json.Marshal(map[string]string{"command": cmd})
		got := Classify("Bash", input)
		if got != "tests" && got != "build_lint" {
			t.Errorf("command %q classified %s", cmd, got)
		}
	}
	input, _ := json.Marshal(map[string]string{"command": "/bin/bash -lc 'cat <<EOF\ngo test ./...\nEOF\n'"})
	if got := Classify("Bash", input); got == "tests" || got == "build_lint" {
		t.Fatal("heredoc text counted as a gate")
	}
	for tool, want := range map[string]string{"patch": "edit", "NotebookEdit": "edit", "search_files": "read"} {
		if got := Classify(tool, nil); got != want {
			t.Errorf("%s: %s", tool, got)
		}
	}
	input, _ = json.Marshal(map[string]string{"cmd": "git commit --no-verify -m fix"})
	if commandPhase(input) == "gates" {
		t.Fatal("no-verify commit counted as hooks")
	}
}

func TestRecordedCodexToolEvents(t *testing.T) {
	path := os.Getenv("BLERG_RECORDED_TOOL_EVENTS")
	if path == "" {
		t.Skip("private recorded events supplied by reviewer")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, row := range rows {
		var p struct {
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(row.Payload, &p) != nil {
			continue
		}
		if command(p.Input) == "" {
			continue
		}
		counts[Classify("shell", p.Input)]++
	}
	t.Logf("Recorded classifications: %v", counts)
	if counts["tests"] == 0 || counts["build_lint"] == 0 {
		t.Fatal("recorded Codex gates were not recognized")
	}
}

func TestRecordedReviewWorkerPhase(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []Event{{At: start, Kind: "review_started", Payload: json.RawMessage(`{}`)}, {At: start, Kind: "status_changed", Payload: json.RawMessage(`{"status":"running"}`)}, {At: start.Add(10 * time.Second), Kind: "mark", Payload: json.RawMessage(`{"kind":"review","verdict":"approved"}`)}}
	got := Split(start, start.Add(12*time.Second), events, nil)
	if got.Phases["reviewing"] != 10 || got.Phases["building"] != 2 {
		t.Fatalf("recorded child review: %+v", got)
	}
}

func TestBackgroundGatesOutliveInitialToolResult(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	ev := func(sec int, kind, p string) Event {
		return Event{At: at.Add(time.Duration(sec) * time.Second), Kind: kind, Payload: json.RawMessage(p)}
	}
	events := []Event{
		ev(0, "status_changed", `{"status":"running"}`),
		ev(2, "tool_call", `{"tool":"bash","call_id":"a","input":{"command":"go test ./..."}}`),
		ev(3, "background_task_started", `{"task_id":"bg","call_id":"a","command":"go test $(truncated", "started_at":"1970-01-01T00:00:02Z"}`),
		ev(3, "tool_result", `{"call_id":"a"}`),
		ev(8, "background_task_done", `{"task_id":"bg","status":"completed"}`),
	}
	got := Split(at, at.Add(10*time.Second), events, nil)
	if got.Seconds["tests"] != 6 || got.Seconds["model"] != 4 {
		t.Fatalf("timing %+v", got)
	}
}
