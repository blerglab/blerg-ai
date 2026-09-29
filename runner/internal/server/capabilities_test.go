package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A capabilities event is re-sanitized on the way in: whatever the daemon
// sent, only the checked form is fanned out (and persisted).
func TestHandleAgentEventSanitizesCapabilities(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "d1", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("s1", "d1")
	sub := make(chan []byte, 8)
	h.Subscribe("s1", "b1", sub)

	payload := `{"engine":"claude","groups":[
	  {"id":"bogus","label":"Nope","items":[{"name":"x"}]},
	  {"id":"skills","label":"Skills","items":[{"name":"evil\u202ename","status":"<b>"}]}]}`
	HandleAgentEvent(context.Background(), h, nil, dc, protocol.AgentEvent{
		Type: "agent_event", SessionID: "s1", ClientEventID: "ce-1", Kind: protocol.CapabilitiesKind,
		Payload: json.RawMessage(payload),
	})
	<-dc.send // ack
	var got protocol.AgentEvent
	if err := json.Unmarshal(<-sub, &got); err != nil {
		t.Fatal(err)
	}
	var p protocol.CapabilitiesPayload
	if err := json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Groups) != 1 || p.Groups[0].ID != "skills" {
		t.Fatalf("groups = %+v", p.Groups)
	}
	if it := p.Groups[0].Items[0]; it.Name != "evilname" || it.Status != "" {
		t.Errorf("item = %+v", it)
	}

	// A payload of the wrong shape is dropped, but acked so it isn't resent.
	HandleAgentEvent(context.Background(), h, nil, dc, protocol.AgentEvent{
		Type: "agent_event", SessionID: "s1", ClientEventID: "ce-2", Kind: protocol.CapabilitiesKind,
		Payload: json.RawMessage(`{"groups":"nope"}`),
	})
	select {
	case raw := <-dc.send:
		if !strings.Contains(string(raw), "ce-2") {
			t.Errorf("ack = %s", raw)
		}
	default:
		t.Error("malformed capabilities must still be acked")
	}
	select {
	case raw := <-sub:
		t.Errorf("malformed capabilities fanned out: %s", raw)
	default:
	}
}

// Capabilities persist with the transcript, appear in the contract's events
// window (and so MCP get_events / SSE), and GET …/capabilities serves the
// latest. Requires TEST_DATABASE_URL; skips otherwise.
func TestCapabilitiesPersistReplayAndLatest(t *testing.T) {
	api, _, dc, pool := spawnTokenFixture(t)
	ctx := context.Background()
	id := postSpawnSessionID(t, api, dc.ID, "daemon", "agent")
	tok := enableBrowserAuth(t, api)("session.start")

	get := func(auth bool) (int, map[string]json.RawMessage) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/capabilities", nil)
		req.SetPathValue("id", id)
		if auth {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		api.HandleGetCapabilities(rec, req)
		var body map[string]json.RawMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	if code, _ := get(false); code != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", code)
	}
	if code, body := get(true); code != http.StatusOK || string(body["capabilities"]) != "null" {
		t.Errorf("before any report: %d %s", code, body["capabilities"])
	}

	for _, tools := range []string{`[{"name":"Bash"}]`, `[{"name":"Bash"},{"name":"Read"}]`} {
		HandleAgentEvent(ctx, api.hub, pool, dc, protocol.AgentEvent{
			Type: "agent_event", SessionID: id, ClientEventID: newUUID(), Kind: protocol.CapabilitiesKind,
			Payload: json.RawMessage(`{"engine":"claude","groups":[{"id":"tools","label":"Tools","items":` + tools + `}]}`),
		})
	}

	code, body := get(true)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	var latest protocol.CapabilitiesPayload
	if err := json.Unmarshal(body["capabilities"], &latest); err != nil {
		t.Fatalf("decode: %v (%s)", err, body["capabilities"])
	}
	if len(latest.Groups) != 1 || len(latest.Groups[0].Items) != 2 {
		t.Errorf("latest = %+v, want the second report", latest)
	}
	if len(body["seq"]) == 0 || len(body["ts"]) == 0 {
		t.Errorf("seq/ts missing: %v", body)
	}

	rows, err := db.ListAgentEvents(ctx, pool, id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Kind == protocol.CapabilitiesKind {
			n++
		}
	}
	if n != 2 {
		t.Errorf("persisted capabilities events = %d, want 2", n)
	}
}
