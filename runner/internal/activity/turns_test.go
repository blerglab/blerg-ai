package activity

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTurnsExcludeIdleAndUnionTools(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	ev := func(n int64, sec int, kind, p string) Event {
		return Event{Seq: n, At: at.Add(time.Duration(sec) * time.Second), Kind: kind, Payload: json.RawMessage(p)}
	}
	events := []Event{
		ev(1, 0, "status_changed", `{"status":"running"}`),
		ev(2, 2, "tool_call", `{"tool":"shell","call_id":"a","input":{"cmd":"go test ./..."}}`),
		ev(3, 3, "tool_call", `{"tool":"shell","call_id":"b","input":{"cmd":"go test ./..."}}`),
		ev(4, 7, "tool_result", `{"call_id":"a"}`), ev(5, 8, "tool_result", `{"call_id":"b"}`),
		ev(6, 10, "turn_done", `{"stop_reason":"interrupted","usage":{"input_tokens":12,"output_tokens":3}}`),
		ev(7, 100, "status_changed", `{"status":"running"}`),
		ev(8, 104, "turn_done", `{"stop_reason":"error","usage_reported":false}`),
	}
	turns := Turns(at, at.Add(200*time.Second), events, nil)
	if len(turns) != 2 {
		t.Fatalf("turns: %+v", turns)
	}
	a, b := turns[0], turns[1]
	if a.Seq != 6 || a.DurationSeconds != 10 || a.Timing.Seconds["tests"] != 6 || a.Timing.Seconds["model"] != 4 || a.ToolCounts["tests"] != 2 || a.Outcome != "stopped" || a.Tokens.Input != 12 {
		t.Fatalf("first: %+v", a)
	}
	if b.DurationSeconds != 4 || b.Outcome != "failed" || b.Tokens != nil {
		t.Fatalf("second: %+v", b)
	}
}

func TestTurnsOpenAndClippedWait(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	events := []Event{{Seq: 1, At: at, Kind: "status_changed", Payload: json.RawMessage(`{"status":"running"}`)}}
	turns := Turns(at, at.Add(10*time.Second), events, []Interval{{at.Add(-time.Second), at.Add(2 * time.Second)}})
	if len(turns) != 1 || turns[0].Outcome != "running" || turns[0].Timing.Seconds["waiting_on_person"] != 2 {
		t.Fatalf("turns: %+v", turns)
	}
}

func TestTurnEndWorkUsesLifecycleClock(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	events := []Event{
		{Seq: 1, At: at, Kind: "status_changed", Payload: json.RawMessage(`{"status":"running"}`)},
		{Seq: 2, At: at.Add(15 * time.Second), Kind: "turn_work", Payload: json.RawMessage(`{"started_at":"1970-01-01T00:00:10Z","ended_at":"1970-01-01T00:00:15Z","seconds":{"sync":5}}`)},
		{Seq: 3, At: at.Add(10 * time.Second), Kind: "turn_done", Payload: json.RawMessage(`{"stop_reason":"end_turn"}`)},
	}
	turns := Turns(at, at.Add(20*time.Second), events, nil)
	if len(turns) != 1 || turns[0].DurationSeconds != 15 || turns[0].Timing.Seconds["turn_end"] != 5 || turns[0].Timing.Seconds["model"] != 10 {
		t.Fatalf("turns: %+v", turns)
	}
}

func TestExplicitGateIntervalIsUnionedWithTools(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	events := []Event{
		{Seq: 1, At: at, Kind: "status_changed", Payload: json.RawMessage(`{"status":"running"}`)},
		{At: at, Kind: "gate_interval", Payload: json.RawMessage(`{"started_at":"1970-01-01T00:00:02Z","ended_at":"1970-01-01T00:00:08Z"}`)},
		{Seq: 2, At: at.Add(10 * time.Second), Kind: "turn_done", Payload: json.RawMessage(`{"stop_reason":"end_turn"}`)},
	}
	turns := Turns(at, at.Add(10*time.Second), events, nil)
	if turns[0].Timing.Phases["gates"] != 6 {
		t.Fatalf("turn: %+v", turns[0])
	}
}

func TestBackgroundWaitIsSeparateFromModel(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	events := []Event{
		{Seq: 1, At: at, Kind: "status_changed", Payload: json.RawMessage(`{"status":"running"}`)},
		{Seq: 2, At: at.Add(time.Second), Kind: "tool_call", Payload: json.RawMessage(`{"tool":"functions.wait","call_id":"wait"}`)},
		{Seq: 3, At: at.Add(8 * time.Second), Kind: "tool_result", Payload: json.RawMessage(`{"call_id":"wait"}`)},
		{Seq: 4, At: at.Add(10 * time.Second), Kind: "turn_done", Payload: json.RawMessage(`{"stop_reason":"end_turn"}`)},
	}
	turns := Turns(at, at.Add(10*time.Second), events, nil)
	if turns[0].BackgroundWaits != 1 || turns[0].Timing.Seconds["waiting"] != 7 || turns[0].Timing.Seconds["model"] != 3 {
		t.Fatalf("turn: %+v", turns[0])
	}
}

func TestIdleClosesTurnWithoutDone(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	events := []Event{{Seq: 1, At: at, Kind: "status_changed", Payload: json.RawMessage(`{"status":"running"}`)}, {Seq: 2, At: at.Add(4 * time.Second), Kind: "status_changed", Payload: json.RawMessage(`{"status":"idle"}`)}}
	turns := Turns(at, at.Add(time.Hour), events, nil)
	if len(turns) != 1 || turns[0].DurationSeconds != 4 || turns[0].Outcome == "running" {
		t.Fatalf("idle included in turn: %+v", turns)
	}
}

func TestProcessSignalReclassifiesGeneratedScriptTool(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	events := []Event{
		{Seq: 1, At: at, Kind: "status_changed", Payload: json.RawMessage(`{"status":"running"}`)},
		{Seq: 2, At: at, Kind: "tool_call", Payload: json.RawMessage(`{"tool":"bash","call_id":"generated","input":{"command":"bash generated.sh"}}`)},
		{Seq: 3, At: at.Add(10 * time.Second), Kind: "tool_result", Payload: json.RawMessage(`{"call_id":"generated"}`)},
		{Seq: 4, At: at.Add(10 * time.Second), Kind: "process_activity", Payload: json.RawMessage(`{"spans":[{"call_id":"generated","program":"go","class":"tests","started_at":"1970-01-01T00:00:01Z","ended_at":"1970-01-01T00:00:08Z","approximate":true,"basis":"proc_sample"}]}`)},
		{Seq: 5, At: at.Add(10 * time.Second), Kind: "turn_done", Payload: json.RawMessage(`{"stop_reason":"end_turn"}`)},
	}
	turns := Turns(at, at.Add(10*time.Second), events, nil)
	if turns[0].Timing.Phases["gates"] != 7 || turns[0].ToolCounts["tests"] != 1 || !turns[0].Timing.Approximate {
		t.Fatalf("process evidence ignored: %+v", turns[0])
	}
}

func TestTurnEndWorkIsUnknownUntilMeasured(t *testing.T) {
	at := time.Unix(0, 0)
	turns := Turns(at, at.Add(time.Second), []Event{{Seq: 1, At: at, Kind: "status_changed", Payload: json.RawMessage(`{"status":"running"}`)}, {Seq: 2, At: at.Add(time.Second), Kind: "turn_done", Payload: json.RawMessage(`{}`)}}, nil)
	if len(turns) != 1 || turns[0].TurnEndBasis != "unknown" {
		t.Fatalf("unknown work: %+v", turns)
	}
}
