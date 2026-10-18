package protocol

import "encoding/json"

const PlanUpdatedKind = "plan_updated"
const MaxPlanItems = 100
const MaxPlanText = 200

// MaxPlanUpdatedBytes bounds decoding before allocations from untrusted wire strings.
const MaxPlanUpdatedBytes = 256 << 10

// PlanItem is an engine-neutral plan step in display order. Active is meaningful only in progress.
type PlanItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
	Active string `json:"active,omitempty"`
}

// PlanUpdatedPayload replaces the root or one attributed child plan. Empty Items clears that scope.
type PlanUpdatedPayload struct {
	Items        []PlanItem `json:"items"`
	ParentCallID string     `json:"parent_call_id,omitempty"`
	ChildID      string     `json:"child_id,omitempty"`
}

// DecodePlanUpdated checks untrusted wire data without treating absent/null items as an empty plan.
func DecodePlanUpdated(raw json.RawMessage) (PlanUpdatedPayload, bool) {
	var p PlanUpdatedPayload
	if len(raw) > MaxPlanUpdatedBytes {
		return PlanUpdatedPayload{}, false
	}
	if json.Unmarshal(raw, &p) != nil {
		return PlanUpdatedPayload{}, false
	}
	return SanitizePlanUpdated(p)
}

// SanitizePlanUpdated returns only bounded display metadata. A malformed item invalidates the
// whole snapshot, preserving the prior plan; only an explicit empty list can clear it.
func SanitizePlanUpdated(p PlanUpdatedPayload) (PlanUpdatedPayload, bool) {
	if _, ok := ProgressScope(p.ParentCallID, p.ChildID); !ok || p.Items == nil || len(p.Items) > MaxPlanItems {
		return PlanUpdatedPayload{}, false
	}
	out := PlanUpdatedPayload{Items: make([]PlanItem, 0, len(p.Items)), ParentCallID: p.ParentCallID, ChildID: p.ChildID}
	for _, item := range p.Items {
		switch item.Status {
		case "pending", "in_progress", "completed":
		default:
			return PlanUpdatedPayload{}, false
		}
		clean := PlanItem{Text: capClean(item.Text, MaxPlanText), Status: item.Status}
		if clean.Text == "" {
			return PlanUpdatedPayload{}, false
		}
		if item.Status == "in_progress" {
			clean.Active = capClean(item.Active, MaxPlanText)
		}
		out.Items = append(out.Items, clean)
	}
	return out, true
}
