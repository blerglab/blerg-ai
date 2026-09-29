package server

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestRunnerBoundSession(t *testing.T) {
	cases := []struct{ mode, name, want string }{
		{"runner", "runner-abc", "abc"},
		{"runner", "other", ""},
		{"local", "runner-abc", ""},
		{"local", "laptop", ""},
	}
	for _, c := range cases {
		if got := runnerBoundSession(c.mode, c.name); got != c.want {
			t.Errorf("runnerBoundSession(%q,%q) = %q, want %q", c.mode, c.name, got, c.want)
		}
	}
	if got := boundSessionIDs("runner", "runner-a", []string{"a", "b"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("runner ids = %v, want [a]", got)
	}
	if got := boundSessionIDs("local", "laptop", []string{"a", "b"}); len(got) != 2 {
		t.Errorf("daemon ids = %v, want both", got)
	}
}

// A runner pod authenticates with the shared daemon token, so it must only
// be able to speak for its own session: an event it sends for another
// session is dropped, not fanned out (or persisted).
func TestRunnerPodCannotSpeakForAnotherSession(t *testing.T) {
	hub := NewHub()
	conn := dialDaemon(t, hub)
	hello, _ := json.Marshal(protocol.DaemonHello{
		Type: "daemon_hello", Name: "runner-own", Mode: "runner", ReposRoot: "/workspace",
		Token: "tok-1234567890", ProtocolVersion: "1", ActiveSessions: []string{},
	})
	if err := conn.WriteMessage(websocket.TextMessage, hello); err != nil {
		t.Fatal(err)
	}
	own := make(chan []byte, 8)
	foreign := make(chan []byte, 8)
	hub.Subscribe("own", "b1", own)
	hub.Subscribe("victim", "b1", foreign)

	send := func(sid string) {
		raw, _ := json.Marshal(protocol.AgentEvent{
			Type: "agent_event", SessionID: sid, ClientEventID: newUUID(), Kind: "assistant_text",
			Payload: json.RawMessage(`{"text":"x","done":false}`), Transient: true,
		})
		if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			t.Fatal(err)
		}
	}
	send("victim")
	send("own")
	select {
	case <-own:
	case <-time.After(3 * time.Second):
		t.Fatal("the pod's own event was not delivered")
	}
	select {
	case m := <-foreign:
		t.Fatalf("event for a foreign session was delivered: %s", m)
	case <-time.After(100 * time.Millisecond):
	}
}
