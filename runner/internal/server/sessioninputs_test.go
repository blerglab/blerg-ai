package server

import (
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestSessionInputAcceptanceSurvivesReconnectAndSettles(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, Text: "Keep working", ClientID: "browser-input-1", Source: "chat"}
	fresh, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg)
	if err != nil || !fresh {
		t.Fatalf("accept: %v %v", fresh, err)
	}
	fresh, err = acceptSessionInput(f.ctx, f.hub, f.pool, msg)
	if err != nil || fresh {
		t.Fatalf("duplicate: %v %v", fresh, err)
	}
	rows, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending: %v %v", rows, err)
	}
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	var replay protocol.AgentUserMessage
	if err := json.Unmarshal(<-f.dc.send, &replay); err != nil || replay.ClientID != msg.ClientID || replay.Text != msg.Text {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	f.ingest("consumed", "user_message", `{"client_id":"browser-input-1","text":"Keep working","source":"chat"}`, "")
	rows, err = db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("settled: %v %v", rows, err)
	}
	fresh, err = acceptSessionInput(f.ctx, f.hub, f.pool, msg)
	if err != nil || fresh {
		t.Fatalf("completed duplicate: %v %v", fresh, err)
	}
}

func TestSessionInputSettlesEvenWhenConsumptionIsReplayed(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, Text: "Continue", ClientID: "retry-input"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	// Simulate the process dying after saving the consumption event but before updating the outbox.
	if _, _, err := db.AppendAgentEvent(f.ctx, f.pool, f.sessionID, cid("already-saved"), "user_message", `{"client_id":"retry-input","text":"Continue"}`); err != nil {
		t.Fatal(err)
	}
	f.ingest("already-saved", "user_message", `{"client_id":"retry-input","text":"Continue"}`, "")
	pending, err := db.PendingSessionInputs(f.ctx, f.pool, f.sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("replayed consumption must settle: %v %v", pending, err)
	}
}

func TestSessionInputPendingAcceptanceIncludedOutsideReplayTail(t *testing.T) {
	f := newIngestFixture(t)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, Text: "Next turn", ClientID: "long-turn-input"}
	if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, _, err := db.AppendAgentEvent(f.ctx, f.pool, f.sessionID, newUUID(), "assistant_text", `{"text":"Still working","done":true}`); err != nil {
			t.Fatal(err)
		}
	}
	browser := &BrowserConn{ID: "tail-browser", send: make(chan []byte, 32)}
	replayAgentEvents(f.ctx, f.hub, browser, f.pool, protocol.SubscribeAgentEvents{Type: "subscribe_agent_events", SessionID: f.sessionID, Tail: 2, Limit: 2})
	var event protocol.AgentEvent
	if err := json.Unmarshal(<-browser.send, &event); err != nil {
		t.Fatal(err)
	}
	if event.Kind != "input_accepted" || event.Seq != 1 {
		t.Fatalf("pending acceptance omitted: %+v", event)
	}
}

func TestSessionInputNonReceiptDoesNotBlockIngestion(t *testing.T) {
	pool := lazyPool(t)
	for _, payload := range []string{`{`, `{"client_id":12}`, `{"message":"native error"}`} {
		for _, kind := range []string{"error", "user_message"} {
			if err := settleSessionInput(t.Context(), pool, protocol.AgentEvent{Kind: kind, Payload: json.RawMessage(payload)}); err != nil {
				t.Errorf("%s %s: non-receipt blocked ingestion: %v", kind, payload, err)
			}
		}
	}
}

// Card #156: the input_accepted event says whose message it is, so the chat never draws Blerg's
// relay of an answer as the person's queued bubble; the message the agent gets is unchanged.
func TestSessionInputAcceptanceNamesItsSource(t *testing.T) {
	f := newIngestFixture(t)
	row := db.MessageRow{ID: "7b5dcd88-7c6a-4a6d-b093-d6417264f6d2", Body: "Favorite color?", Kind: "ask", NoWait: true}
	text := questionAnswerText(row, "orange")
	if text != `Answer to your question 7b5dcd88-7c6a-4a6d-b093-d6417264f6d2 ("Favorite color?"): orange` {
		t.Fatalf("the agent's message changed: %q", text)
	}
	relay := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, Text: text, ClientID: questionAnswerClientID(row.ID), Source: questionAnswerSource}
	person := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, Text: "hi", ClientID: "browser-1"}
	for _, msg := range []protocol.AgentUserMessage{relay, person} {
		if _, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.pool.Query(f.ctx, `SELECT payload FROM agent_events WHERE session_id=$1 AND kind='input_accepted' ORDER BY seq`, f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		got = append(got, p)
	}
	if len(got) != 2 || got[0]["source"] != questionAnswerSource || got[0]["text"] != text {
		t.Fatalf("relay acceptance = %v", got)
	}
	if _, ok := got[1]["source"]; ok {
		t.Fatalf("a person's message gained a source: %v", got[1])
	}
	// The frame the agent is sent (replayed from the outbox) is the relay as it was.
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	var replay protocol.AgentUserMessage
	if err := json.Unmarshal(<-f.dc.send, &replay); err != nil || replay.Text != text || replay.Source != questionAnswerSource {
		t.Fatalf("the agent got %+v %v", replay, err)
	}
}
