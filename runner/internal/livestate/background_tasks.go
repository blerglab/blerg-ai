package livestate

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

// BackgroundTaskState identifies the current incarnation of an open task. StartEventID and
// Lifetime stay internal; the public Task shape is unchanged.
type BackgroundTaskState struct {
	Task         Task
	StartEventID string
	Lifetime     string
	OriginalSeq  int64
}

// OpenBackgroundTasks folds semantic task identities in sequence order, without the UI's age
// cutoff. Duplicate IDs replace the prior incarnation. A targeted synthetic completion only
// stops its original start; delayed old-lifetime or old-call completions cannot stop a new one.
func OpenBackgroundTasks(events []Event) []BackgroundTaskState {
	current := map[string]BackgroundTaskState{}
	for _, event := range sorted(events) {
		switch event.Kind {
		case "background_task_started":
			var p struct {
				agent.BackgroundTaskStartedPayload
				Lifetime    string `json:"daemon_lifetime"`
				OriginalSeq int64  `json:"origin_start_seq"`
			}
			if json.Unmarshal(event.Payload, &p) != nil || p.TaskID == "" || p.OriginalSeq < 0 || p.OriginalSeq > event.Seq {
				continue
			}
			original := p.OriginalSeq
			if original == 0 {
				original = event.Seq
			}
			at := event.Ts
			if parsed, err := time.Parse(time.RFC3339Nano, p.StartedAt); err == nil {
				at = parsed
			}
			current[p.TaskID] = BackgroundTaskState{Task: Task{TaskID: p.TaskID, CallID: p.CallID, Kind: p.Kind, Description: p.Description, StartedAt: at, Seq: event.Seq}, StartEventID: event.ID, Lifetime: p.Lifetime, OriginalSeq: original}
		case "background_task_done":
			var p struct {
				TaskID       string `json:"task_id"`
				CallID       string `json:"call_id"`
				Lifetime     string `json:"daemon_lifetime"`
				StartEventID string `json:"start_event_id"`
			}
			if json.Unmarshal(event.Payload, &p) != nil || p.TaskID == "" {
				continue
			}
			open, ok := current[p.TaskID]
			if !ok {
				continue
			}
			if p.StartEventID != "" {
				if p.StartEventID != open.StartEventID {
					continue
				}
			} else {
				if p.Lifetime != "" && open.Lifetime != "" && p.Lifetime != open.Lifetime {
					continue
				}
				if p.CallID != "" && open.Task.CallID != "" && p.CallID != open.Task.CallID {
					continue
				}
			}
			delete(current, p.TaskID)
		}
	}
	out := make([]BackgroundTaskState, 0, len(current))
	for _, open := range current {
		out = append(out, open)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Task.Seq != out[j].Task.Seq {
			return out[i].Task.Seq < out[j].Task.Seq
		}
		return out[i].Task.TaskID < out[j].Task.TaskID
	})
	return out
}
