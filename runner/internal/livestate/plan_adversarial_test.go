package livestate

import (
	"encoding/json"
	"testing"
	"time"
)

func nativePlanFixture() planEv {
	return planEv{"plan_updated", `{"items":[{"text":"Native root","status":"pending"}]}`}
}

func TestNativePlanSurvivesMalformedOrFailedClaudeCreate(t *testing.T) {
	for _, input := range []string{`{}`, `null`, `{"subject":""}`, `{"subject":"   "}`, `{"subject":"\u202e\u0000"}`, `{"subject":3}`} {
		evs := planEvents(nativePlanFixture(), planEv{"tool_call", `{"tool":"TaskCreate","call_id":"bad","input":` + input + `}`})
		if got := planTexts(PlanFrom(evs)); got != "Native root:pending" {
			t.Fatalf("invalid TaskCreate input %s erased native plan: %s", input, got)
		}
	}
	p := PlanFrom(planEvents(nativePlanFixture(), create("failed", "Should not replace", ""), planEv{"tool_result", `{"call_id":"failed","is_error":true,"output":"InputValidationError"}`}))
	if got := planTexts(p); got != "Native root:pending" {
		t.Fatalf("failed create erased native plan: %s", got)
	}
}

func TestNativePlanDoesNotInventClaudeTaskIDs(t *testing.T) {
	p := PlanFrom(planEvents(nativePlanFixture(), update("orphan", "1", "completed")))
	if got := planTexts(p); got != "Native root:pending" {
		t.Fatalf("unmatched TaskUpdate mutated native step: %s", got)
	}
	p = PlanFrom(planEvents(nativePlanFixture(), create("first", "First Claude task", ""), created("first", 7), create("second", "Second Claude task", ""), created("second", 8), update("finish", "7", "completed")))
	if got := planTexts(p); got != "First Claude task:completed,Second Claude task:pending" {
		t.Fatalf("first valid Claude create should reset once, then fold deltas: %s", got)
	}
}

func TestChildResultCannotPoisonRootTaskPlan(t *testing.T) {
	p := PlanFrom(planEvents(create("root", "Root task", ""), created("root", 1), planEv{"tool_result", `{"call_id":"root","parent_call_id":"child","output":"InputValidationError","is_error":true}`}, update("done", "1", "completed")))
	if got := planTexts(p); got != "Root task:completed" {
		t.Fatalf("child result poisoned root call identity: %s", got)
	}
}

func TestNativePlanClearDoesNotResurrectEarlierClaudeTasks(t *testing.T) {
	events := planEvents(create("old", "Old Claude task", ""), created("old", 1), nativePlanFixture(), planEv{"plan_updated", `{"items":[]}`}, planEv{"plan_updated", `{"items":null}`}, planEv{"plan_updated", `{"items":[{"text":"Child task","status":"pending"}],"parent_call_id":"child"}`}, update("old-done", "1", "completed"))
	if p := PlanFrom(events); p != nil {
		t.Fatalf("cleared plan resurrected from stale/malformed/child history: %s", planTexts(p))
	}
}

func TestNativePlanSequenceWinsOverTimestampAndInputOrder(t *testing.T) {
	later := Event{Seq: 20, Kind: "plan_updated", Payload: json.RawMessage(`{"items":[{"text":"Later sequence","status":"completed"}]}`), Ts: time.Unix(1, 0)}
	earlier := Event{Seq: 10, Kind: "plan_updated", Payload: json.RawMessage(`{"items":[{"text":"Later timestamp","status":"pending"}]}`), Ts: time.Unix(1000, 0)}
	if got := planTexts(PlanFrom([]Event{later, earlier})); got != "Later sequence:completed" {
		t.Fatalf("plan ignored server sequence: %s", got)
	}
}
