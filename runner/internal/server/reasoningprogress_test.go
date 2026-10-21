package server

import (
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLiveReasoningUpdatesRunningChildWithoutPersistingDeltas(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='running' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	before := childrenLiveGet(t, fx, "owner-1", "").Header().Get("ETag")
	send := func(kind string, payload string) {
		raw, _ := json.Marshal(protocol.AgentEvent{Type: "agent_event", SessionID: child, ClientEventID: newUUID(), Kind: kind, Payload: json.RawMessage(payload), Ts: time.Now().UTC().Format(time.RFC3339Nano), Transient: true})
		fx.hub.FanOutAgentEvent(child, 0, raw)
	}
	send("thinking_started", `{}`)
	send("thinking_progress", `{"tokens":37}`)
	rec := childrenLiveGet(t, fx, "owner-1", before)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Thinking… 37 tokens") {
		t.Fatalf("live reasoning = %d %s", rec.Code, rec.Body.String())
	}
	other := childrenLiveGet(t, fx, "owner-2", "")
	if strings.Contains(other.Body.String(), "37 tokens") || !strings.Contains(other.Body.String(), "Thinking…") {
		t.Fatalf("reasoning count privacy: %d %s", other.Code, other.Body.String())
	}
	after := rec.Header().Get("ETag")
	send("thinking_done", `{}`)
	rec = childrenLiveGet(t, fx, "owner-1", after)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Thinking… 37 tokens") {
		t.Fatalf("completed reasoning retained: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReasoningCacheIsBoundedAndRejectsInvalidCounters(t *testing.T) {
	var cache reasoningCache
	send := func(sid string, n int) bool {
		raw, _ := json.Marshal(protocol.AgentEvent{SessionID: sid, Kind: "thinking_progress", Transient: true, Ts: time.Now().UTC().Format(time.RFC3339Nano), Payload: json.RawMessage(fmt.Sprintf(`{"tokens":%d}`, n))})
		return cache.observe(sid, raw)
	}
	for i := range maxReasoningSessions + 20 {
		send(fmt.Sprint(i), i)
	}
	if len(cache.sessions) != maxReasoningSessions {
		t.Fatalf("cache grew to %d", len(cache.sessions))
	}
	if send("invalid", -1) {
		t.Fatal("negative counter accepted")
	}
	if _, ok := cache.sessions["invalid"]; ok {
		t.Fatal("invalid counter allocated state")
	}
}

func reasoningEvent(t *testing.T, sid, kind, payload string, transient bool) []byte {
	t.Helper()
	raw, err := json.Marshal(protocol.AgentEvent{Type: "agent_event", SessionID: sid, ClientEventID: newUUID(), Kind: kind, Payload: json.RawMessage(payload), Ts: time.Now().UTC().Format(time.RFC3339Nano), Transient: transient})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A daemon restart ends whatever its sessions were thinking about: when it registers again, their
// scopes close, so no clock keeps ticking from before the restart.
func TestDaemonReconnectClosesItsSessionsReasoning(t *testing.T) {
	hub := NewHub()
	hub.SetSessionOwner("mine", "daemon-1")
	hub.SetSessionOwner("theirs", "daemon-2")
	for _, sid := range []string{"mine", "theirs"} {
		if !hub.reasoning.observe(sid, reasoningEvent(t, sid, "thinking_started", `{}`, true)) {
			t.Fatalf("%s: scope not opened", sid)
		}
	}
	hub.Register(&DaemonConn{ID: "daemon-1"})
	if _, ok := hub.reasoning.sessions["mine"]; ok {
		t.Fatal("a reconnected daemon's session kept its reasoning scope")
	}
	if _, ok := hub.reasoning.sessions["theirs"]; !ok {
		t.Fatal("another daemon's session lost its reasoning scope")
	}
}

// Events other than thinking ones matter only to a session with an open scope; anything else is
// skipped before it is decoded, and an open scope still closes on them.
func TestReasoningCacheSkipsOtherEventsUntilAScopeIsOpen(t *testing.T) {
	var cache reasoningCache
	if cache.observe("s", reasoningEvent(t, "s", "tool_call", `{"tool":"Read"}`, false)) {
		t.Fatal("a tool call changed a session with no scope")
	}
	if len(cache.sessions) != 0 {
		t.Fatal("state allocated for a session with no scope")
	}
	cache.observe("s", reasoningEvent(t, "s", "thinking_started", `{}`, true))
	if !cache.observe("s", reasoningEvent(t, "s", "tool_call", `{"tool":"Read"}`, false)) {
		t.Fatal("a tool call did not close the open scope")
	}
}

func TestReasoningLeavesAnAskingChildsLine(t *testing.T) {
	children := []liveChild{{ID: "c1", Status: "running", Asking: true, Activity: "Waiting for an answer"}}
	n := 5
	applyReasoning(&livestate.State{}, children, "parent", map[string]map[string]livestate.Reasoning{"c1": {"": {Since: 1, Tokens: &n}}})
	if children[0].Activity != "Waiting for an answer" {
		t.Fatalf("activity = %q, want the ask's line", children[0].Activity)
	}
}
