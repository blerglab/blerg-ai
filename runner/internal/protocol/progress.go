package protocol

import "encoding/json"

const ProgressUpdatedKind = "progress_updated"
const MaxProgressSteps = 1_000_000
const MaxProgressUpdatedBytes = 4 << 10

// ProgressUpdatedPayload is a complete explicit progress snapshot for one scope.
type ProgressUpdatedPayload struct {
	Completed    int    `json:"completed"`
	Total        int    `json:"total"`
	Label        string `json:"label"`
	ParentCallID string `json:"parent_call_id,omitempty"`
	ChildID      string `json:"child_id,omitempty"`
}

// ProgressScope validates opaque attribution without clipping or guessing identities.
func ProgressScope(parent, child string) (string, bool) {
	if (parent != "" && !ValidBackgroundTaskID(parent)) || (child != "" && !ValidBackgroundTaskID(child)) || (parent != "" && child != "" && parent != child) {
		return "", false
	}
	if child != "" {
		return child, true
	}
	return parent, true
}

func DecodeProgressUpdated(raw json.RawMessage) (ProgressUpdatedPayload, bool) {
	var p ProgressUpdatedPayload
	if len(raw) > MaxProgressUpdatedBytes || json.Unmarshal(raw, &p) != nil {
		return p, false
	}
	return SanitizeProgressUpdated(p)
}

func SanitizeProgressUpdated(p ProgressUpdatedPayload) (ProgressUpdatedPayload, bool) {
	if _, ok := ProgressScope(p.ParentCallID, p.ChildID); !ok || p.Total < 1 || p.Total > MaxProgressSteps || p.Completed < 0 || p.Completed > p.Total {
		return ProgressUpdatedPayload{}, false
	}
	p.Label = capClean(p.Label, MaxPlanText)
	if p.Label == "" {
		return ProgressUpdatedPayload{}, false
	}
	return p, true
}
