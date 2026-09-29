package daemon

import (
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// mockSender captures sent messages for assertions.
type mockSender struct {
	mu   sync.Mutex
	msgs []any
}

func (m *mockSender) Send(msg any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, msg)
	return nil
}

func (m *mockSender) stateChanges() []protocol.SessionStateChanged {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []protocol.SessionStateChanged
	for _, msg := range m.msgs {
		if sc, ok := msg.(protocol.SessionStateChanged); ok {
			out = append(out, sc)
		}
	}
	return out
}

// TestStateTracker_EmitsOnlyOnTransition verifies a state change is emitted once
// per transition, repeated identical updates are suppressed, and empty updates
// are ignored so the last known state survives a failed capture.
func TestStateTracker_EmitsOnlyOnTransition(t *testing.T) {
	sender := &mockSender{}
	tr := NewStateTracker("sess", sender)

	tr.Update("running")
	tr.Update("running") // duplicate — suppressed
	tr.Update("")        // unknown — ignored, preserves "running"
	tr.Update("idle")    // idle #1 — debounced, not yet committed
	tr.Update("idle")    // idle #2 — confirmed, emits idle
	tr.Update("running")

	got := sender.stateChanges()
	want := []string{"running", "idle", "running"}
	if len(got) != len(want) {
		t.Fatalf("emitted %d changes %v, want %d %v", len(got), got, len(want), want)
	}
	for i, w := range want {
		if got[i].Status != w {
			t.Errorf("change %d = %q, want %q", i, got[i].Status, w)
		}
	}
	if tr.State() != "running" {
		t.Errorf("State() = %q, want running", tr.State())
	}
}

// TestStateTracker_IdleHysteresis verifies a single transient idle observation
// (e.g. a mid-repaint capture during a navigation resize) does NOT flap the
// session to idle — preventing the spurious "ended its turn" notification — while
// sustained idle still commits after the confirmation threshold.
func TestStateTracker_IdleHysteresis(t *testing.T) {
	sender := &mockSender{}
	tr := NewStateTracker("sess", sender)

	tr.Update("running") // commits running
	tr.Update("idle")    // transient idle — below threshold, not committed
	tr.Update("running") // back to running before idle confirmed → no idle emitted

	got := sender.stateChanges()
	if len(got) != 1 || got[0].Status != "running" {
		t.Fatalf("transient idle must not emit; got %v", got)
	}
	if tr.State() != "running" {
		t.Errorf("State() = %q, want running", tr.State())
	}

	// Sustained idle commits once it persists for idleConfirmThreshold polls.
	tr.Update("idle") // #1
	tr.Update("idle") // #2 — confirmed
	got = sender.stateChanges()
	if len(got) != 2 || got[1].Status != "idle" {
		t.Fatalf("sustained idle must emit idle; got %v", got)
	}
	if tr.State() != "idle" {
		t.Errorf("State() = %q, want idle", tr.State())
	}
}

// TestStateTracker_FreshState verifies State() is "" before any update so the
// heartbeat omits sessions the daemon can't yet characterise.
func TestStateTracker_FreshState(t *testing.T) {
	tr := NewStateTracker("sess", &mockSender{})
	if got := tr.State(); got != "" {
		t.Errorf("fresh State() = %q, want \"\"", got)
	}
}
