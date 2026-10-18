package livestate

// The engine's own plan, shown when the agent has set no checklist. A plan_updated snapshot
// replaces the complete main-agent plan. Historical Claude task tools remain readable as
// deltas; the first valid TaskCreate after a native snapshot switches to a fresh Claude plan.
// Unmatched Claude updates cannot mutate anonymous native items. Child plans are left out.
// Claude Code emits its plan in one of two shapes:
//
//   - TaskCreate {subject, description, activeForm} adds one task; its result says its id ("Task #3
//     created successfully: …"). TaskUpdate {taskId, status?, subject?, activeForm?} changes one;
//     status is pending, in_progress, completed or deleted (it goes). Each call is a delta: the plan
//     folds them all.
//   - TodoWrite {todos: [{content, status, activeForm}]} (older builds) carries the whole list: the
//     latest call wins.
//
// The server reads these events through their own index (PlanToolsSQL), not with the live kinds.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
)

const (
	// MaxPlanItems is how many items the plan shows; MaxPlanText an item's length in runes.
	MaxPlanItems = protocol.MaxPlanItems
	MaxPlanText  = protocol.MaxPlanText
)

// PlanTools are the tools whose calls the plan reads.
var PlanTools = []string{"TodoWrite", "TaskCreate", "TaskUpdate"}

// PlanItem is one task: its text, its status (pending, in_progress or completed) and, while it is
// in progress, how the engine words it ("Writing tests").
type PlanItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
	Active string `json:"active,omitempty"`
}

// Plan is the engine's plan now, in its order, and when it last changed.
type Plan struct {
	Items     []PlanItem `json:"items"`
	UpdatedAt time.Time  `json:"updated_at"`
}

var taskCreatedRe = regexp.MustCompile(`^Task #(\d+) created`)

type planTask struct {
	id   int
	item PlanItem
}

func planStatus(s string) string {
	switch s {
	case "in_progress", "completed":
		return s
	}
	return "pending"
}

// PlanFrom folds the plan from events (any order; other events are ignored). nil when there is none.
func PlanFrom(events []Event) *Plan {
	p, _ := foldPlan(scopeEvents(events, "", 0))
	return p
}

