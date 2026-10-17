package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestMCPStatusSanitizedBeforePersistFanoutAndReplay(t *testing.T) {
	fx := newLiveFx(t)
	dc := &DaemonConn{ID: "fixture-mcp", send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	sub := make(chan []byte, 8)
	fx.api.hub.Subscribe(fx.sess, "fixture-browser", sub)
	payload := json.RawMessage(`{"engine":"claude","since":"2026-10-16T12:00:00Z","servers":[{"name":"jira","status":"pending","source":"gateway","tools":0,"url":"https://private.invalid","env":{"TOKEN":"fixture-private"}},{"name":"https://private.invalid","status":"connected"}],"url":"https://private.invalid","token":"fixture-private","hold":{"since":"2026-10-16T12:00:00Z","until":"2026-10-16T12:00:30Z"}}`)
	now := time.Now().UTC()
	payload = json.RawMessage(strings.ReplaceAll(strings.ReplaceAll(string(payload), "2026-10-16T12:00:30Z", now.Add(30*time.Second).Format(time.RFC3339Nano)), "2026-10-16T12:00:00Z", now.Format(time.RFC3339Nano)))
	HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, dc, protocol.AgentEvent{Type: "agent_event", SessionID: fx.sess, ClientEventID: newUUID(), Kind: protocol.MCPStatusKind, Payload: payload, Transient: true})
	<-dc.send
	raw := <-sub
	if strings.Contains(string(raw), "private.invalid") || strings.Contains(string(raw), "fixture-private") {
		t.Fatalf("unsafe fanout %s", raw)
	}
	var event protocol.AgentEvent
	if err := json.Unmarshal(raw, &event); err != nil || event.Transient {
		t.Fatalf("snapshot not durable %+v %v", event, err)
	}
	rows, err := db.ListLiveEvents(t.Context(), fx.pool, fx.sess, 1000000)
	if err != nil || len(rows) != 1 || strings.Contains(rows[0].Payload, "private.invalid") {
		t.Fatalf("unsafe persistence %+v %v", rows, err)
	}
	owner := fx.token(topicOwner, "human")
	rec := fx.getLive(fx.sess, owner, "")
	var body struct {
		MCP     *protocol.MCPStatusPayload `json:"mcp"`
		MCPHold *protocol.MCPHold          `json:"mcp_hold"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || body.MCP == nil || len(body.MCP.Servers) != 1 || body.MCP.Servers[0].Name != "jira" || body.MCPHold == nil || body.MCP.Hold == nil {
		t.Fatalf("replayed %d %s (%v)", rec.Code, rec.Body, err)
	}
	if !strings.Contains(rec.Body.String(), `"can_release_prompt":true`) {
		t.Fatalf("the owner is told they may release: %s", rec.Body)
	}
	release := protocol.MCPStatusPayload{Engine: "claude", Since: now.Format(time.RFC3339Nano), Released: "person", Servers: []protocol.MCPServerStatus{{Name: "jira", Status: "pending", Source: "gateway"}}}
	releaseRaw, _ := json.Marshal(release)
	HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, dc, protocol.AgentEvent{Type: "agent_event", SessionID: fx.sess, ClientEventID: newUUID(), Kind: protocol.MCPStatusKind, Payload: releaseRaw})
	<-dc.send
	<-sub
	rec = fx.getLive(fx.sess, owner, "")
	body.MCP = nil
	body.MCPHold = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.MCP == nil || body.MCP.Released != "person" || body.MCPHold != nil || body.MCP.Hold != nil {
		t.Fatalf("released snapshot %d %s (%v)", rec.Code, rec.Body, err)
	}
	// Old daemon: no invented snapshot in a session that has never emitted one.
	rec = fx.getLive(fx.ended, owner, "")
	if strings.Contains(rec.Body.String(), `"mcp"`) || strings.Contains(rec.Body.String(), `"mcp_hold"`) {
		t.Fatalf("old daemon state %s", rec.Body)
	}
	// Inventory inherits the exact private-session access boundary of every live field.
	fx.event(fx.privSess, "mcp_status", protocol.MCPStatusPayload{Engine: "claude", Since: time.Now().UTC().Format(time.RFC3339), Servers: []protocol.MCPServerStatus{{Name: "private-jira", Status: "connected"}}})
	rec = fx.getLive(fx.privSess, owner, "")
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "private-jira") {
		t.Fatalf("private inventory leaked %d %s", rec.Code, rec.Body)
	}
}

func TestMCPStatusMalformedAckAndUnknownEventCompatibility(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "fixture", send: make(chan []byte, 8)}
	h.Register(dc)
	sub := make(chan []byte, 8)
	h.Subscribe("fixture-session", "fixture-browser", sub)
	for _, payload := range []string{`{"servers":"bad"}`, `{"engine":"claude","since":"bad"}`} {
		HandleAgentEvent(t.Context(), h, nil, dc, protocol.AgentEvent{Type: "agent_event", SessionID: "fixture-session", ClientEventID: newUUID(), Kind: protocol.MCPStatusKind, Payload: json.RawMessage(payload)})
		if len(dc.send) != 1 || len(sub) != 0 {
			t.Fatal("malformed snapshot was not dropped and acknowledged")
		}
		<-dc.send
	}
	// New event kinds remain forward-compatible with older server handling: opaque events
	// are acknowledged and delivered, with no dependency on a closed event-kind allowlist.
	HandleAgentEvent(t.Context(), h, nil, dc, protocol.AgentEvent{Type: "agent_event", SessionID: "fixture-session", ClientEventID: newUUID(), Kind: "future_fixture_kind", Payload: json.RawMessage(`{}`)})
	if len(dc.send) != 1 || len(sub) != 1 {
		t.Fatal("unknown event kind was rejected")
	}
}

func TestMCPTransientFrameCannotBypassSanitization(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "fixture", send: make(chan []byte, 8)}
	h.Register(dc)
	sub := make(chan []byte, 8)
	h.Subscribe("fixture-session", "fixture-browser", sub)
	raw := []byte(`{"type":"agent_event","session_id":"fixture-session","client_event_id":"fixture-event","kind":"mcp_status","transient":true,"payload":{"engine":"codex","since":"2026-10-16T12:00:00Z","servers":[{"name":"jira","status":"connected","url":"https://private.invalid"}]}}`)
	if !handleDaemonFrame(t.Context(), h, dc, nil, raw, envelope{Type: "agent_event", SessionID: "fixture-session"}) {
		t.Fatal("frame rejected")
	}
	if len(sub) != 1 || len(dc.send) != 1 {
		t.Fatalf("snapshot not persisted path: subscriber=%d ack=%d", len(sub), len(dc.send))
	}
	got := <-sub
	if strings.Contains(string(got), "private.invalid") || strings.Contains(string(got), `"transient":true`) {
		t.Fatalf("unsafe transient fanout %s", got)
	}
}
