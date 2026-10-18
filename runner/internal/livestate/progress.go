package livestate

import (
	"encoding/json"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Progress is display-only progress for one currently attributed scope.
type Progress struct {
	Completed int       `json:"completed"`
	Total     int       `json:"total"`
	Label     string    `json:"label"`
	UpdatedAt time.Time `json:"updated_at"`
}

// scopeEvents projects one incarnation into the root-only historical plan reducer. Neither
// another child's snapshots nor its local TaskCreate IDs can mutate this scope.
func scopeEvents(events []Event, key string, since int64) []Event {
	out := make([]Event, 0, len(events))
	for _, e := range events {
		if e.Seq <= since {
			continue
		}
		var marker struct {
			Parent string `json:"parent_call_id"`
			Child  string `json:"child_id"`
		}
		if json.Unmarshal(e.Payload, &marker) != nil {
			continue
		}
		scope, ok := protocol.ProgressScope(marker.Parent, marker.Child)
		if !ok || scope != key {
			continue
		}
		if e.Kind == protocol.PlanUpdatedKind {
			p, ok := protocol.DecodePlanUpdated(e.Payload)
			if !ok {
				continue
			}
			p.ParentCallID, p.ChildID = "", ""
			e.Payload, _ = json.Marshal(p)
		} else if e.Kind == "tool_call" || e.Kind == "tool_result" {
			if len(e.Payload) > protocol.MaxPlanUpdatedBytes {
				continue
			}
			var p map[string]json.RawMessage
			if json.Unmarshal(e.Payload, &p) != nil {
				continue
			}
			delete(p, "parent_call_id")
			delete(p, "child_id")
			e.Payload, _ = json.Marshal(p)
		}
		out = append(out, e)
	}
	return out
}

// ProgressFor selects the newest explicit snapshot or native plan, by event sequence rather
// than wall-clock time. An empty native plan clears this scope without reviving older progress.
func ProgressFor(events []Event, key string, since int64) *Progress {
	scoped := scopeEvents(events, key, since)
	p, planSeq := foldPlan(scoped)
	var latest *Progress
	var latestSeq int64
	for _, e := range scoped {
		if e.Kind != protocol.ProgressUpdatedKind || e.Seq <= latestSeq {
			continue
		}
		v, ok := protocol.DecodeProgressUpdated(e.Payload)
		if !ok {
			continue
		}
		latest = &Progress{Completed: v.Completed, Total: v.Total, Label: v.Label, UpdatedAt: e.Ts}
		latestSeq = e.Seq
	}
	if latest != nil && latestSeq > planSeq {
		return latest
	}
	if p == nil {
		return nil
	}
	progress := &Progress{Total: len(p.Items), UpdatedAt: p.UpdatedAt}
	active := ""
	for _, item := range p.Items {
		if item.Status == "completed" {
			progress.Completed++
			progress.Label = item.Text
		}
		if active == "" && item.Status == "in_progress" {
			active = item.Active
			if active == "" {
				active = item.Text
			}
		}
	}
	if active != "" {
		progress.Label = active
	}
	return progress
}
