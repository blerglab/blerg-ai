package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestHandleAgentEventAcksAndFansOut(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "d1", send: make(chan []byte, 8)}
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	dc.SetWorkBranches(true)  // current daemon: immutable recorded branch names
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
	dc.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	dc.SetWorkBranches(true)  // current daemon: immutable recorded branch names
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

func TestBrowserStopPreservesTargetTurnEvent(t *testing.T) {
	f := newPrivFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", f.hub.ServeBrowser(f.pool, f.api.AuthorizeBrowserWS))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	browser := f.dial(t, srv, f.tok(t, "human", privAcctA, coreAuthBrowserCap))
	browser.until(t, "initial_state")
	browser.send(t, map[string]any{"type": "interrupt_session", "session_id": f.pubSession, "target_event_id": "stopped-turn-event"})
	deadline := time.After(5 * time.Second)
	for {
		select {
		case raw := <-f.dc.send:
			var msg map[string]any
			_ = json.Unmarshal(raw, &msg)
			if msg["type"] != "interrupt_session" {
				continue
			}
			if msg["target_event_id"] != "stopped-turn-event" {
				t.Fatalf("Stop lost its turn target: %s", raw)
			}
			return
		case <-deadline:
			t.Fatal("Stop was not forwarded")
		}
	}
}

func TestBrowserPausedStopDoesNotAppendNotices(t *testing.T) {
	f := newPrivFixture(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", f.hub.ServeBrowser(f.pool, f.api.AuthorizeBrowserWS))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	browser := f.dial(t, srv, f.tok(t, "human", privAcctA, coreAuthBrowserCap))
	browser.until(t, "initial_state")
	handleSessionPaused(t.Context(), f.hub, f.pool, f.dc, protocol.SessionPaused{SessionID: f.pubSession, Reason: protocol.PausedByRestart, Detail: "Recovery paused: Workspace stop is unfinished. Send a message to retry."})
	for range 3 {
		browser.send(t, map[string]any{"type": "interrupt_session", "session_id": f.pubSession})
	}
	// Retrying recovery through the real browser wire only queues input.
	browser.send(t, map[string]any{"type": "agent_user_message", "session_id": f.pubSession, "text": "Retry recovery", "source": "chat", "client_id": newUUID()})
	// The next frame is a barrier: the interrupt and input frames precede its reply.
	browser.send(t, map[string]any{"type": "set_session_star", "session_id": f.pubSession, "starred": true})
	browser.until(t, "session_star_changed")
	var notices int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='stop_result'`, f.pubSession).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 0 {
		t.Fatalf("paused Stop appended %d notices", notices)
	}
	var paused string
	if err := f.pool.QueryRow(t.Context(), `SELECT coalesce(paused_reason,'') FROM sessions WHERE id=$1`, f.pubSession).Scan(&paused); err != nil {
		t.Fatal(err)
	}
	if paused != protocol.PausedByRestart {
		t.Fatal("browser message prematurely claimed recovery")
	}
	if err := resolveSessionFailureAlerts(t.Context(), f.hub, f.pool, f.pubSession); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='mark' AND payload->>'text'='Session recovered; its failure alert was resolved.'`, f.pubSession).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 0 {
		t.Fatal("unrecovered browser input produced a recovery mark")
	}
}

func TestAgentAskCannotClaimLiveBinding(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "fixture-daemon", send: make(chan []byte, 8)}
	dc.SetSandboxClones(true)
	dc.SetWorkBranches(true)
	h.Register(dc)
	h.SetSessionOwner("fixture-session", dc.ID)
	sub := make(chan []byte, 8)
	h.Subscribe("fixture-session", "fixture-browser", sub)
	HandleAgentEvent(t.Context(), h, nil, dc, protocol.AgentEvent{Type: "agent_event", SessionID: "fixture-session", ClientEventID: "fixture-event", Ts: "2026-11-05T00:00:00Z", Kind: "ask", Payload: json.RawMessage(`{"body":"Ordinary question","message_id":"ordinary","live_binding":true,"decision":true}`)})
	select {
	case raw := <-sub:
		var got protocol.AgentEvent
		if err := json.Unmarshal(raw, &got); err != nil || got.Kind != "ask" {
			t.Fatal("missing ask fanout", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(got.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if _, present := payload["live_binding"]; present || payload["decision"] != nil {
			t.Fatal("daemon supplied a server-only live-binding marker")
		}
		if payload["message_id"] != "ordinary" || payload["body"] != "Ordinary question" {
			t.Fatal("ordinary question was changed")
		}
	default:
		t.Fatal("missing ask event")
	}
}
