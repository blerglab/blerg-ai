package shellruntime

import (
	"encoding/json"
	"testing"
)

func TestRetainedEarlyRefusalKeepsRequestWireVersion(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	for _, version := range []int{1, 2} {
		result := s.execute(t.Context(), RPC{Version: version, SessionID: "missing", Op: "describe"})
		if result.OK || result.Version != version {
			t.Fatalf("version %d refusal replied with version %d", version, result.Version)
		}
	}
}

func TestRetainedQueueRefusalKeepsRequestWireVersion(t *testing.T) {
	replies := make(chan RPC, 1)
	s := NewService(func(r RPC) error { replies <- r; return nil })
	q := &sessionQueue{done: make(chan struct{}), control: make(chan RPC, 1), data: make(chan RPC, 1)}
	q.control <- RPC{}
	s.queues["session"] = q
	raw, err := json.Marshal(RPC{Version: 2, Type: "owner_shell_request", SessionID: "session", RequestID: "request", Op: "configure"})
	if err != nil {
		t.Fatal(err)
	}
	s.HandleContext(t.Context(), raw)
	select {
	case r := <-replies:
		if r.Version != 2 || r.OK {
			t.Fatal("queue refusal changed the wire version")
		}
	default:
		t.Fatal("queue refusal missing")
	}
}