func foldPlan(events []Event) (*Plan, int64) {
	events = sorted(events)
	// Native snapshots supersede the complete prior plan. Retain only the latest valid
	// main-agent snapshot; invalid/child events never erase earlier state.
	latest := -1
	for i, event := range events {
		if event.Kind == protocol.PlanUpdatedKind {
			if _, ok := protocol.DecodePlanUpdated(event.Payload); ok {
				latest = i
			}
		}
	}
	if latest >= 0 {
		events = events[latest:]
	}
	type call struct {
		Tool   string          `json:"tool"`
		CallID string          `json:"call_id"`
		Input  json.RawMessage `json:"input"`
		Parent string          `json:"parent_call_id"`
		Child  string          `json:"child_id"`
	}
	// The id each TaskCreate got, from its result; -1 when it failed.
	ids := map[string]int{}
	for _, ev := range events {
		if ev.Kind != "tool_result" {
			continue
		}
		var r struct {
			CallID  string `json:"call_id"`
			Output  string `json:"output"`
			IsError bool   `json:"is_error"`
			Parent  string `json:"parent_call_id"`
			Child   string `json:"child_id"`
		}
		if json.Unmarshal(ev.Payload, &r) != nil || r.CallID == "" || r.Parent != "" || r.Child != "" {
			continue
		}
		if m := taskCreatedRe.FindStringSubmatch(r.Output); m != nil && !r.IsError {
			if n, err := strconv.Atoi(m[1]); err == nil {
				ids[r.CallID] = n
			}
		} else {
			ids[r.CallID] = -1
		}
	}
	var tasks []*planTask
	byID := map[int]*planTask{}
	next := 1
	var updated time.Time
	var updatedSeq int64
	native := false
	text := func(s string) string { return topics.CleanText(s, MaxPlanText) }
	for _, ev := range events {
		if ev.Kind == protocol.PlanUpdatedKind {
			p, ok := protocol.DecodePlanUpdated(ev.Payload)
			if !ok {
				continue
			}
			tasks, byID, next = nil, map[int]*planTask{}, 1
			for i, item := range p.Items {
				tasks = append(tasks, &planTask{id: i + 1, item: PlanItem{Text: item.Text, Status: item.Status, Active: item.Active}})
			}
			native = true
			updated, updatedSeq = ev.Ts, ev.Seq
			continue
		}
		if ev.Kind != "tool_call" {
			continue
		}
		var c call
		if json.Unmarshal(ev.Payload, &c) != nil || c.Parent != "" || c.Child != "" {
			continue
		}
		switch c.Tool {
		case "TodoWrite":
			var in struct {
				Todos []struct {
					Content    string `json:"content"`
					Status     string `json:"status"`
					ActiveForm string `json:"activeForm"`
				} `json:"todos"`
			}
			if json.Unmarshal(c.Input, &in) != nil || in.Todos == nil {
				continue
			}
			native = false
			tasks, byID, next = nil, map[int]*planTask{}, 1
			for _, td := range in.Todos {
				t := &planTask{id: next, item: PlanItem{Text: text(td.Content), Status: planStatus(td.Status), Active: text(td.ActiveForm)}}
				next++
				tasks = append(tasks, t)
				byID[t.id] = t
			}
			updated, updatedSeq = ev.Ts, ev.Seq
		case "TaskCreate":
			var in struct {
				Subject    string `json:"subject"`
				ActiveForm string `json:"activeForm"`
			}
			if json.Unmarshal(c.Input, &in) != nil {
				continue
			}
			id, ok := ids[c.CallID]
			if id < 0 {
				continue // it failed
			}
			// A valid Claude create switches delta sources. Native items have no Claude
			// IDs, so updates alone cannot mutate them or resurrect pre-snapshot tasks.
			if native {
				if text(in.Subject) == "" {
					continue
				}
				tasks, byID, next = nil, map[int]*planTask{}, 1
				native = false
			}
			if !ok {
				id = next
			}
			if id >= next {
				next = id + 1
			}
			t := &planTask{id: id, item: PlanItem{Text: text(in.Subject), Status: "pending", Active: text(in.ActiveForm)}}
			if old := byID[id]; old != nil {
				*old = *t
			} else {
				tasks = append(tasks, t)
				byID[id] = t
			}
			updated, updatedSeq = ev.Ts, ev.Seq
		case "TaskUpdate":
			var in struct {
				TaskID     json.RawMessage `json:"taskId"`
				Status     *string         `json:"status"`
				Subject    *string         `json:"subject"`
				ActiveForm *string         `json:"activeForm"`
			}
			if json.Unmarshal(c.Input, &in) != nil {
				continue
			}
			id, ok := taskID(in.TaskID)
			t := byID[id]
			if !ok || t == nil {
				continue
			}
			if in.Status != nil && *in.Status == "deleted" {
				delete(byID, id)
				for i, x := range tasks {
					if x == t {
						tasks = append(tasks[:i], tasks[i+1:]...)
						break
					}
				}
				updated, updatedSeq = ev.Ts, ev.Seq
				continue
			}
			if in.Status != nil {
				t.item.Status = planStatus(*in.Status)
			}
			if in.Subject != nil {
				t.item.Text = text(*in.Subject)
			}
			if in.ActiveForm != nil {
				t.item.Active = text(*in.ActiveForm)
			}
			updated, updatedSeq = ev.Ts, ev.Seq
		}
	}
	if len(tasks) == 0 {
		return nil, updatedSeq
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].id < tasks[j].id })
	p := &Plan{UpdatedAt: updated}
	for _, t := range tasks {
		if len(p.Items) == MaxPlanItems {
			break
		}
		if t.item.Text == "" {
			continue
		}
		p.Items = append(p.Items, t.item)
	}
	if len(p.Items) == 0 {
		return nil, updatedSeq
	}
	return p, updatedSeq
}

// taskID reads a TaskUpdate's taskId: a string ("3") or a number.
func taskID(raw json.RawMessage) (int, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, err := strconv.Atoi(s)
		return n, err == nil
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n, true
	}
	return 0, false
}
