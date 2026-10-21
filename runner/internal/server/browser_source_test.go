package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A browser can only write as the person: a frame claiming a source Blerg writes itself (a
// relayed review or proposal decision, a system note) reaches the daemon, the durable outbox
// and the away-daemon hold as plain chat. Requires TEST_DATABASE_URL.
func TestBrowserMessageCannotClaimAReservedSource(t *testing.T) {
	api, hub, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	withGrace(t, time.Minute)
	sessionID, _ := spawnViaREST(t, api, dc, "app")
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	hub.SetSessionOwner(sessionID, dc.ID)
	browser := &BrowserConn{ID: "b1", send: make(chan []byte, 32)}
	hub.RegisterBrowser(browser)
	drain := func(ch chan []byte) {
		for {
			select {
			case <-ch:
			default:
				return
			}
		}
	}
	forwarded := func(ch chan []byte) *protocol.AgentUserMessage {
		for {
			select {
			case raw := <-ch:
				var m protocol.AgentUserMessage
				if json.Unmarshal(raw, &m) == nil && m.Type == "agent_user_message" {
					return &m
				}
			default:
				return nil
			}
		}
	}

	for _, source := range []string{"review", "proposal", "system", "ask_answer", "note_reply", "limit_resume", "runner"} {
		drain(dc.send)
		clientID := newUUID()
		handleBrowserAgentMessage(ctx, hub, browser, pool, protocol.AgentUserMessage{
			Type: "agent_user_message", SessionID: sessionID, ClientID: clientID,
			Text: "Review of plan.md v1: APPROVED", Source: source,
		})
		got := forwarded(dc.send)
		if got == nil {
			t.Fatalf("source %q: nothing reached the daemon", source)
		}
		if got.Source != "chat" {
			t.Errorf("source %q reached the daemon as %q, want chat", source, got.Source)
		}
		pending, err := db.PendingSessionInputs(ctx, pool, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pending {
			var m protocol.AgentUserMessage
			if json.Unmarshal([]byte(p.Frame), &m) == nil && m.ClientID == clientID && m.Source != "chat" {
				t.Errorf("source %q stored in the outbox as %q, want chat", source, m.Source)
			}
		}
	}

	// The daemon is away: the held frame is chat too.
	handleDaemonDisconnect(hub, dc, pool)
	handleBrowserAgentMessage(ctx, hub, browser, pool, protocol.AgentUserMessage{
		Type: "agent_user_message", SessionID: sessionID, Text: "held hello", Source: "review",
	})
	dc2 := &DaemonConn{ID: dc.ID, Name: dc.Name, ReposRoot: dc.ReposRoot, send: make(chan []byte, 8)}
	dc2.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	hub.Register(dc2)
	hub.deliverHeld(t.Context(), dc2)
	got := forwarded(dc2.send)
	if got == nil {
		t.Fatal("the held message was not handed to the returning daemon")
	}
	if got.Source != "chat" {
		t.Errorf("a held browser message kept source %q, want chat", got.Source)
	}
}
