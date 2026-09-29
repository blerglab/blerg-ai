package agent

import (
	"regexp"
	"sync"
	"testing"
)

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) Emit(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	for i, ev := range r.events {
		out[i] = ev.Kind
	}
	return out
}

func (r *recorder) byKind(kind string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, ev := range r.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewEventMintsUUIDAndTimestamp(t *testing.T) {
	ev := newEvent("check_in", CheckInPayload{Phase: "task_start", Summary: "s"})
	if !uuidRe.MatchString(ev.ClientEventID) {
		t.Fatalf("ClientEventID not a v4 UUID: %q", ev.ClientEventID)
	}
	if ev.Ts.IsZero() {
		t.Fatal("Ts is zero")
	}
	if ev.Kind != "check_in" {
		t.Fatalf("kind = %q", ev.Kind)
	}
	ev2 := newEvent("check_in", nil)
	if ev.ClientEventID == ev2.ClientEventID {
		t.Fatal("UUIDs must be unique")
	}
}
