package livestate

import (
	"encoding/json"
	"testing"
	"time"
)

func TestProgressSeparatesRootAndChildAndHonoursIncarnations(t *testing.T) {
	events := []Event{
		{Seq: 1, Kind: "plan_updated", Payload: json.RawMessage(`{"items":[{"text":"Root done","status":"completed"},{"text":"Root next","status":"in_progress"}]}`), Ts: time.Unix(1, 0)},
		{Seq: 2, Kind: "plan_updated", Payload: json.RawMessage(`{"items":[{"text":"Child","status":"completed"}],"child_id":"child"}`), Ts: time.Unix(2, 0)},
	}
	p := ProgressFor(events, "", 0)
	if p == nil || p.Completed != 1 || p.Total != 2 || p.Label != "Root next" {
		t.Fatal("child overwrote root progress")
	}
	p = ProgressFor(events, "child", 1)
	if p == nil || p.Completed != 1 || p.Total != 1 || p.Label != "Child" {
		t.Fatal("child plan not attributed")
	}
	if p = ProgressFor(events, "child", 3); p != nil {
		t.Fatal("prior incarnation progress survived new start")
	}
	events = append(events, Event{Seq: 4, Kind: "tool_call", Payload: json.RawMessage(`{"tool":"TodoWrite","parent_call_id":"child","input":{"todos":[{"content":"Claude child","status":"in_progress"}]}}`), Ts: time.Unix(4, 0)})
	if p = ProgressFor(events, "child", 3); p == nil || p.Label != "Claude child" {
		t.Fatal("Claude child plan was dropped")
	}
}

func TestExplicitProgressWinsUntilNewPlanAndEmptySnapshotClears(t *testing.T) {
	events := []Event{{Seq: 1, Kind: "progress_updated", Payload: json.RawMessage(`{"completed":3,"total":7,"label":"Script"}`), Ts: time.Unix(1, 0)}}
	if p := ProgressFor(events, "", 0); p == nil || p.Completed != 3 || p.Total != 7 {
		t.Fatal("explicit progress missing")
	}
	events = append(events, Event{Seq: 2, Kind: "plan_updated", Payload: json.RawMessage(`{"items":[{"text":"New plan","status":"completed"}]}`), Ts: time.Unix(2, 0)})
	if p := ProgressFor(events, "", 0); p == nil || p.Label != "New plan" || p.Completed != 1 {
		t.Fatal("newer plan did not replace explicit progress")
	}
	events = append(events, Event{Seq: 3, Kind: "plan_updated", Payload: json.RawMessage(`{"items":[]}`), Ts: time.Unix(3, 0)})
	if p := ProgressFor(events, "", 0); p != nil {
		t.Fatal("clear resurrected older explicit progress")
	}
}
