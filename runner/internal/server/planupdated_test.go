package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestPlanUpdatedTransientCannotBypassValidation(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "fixture", send: make(chan []byte, 8)}
	h.Register(dc)
	sub := make(chan []byte, 8)
	h.Subscribe("fixture-session", "fixture-browser", sub)
	raw := []byte(`{"type":"agent_event","session_id":"fixture-session","client_event_id":"fixture-event","kind":"plan_updated","transient":true,"payload":{"items":[{"text":"Review","status":"in_progress"}],"explanation":"fixture-private","token":"fixture-private"}}`)
	if !handleDaemonFrame(t.Context(), h, dc, nil, raw, envelope{Type: "agent_event", SessionID: "fixture-session"}) {
		t.Fatal("frame refused")
	}
	if len(sub) != 1 || len(dc.send) != 1 {
		t.Fatalf("not durable validated path: subscriber=%d ack=%d", len(sub), len(dc.send))
	}
	got := <-sub
	if strings.Contains(string(got), "fixture-private") || strings.Contains(string(got), `"transient":true`) {
		t.Fatalf("raw native data escaped: %s", got)
	}
}
func TestPlanUpdatedMalformedAndChildSnapshotsAreAcknowledgedAndDropped(t *testing.T) {
	for _, raw := range []string{`{}`, `{"items":null}`, `{"items":[{"text":"wrong","status":"surprise"}]}`, `{"items":[],"parent_call_id":"child"}`, `{"items":[],"child_id":"child"}`} {
		h := NewHub()
		dc := &DaemonConn{ID: "fixture", send: make(chan []byte, 2)}
		h.Register(dc)
		sub := make(chan []byte, 2)
		h.Subscribe("fixture-session", "fixture-browser", sub)
		HandleAgentEvent(t.Context(), h, nil, dc, protocol.AgentEvent{Type: "agent_event", SessionID: "fixture-session", ClientEventID: newUUID(), Kind: protocol.PlanUpdatedKind, Payload: json.RawMessage(raw)})
		if len(dc.send) != 1 || len(sub) != 0 {
			t.Fatalf("invalid snapshot not dropped: %s", raw)
		}
	}
}
func TestLiveCodexPlanReplaysAndClearsAndRemainsPrivate(t *testing.T) {
	fx := newLiveFx(t)
	dc := &DaemonConn{ID: "fixture", send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	appendPlan := func(payload string) {
		t.Helper()
		HandleAgentEvent(t.Context(), fx.api.hub, fx.pool, dc, protocol.AgentEvent{Type: "agent_event", SessionID: fx.sess, ClientEventID: newUUID(), Kind: protocol.PlanUpdatedKind, Payload: json.RawMessage(payload)})
		<-dc.send
	}
	appendPlan(`{"items":[{"text":"Write tests","status":"in_progress"},{"text":"Ship","status":"pending"}],"explanation":"fixture-private"}`)
	rec := fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	var body struct {
		Plan *struct {
			Items []protocol.PlanItem `json:"items"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || body.Plan == nil || len(body.Plan.Items) != 2 || body.Plan.Items[0].Status != "in_progress" {
		t.Fatalf("plan not replayed: %d %s", rec.Code, rec.Body)
	}
	appendPlan(`{"items":null}`)
	rec = fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	if !strings.Contains(rec.Body.String(), "Write tests") {
		t.Fatalf("invalid update erased plan: %s", rec.Body)
	}
	appendPlan(`{"items":[]}`)
	rec = fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
	body.Plan = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || body.Plan != nil {
		t.Fatalf("explicit clear retained plan: %s", rec.Body)
	}
	fx.event(fx.privSess, protocol.PlanUpdatedKind, protocol.PlanUpdatedPayload{Items: []protocol.PlanItem{{Text: "private plan fixture", Status: "pending"}}})
	rec = fx.getLive(fx.privSess, fx.token(topicOwner, "human"), "")
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "private plan fixture") {
		t.Fatalf("private plan escaped: %d %s", rec.Code, rec.Body)
	}
}
