package daemon

import (
	"encoding/json"
	"sort"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const backgroundSeenLimit = 65536

type backgroundObservedEvent struct{ kind, task string }

// All fields are protected by WSClient.mu. Replay IDs outlive acknowledgments:
// an older start may still be in a concurrent outbox snapshot after done acked.
type backgroundTaskTracker struct {
	lifetime string
	known    bool
	seen     map[string]backgroundObservedEvent
	live     map[string]map[string]bool
	total    int
}

func newBackgroundTaskTracker() *backgroundTaskTracker {
	return &backgroundTaskTracker{lifetime: ccUUID(), known: true, seen: map[string]backgroundObservedEvent{}, live: map[string]map[string]bool{}}
}
func (t *backgroundTaskTracker) invalidate() {
	t.known = false
	t.seen = nil
	t.live = nil
	t.total = 0
}

// observeBackgroundTasks runs before the disconnected return in Send, so the
// snapshot includes tasks created while offline. It never retains descriptions,
// tool output, endpoints, summaries or any other free-form task content.
func (c *WSClient) observeBackgroundTasks(raw []byte) {
	var event struct {
		Type          string `json:"type"`
		SessionID     string `json:"session_id"`
		ClientEventID string `json:"client_event_id"`
		Kind          string `json:"kind"`
		Transient     bool   `json:"transient"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	if event.Type == "session_ended" {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.background != nil && c.background.known {
			c.background.total -= len(c.background.live[event.SessionID])
			delete(c.background.live, event.SessionID)
		}
		return
	}
	if event.Type != "agent_event" || (event.Kind != "background_task_started" && event.Kind != "background_task_done") {
		return
	}
	var lifecycle struct {
		Payload struct {
			TaskID string `json:"task_id"`
		} `json:"payload"`
	}
	payloadOK := json.Unmarshal(raw, &lifecycle) == nil
	taskID := lifecycle.Payload.TaskID
	c.mu.Lock()
	defer c.mu.Unlock()
	tracker := c.background
	if tracker == nil || !tracker.known {
		return
	}
	singleton := &protocol.BackgroundTaskInventory{Lifetime: tracker.lifetime, Sessions: map[string][]string{event.SessionID: {taskID}}}
	if !payloadOK || event.Transient || !protocol.ValidBackgroundTaskID(event.ClientEventID) || !protocol.ValidBackgroundTaskInventory(singleton) {
		tracker.invalidate()
		return
	}
	key := event.SessionID + "\x00" + event.ClientEventID
	descriptor := backgroundObservedEvent{kind: event.Kind, task: taskID}
	if old, seen := tracker.seen[key]; seen {
		if old != descriptor {
			tracker.invalidate()
		}
		return
	}
	if len(tracker.seen) >= backgroundSeenLimit {
		tracker.invalidate()
		return
	}
	tracker.seen[key] = descriptor
	tasks := tracker.live[event.SessionID]
	if event.Kind == "background_task_started" {
		if tasks == nil {
			if len(tracker.live) >= protocol.MaxBackgroundTaskSessions {
				tracker.invalidate()
				return
			}
			tasks = map[string]bool{}
			tracker.live[event.SessionID] = tasks
		}
		if !tasks[taskID] {
			if tracker.total >= protocol.MaxBackgroundTasks {
				tracker.invalidate()
				return
			}
			tasks[taskID] = true
			tracker.total++
		}
	} else {
		if tasks[taskID] {
			delete(tasks, taskID)
			tracker.total--
		}
		if len(tasks) == 0 {
			delete(tracker.live, event.SessionID)
		}
	}
}

func (c *WSClient) backgroundTaskInventory() *protocol.BackgroundTaskInventory {
	c.mu.Lock()
	tracker := c.background
	if tracker == nil || !tracker.known {
		c.mu.Unlock()
		return nil
	}
	snapshot := &protocol.BackgroundTaskInventory{Lifetime: tracker.lifetime, Sessions: make(map[string][]string, len(tracker.live))}
	for session, tasks := range tracker.live {
		ids := make([]string, 0, len(tasks))
		for id := range tasks {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		snapshot.Sessions[session] = ids
	}
	c.mu.Unlock()
	if !protocol.ValidBackgroundTaskInventory(snapshot) {
		return nil
	}
	return snapshot
}
