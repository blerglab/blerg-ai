package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestHandleAgentEventAcksAndFansOut(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "d1", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("s1", "d1")

	// Subscribe a fake browser channel to the session.
	sub := make(chan []byte, 8)
	h.Subscribe("s1", "b1", sub)

	ev := protocol.AgentEvent{
		Type: "agent_event", SessionID: "s1", ClientEventID: "ce-1",
		Ts: "2026-07-26T00:00:00Z", Kind: "turn_done", Payload: json.RawMessage(`{"stop_reason":"end_turn"}`),
	}
	HandleAgentEvent(context.Background(), h, nil, dc, ev)

	// Ack delivered to the daemon.
	select {
	case raw := <-dc.send:
		var ack protocol.AgentEventAck
		if err := json.Unmarshal(raw, &ack); err != nil || ack.Type != "agent_event_ack" || ack.ClientEventID != "ce-1" {
			t.Fatalf("ack = %s err=%v", raw, err)
		}
	default:
		t.Fatal("no ack sent to daemon")
	}

	// Event fanned out to the subscriber.
	select {
	case raw := <-sub:
		var got protocol.AgentEvent
		if err := json.Unmarshal(raw, &got); err != nil || got.Kind != "turn_done" {
			t.Fatalf("fanout = %s err=%v", raw, err)
		}
	default:
		t.Fatal("no fanout to subscriber")
	}
}

func TestForwardToSessionDaemonRoutes(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "d1", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("s1", "d1")
	bc := &BrowserConn{ID: "b1"}

	forwardToSessionDaemon(h, bc, "s1", protocol.AgentUserMessage{
		Type: "agent_user_message", SessionID: "s1", Text: "hello",
	})
	select {
	case raw := <-dc.send:
		var msg protocol.AgentUserMessage
		if err := json.Unmarshal(raw, &msg); err != nil || msg.Text != "hello" {
			t.Fatalf("forwarded = %s err=%v", raw, err)
		}
	default:
		t.Fatal("nothing forwarded to daemon")
	}

	// Unknown session: no panic, nothing sent.
	forwardToSessionDaemon(h, bc, "nope", protocol.InterruptSession{Type: "interrupt_session", SessionID: "nope"})
	select {
	case raw := <-dc.send:
		t.Fatalf("unexpected forward: %s", raw)
	default:
	}
}
