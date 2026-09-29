package daemon

import (
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// StateTracker holds a session's current externally-visible state
// ("running"/"idle"/"waiting") and emits a session_state_changed event whenever
// it transitions.
//
// State is derived from periodic screen snapshots (see classifyScreen and the
// Manager's state poller) rather than the raw PTY byte stream. The byte stream
// can't distinguish a resize repaint of static content (the "✻ Cooked for 24s"
// turn summary, the "… +N lines" indicator) from a live animation frame, which
// made glyph-based detection flip idle sessions to "running" on every redraw.
// The rendered screen tells the truth, so the tracker just records what the
// classifier reports.
type StateTracker struct {
	sessionID string
	sender    Sender

	mu         sync.Mutex
	state      string // "", "running", "idle", or "waiting"
	idleStreak int    // consecutive idle observations not yet committed (hysteresis)
}

// idleConfirmThreshold is how many consecutive idle observations must be seen
// before the tracker commits an idle transition. The state poller snapshots every
// statePollInterval (1s); navigating to/from a session triggers a resize +
// refresh-client repaint, and a poll that lands mid-repaint can momentarily
// classify a busy session as idle. Requiring idle to persist for two polls
// suppresses that single-frame flap (which otherwise fired a spurious
// "session ended its turn" notification on every navigation) while adding at most
// ~1s of latency to a genuine idle. Running/waiting commit immediately so
// attention-needed states stay responsive.
const idleConfirmThreshold = 2

// NewStateTracker creates a StateTracker for sessionID that emits on sender.
func NewStateTracker(sessionID string, sender Sender) *StateTracker {
	return &StateTracker{sessionID: sessionID, sender: sender}
}

// Update records the latest classified state, emitting session_state_changed
// only on a transition. An empty state (classifier couldn't characterise the
// screen, or capture failed) is ignored so the last known state is preserved.
func (t *StateTracker) Update(state string) {
	if state == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	// Idle is debounced: a session must be observed idle for idleConfirmThreshold
	// consecutive polls before the transition is committed, so a single mid-repaint
	// frame (e.g. during a navigation-triggered resize) doesn't flap to idle and
	// fire a spurious notification. Non-idle states reset the streak and commit
	// immediately.
	if state == "idle" {
		if t.state == "idle" {
			return
		}
		t.idleStreak++
		if t.idleStreak < idleConfirmThreshold {
			return
		}
	} else {
		t.idleStreak = 0
	}

	if state == t.state {
		return
	}
	t.state = state
	t.sendState(state)
}

// State returns the current externally-visible state, or "" if none has been
// observed yet. Used by the heartbeat to periodically re-assert state so a
// dropped session_state_changed event self-heals.
func (t *StateTracker) State() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// sendState emits a session_state_changed message. Must hold t.mu.
func (t *StateTracker) sendState(status string) {
	_ = t.sender.Send(protocol.SessionStateChanged{
		Type:      "session_state_changed",
		SessionID: t.sessionID,
		Status:    status,
	})
}
