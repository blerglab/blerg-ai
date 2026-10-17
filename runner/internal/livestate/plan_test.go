package livestate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

var planT0 = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

type planEv struct {
	kind    string
	payload string
}

func planEvents(evs ...planEv) []Event {
	out := make([]Event, len(evs))
	for i, e := range evs {
		out[i] = Event{Seq: int64(i + 1), ID: fmt.Sprintf("e%d", i+1), Kind: e.kind, Payload: json.RawMessage(e.payload), Ts: planT0.Add(time.Duration(i) * time.Second)}
	}
	return out
}

func create(call, subject, active string) planEv {
	return planEv{"tool_call", fmt.Sprintf(`{"tool":"TaskCreate","call_id":%q,"input":{"subject":%q,"description":"d","activeForm":%q}}`, call, subject, active)}
}

func created(call string, n int) planEv {
	return planEv{"tool_result", fmt.Sprintf(`{"call_id":%q,"output":"Task #%d created successfully: x"}`, call, n)}
}

func update(call, id, status string) planEv {
	return planEv{"tool_call", fmt.Sprintf(`{"tool":"TaskUpdate","call_id":%q,"input":{"taskId":%q,"status":%q}}`, call, id, status)}
}

func planTexts(p *Plan) string {
	if p == nil {
		return "<nil>"
	}
	var parts []string
	for _, it := range p.Items {
		parts = append(parts, it.Text+":"+it.Status)
	}
	return strings.Join(parts, ",")
}

// Claude Code's task tools are deltas: each TaskCreate adds one (its id is in its result), each
// TaskUpdate changes one; the plan folds them all.
func TestPlanFoldsTaskCreateAndUpdate(t *testing.T) {
	evs := planEvents(
		create("c1", "Read the spec", "Reading the spec"), created("c1", 1),
		create("c2", "Write tests", "Writing tests"), created("c2", 2),
		create("c3", "Ship", "Shipping"), created("c3", 3),
		update("u1", "1", "completed"),
		update("u2", "2", "in_progress"),
		update("u3", "3", "deleted"),
	)
	p := PlanFrom(evs)
	if got := planTexts(p); got != "Read the spec:completed,Write tests:in_progress" {
		t.Fatalf("plan = %s", got)
	}
	if p.Items[1].Active != "Writing tests" || !p.UpdatedAt.Equal(evs[len(evs)-1].Ts) {
		t.Fatalf("%+v", p)
	}
}

// A create whose result is not held yet takes the next id; one that failed adds nothing.
func TestPlanCreateWithoutOrFailedResult(t *testing.T) {
	p := PlanFrom(planEvents(
		create("c1", "One", ""), created("c1", 4),
		create("c2", "Bad", ""), planEv{"tool_result", `{"call_id":"c2","output":"InputValidationError","is_error":true}`},
		create("c3", "Two", ""),
		update("u1", "5", "in_progress"),
	))
	if got := planTexts(p); got != "One:pending,Two:in_progress" {
		t.Fatalf("plan = %s", got)
	}
}

// TodoWrite carries the whole list: the latest call wins.
func TestPlanTodoWriteLatestWins(t *testing.T) {
	p := PlanFrom(planEvents(
		planEv{"tool_call", `{"tool":"TodoWrite","call_id":"t1","input":{"todos":[{"content":"A","status":"pending","activeForm":"Doing A"}]}}`},
		planEv{"tool_call", `{"tool":"TodoWrite","call_id":"t2","input":{"todos":[{"content":"A","status":"completed","activeForm":"Doing A"},{"content":"B","status":"in_progress","activeForm":"Doing B"},{"content":"C","status":"weird"}]}}`},
	))
	if got := planTexts(p); got != "A:completed,B:in_progress,C:pending" {
		t.Fatalf("plan = %s", got)
	}
}

// A subagent's plan is its own: only the main agent's calls count.
func TestPlanIgnoresSubagentsAndOtherTools(t *testing.T) {
	p := PlanFrom(planEvents(
		planEv{"tool_call", `{"tool":"TaskCreate","call_id":"s1","parent_call_id":"agent1","input":{"subject":"Sub"}}`},
		planEv{"tool_call", `{"tool":"Bash","call_id":"b1","input":{"command":"ls"}}`},
	))
	if p != nil {
		t.Fatalf("plan = %s", planTexts(p))
	}
	if PlanFrom(planEvents(create("c1", "x", ""), created("c1", 1), update("u", "1", "deleted"))) != nil {
		t.Fatal("an emptied plan is no plan")
	}
}

func TestPlanCapsAndCleans(t *testing.T) {
	var evs []planEv
	for i := 0; i < MaxPlanItems+5; i++ {
		c := fmt.Sprintf("c%d", i)
		evs = append(evs, create(c, "x\u202e"+strings.Repeat("y", 300), ""), created(c, i+1))
	}
	p := PlanFrom(planEvents(evs...))
	if len(p.Items) != MaxPlanItems || strings.ContainsRune(p.Items[0].Text, 0x202e) || len([]rune(p.Items[0].Text)) != MaxPlanText {
		t.Fatalf("%d items, first %q", len(p.Items), p.Items[0].Text)
	}
}

func TestPlanUpdatedSnapshotOrderClearAndInvalidPreserves(t *testing.T) {
	events := planEvents(create("old", "Old", ""), created("old", 1), planEv{"plan_updated", `{"items":[{"text":"Second","status":"in_progress"},{"text":"First","status":"pending"}]}`}, planEv{"plan_updated", `{"items":null}`}, planEv{"plan_updated", `{"items":[{"text":"child","status":"pending"}],"child_id":"child"}`})
	p := PlanFrom(events)
	if got := planTexts(p); got != "Second:in_progress,First:pending" || !p.UpdatedAt.Equal(events[2].Ts) {
		t.Fatalf("snapshot/invalid: %s %+v", got, p)
	}
	events = append(events, Event{Seq: 6, Kind: "plan_updated", Payload: json.RawMessage(`{"items":[]}`), Ts: planT0.Add(6 * time.Second)})
	if p := PlanFrom(events); p != nil {
		t.Fatalf("clear retained plan %+v", p)
	}
}

func TestPlanUpdatedDoesNotCollideWithClaudeTaskIDs(t *testing.T) {
	p := PlanFrom(planEvents(planEv{"plan_updated", `{"items":[{"text":"Native","status":"pending"}]}`}, update("unknown", "1", "completed"), create("c1", "Claude", ""), created("c1", 1), update("known", "1", "in_progress")))
	if got := planTexts(p); got != "Claude:in_progress" {
		t.Fatalf("source ID collision: %s", got)
	}
	p = PlanFrom(planEvents(create("c1", "Claude", ""), created("c1", 1), planEv{"plan_updated", `{"items":[{"text":"Native","status":"pending"}]}`}, planEv{"tool_call", `{"tool":"TodoWrite","input":{"todos":[{"content":"Newest","status":"completed"}]}}`}))
	if got := planTexts(p); got != "Newest:completed" {
		t.Fatalf("latest source snapshot: %s", got)
	}
}

func TestPlanIgnoresChildTaskResults(t *testing.T) {
	p := PlanFrom(planEvents(create("c1", "Root", ""), planEv{"tool_result", `{"call_id":"c1","parent_call_id":"child","output":"Task #9 created successfully: child"}`}, update("u", "1", "completed")))
	if got := planTexts(p); got != "Root:completed" {
		t.Fatalf("child result corrupted root task: %s", got)
	}
}
