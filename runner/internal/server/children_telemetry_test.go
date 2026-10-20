package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"strings"
	"testing"
	"time"
)

func telemetryChild(t *testing.T, fx *delegFx) map[string]any {
	t.Helper()
	w := childrenLiveGet(t, fx, "owner-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("live code %d", w.Code)
	}
	var reply struct {
		Children []map[string]any `json:"children"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Children) != 1 {
		t.Fatalf("children %d", len(reply.Children))
	}
	return reply.Children[0]
}
func TestChildTelemetryUsesNativeToolsPendingUsageAndActualIdle(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET engine='hermes',status='running' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	initial := telemetryChild(t, fx)
	if initial["tokens"] != nil || initial["usage_status"] != "pending" {
		t.Fatalf("unreported shown as zero: %+v", initial)
	}
	seedEvent(t, fx.pool, child, time.Now(), "tool_call", map[string]any{"tool": "terminal", "call_id": "native-tool", "input": map[string]string{"command": "go test ./..."}})
	working := telemetryChild(t, fx)
	if working["tool_calls"] != float64(1) || working["activity"] != "running terminal · go test ./..." {
		t.Fatalf("native activity missing: %+v", working)
	}
	seedEvent(t, fx.pool, child, time.Now(), "turn_done", turnDonePayload("fixture-model", 20, 5, 0, 0))
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='idle' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	idle := telemetryChild(t, fx)
	if idle["status"] != "idle" || idle["tokens"] != float64(25) || idle["activity"] == working["activity"] {
		t.Fatalf("idle child still running/empty: %+v", idle)
	}
}
func TestChildUsageProgressReplacesSnapshotsAndDoesNotDoubleCountCompletion(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='running' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	for _, input := range []int{10, 20} {
		seedEvent(t, fx.pool, child, time.Now(), "usage_updated", map[string]any{"scope": "turn", "model": "fixture-model", "usage": map[string]int{"input_tokens": input, "output_tokens": 5, "cache_read_tokens": 0, "cache_write_tokens": 0}})
	}
	active := telemetryChild(t, fx)
	if active["tokens"] != float64(25) || active["usage_status"] != "reported" {
		t.Fatalf("snapshots summed/ignored: %+v", active)
	}
	seedEvent(t, fx.pool, child, time.Now(), "turn_done", turnDonePayload("fixture-model", 20, 5, 0, 0))
	complete := telemetryChild(t, fx)
	if complete["tokens"] != float64(25) {
		t.Fatalf("completion double counted: %+v", complete)
	}
	seedEvent(t, fx.pool, child, time.Now(), "usage_updated", map[string]any{"scope": "turn", "usage": nil})
	next := telemetryChild(t, fx)
	if next["tokens"] != float64(25) || next["usage_status"] != "pending" {
		t.Fatalf("new turn retained snapshot: %+v", next)
	}
}

func TestChildTelemetryPrivacyAndLocalAreObservedNotInvented(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	foreign, _ := fx.session(sessOpts{owner: "owner-2", parent: fx.parent, private: true})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET engine='hermes' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	seedEvent(t, fx.pool, foreign, time.Now(), "tool_call", map[string]any{"tool": "terminal", "input": map[string]string{"command": "PRIVATE_CHILD_FIXTURE"}})
	seedEvent(t, fx.pool, child, time.Now(), "usage_updated", map[string]any{"scope": "turn", "model": "unpriced-local-model", "usage": map[string]int{"input_tokens": 10, "output_tokens": 2, "cache_read_tokens": 0, "cache_write_tokens": 0}})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	fx.dc.SetLocalEngines([]string{"hermes"})
	w := childrenLiveGet(t, fx, "owner-1", "")
	if strings.Contains(w.Body.String(), "PRIVATE_CHILD_FIXTURE") || strings.Contains(w.Body.String(), foreign) {
		t.Fatal("foreign private child telemetry leaked")
	}
	own := telemetryChild(t, fx)
	if own["local"] != true || own["cost_usd"] != nil {
		t.Fatalf("local inferred monetary price: %+v", own)
	}
	other := childrenLiveGet(t, fx, "owner-3", "")
	var reply struct {
		Children []map[string]any `json:"children"`
	}
	if err := json.Unmarshal(other.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Children) != 1 || reply.Children[0]["tokens"] != nil || reply.Children[0]["cost_usd"] != nil || reply.Children[0]["usage_status"] != "hidden" {
		t.Fatalf("usage visibility violated: %s", other.Body.String())
	}
	etag := w.Header().Get("ETag")
	fx.dc.SetLocalEngines(nil)
	changed := childrenLiveGet(t, fx, "owner-1", etag)
	if changed.Code != http.StatusOK {
		t.Fatal("local classification change did not invalidate cache")
	}
}
func TestChildTelemetryResetCannotResurrectAnOldCommand(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	seedEvent(t, fx.pool, child, time.Now(), "tool_call", map[string]any{"tool": "terminal", "call_id": "old-tool", "input": map[string]string{"command": "old command"}})
	seedEvent(t, fx.pool, child, time.Now(), "usage_updated", map[string]any{"scope": "turn", "usage": nil})
	got := telemetryChild(t, fx)
	if strings.Contains(got["activity"].(string), "old command") {
		t.Fatal("restart reset resurrected old tool")
	}
	seedEvent(t, fx.pool, child, time.Now(), "usage_updated", map[string]any{"scope": "turn", "model": "fixture", "usage": map[string]int{"input_tokens": 2, "output_tokens": 1, "cache_read_tokens": 0, "cache_write_tokens": 0}})
	got = telemetryChild(t, fx)
	if strings.Contains(got["activity"].(string), "old command") {
		t.Fatal("reported usage lost restart reset barrier")
	}
}
func TestUsageProgressSanitizedDurableAndNotifiesParentAfterPersist(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	viewer := &BrowserConn{ID: "telemetry-owner", AccountID: "owner-1", send: make(chan []byte, 20)}
	fx.hub.RegisterBrowser(viewer)
	before := childrenLiveGet(t, fx, "owner-1", "").Header().Get("ETag")
	raw := []byte(`{"type":"agent_event","session_id":"` + child + `","client_event_id":"` + newUUID() + `","kind":"usage_updated","transient":true,"payload":{"scope":"turn","usage":{"input_tokens":10,"output_tokens":2,"cache_read_tokens":0,"cache_write_tokens":0},"secret":"PRIVATE_NATIVE_FIXTURE"}}`)
	handleDaemonFrame(t.Context(), fx.hub, fx.dc, fx.pool, raw, envelope{Type: "agent_event"})
	rows, err := db.ListAgentEventsTailKinds(t.Context(), fx.pool, child, []string{protocol.UsageUpdatedKind}, 10)
	if err != nil || len(rows) != 1 || strings.Contains(rows[0].Payload, "PRIVATE_NATIVE_FIXTURE") {
		t.Fatalf("usage bypassed sanitization/durability: %v", err)
	}
	found := false
	for len(viewer.send) > 0 {
		var msg map[string]any
		_ = json.Unmarshal(<-viewer.send, &msg)
		if msg["type"] == "children_changed" {
			found = true
		}
	}
	if !found || childrenLiveGet(t, fx, "owner-1", before).Code != http.StatusOK {
		t.Fatal("reported usage did not invalidate parent after persistence")
	}
	ledger, err := db.ListSessionTokenUsage(t.Context(), fx.pool, child)
	if err != nil || len(ledger) != 0 {
		t.Fatal("nonbilling progress entered completed usage ledger")
	}
}

func TestChildUnknownCompletedUsageDoesNotInventZero(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "idle"})
	seedEvent(t, fx.pool, child, time.Now(), "turn_done", map[string]any{"model": "fixture", "usage_reported": false, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0}})
	got := telemetryChild(t, fx)
	if got["tokens"] != nil || got["cost_usd"] != nil || got["usage_status"] != "pending" {
		t.Fatalf("unreported completion invented zero: %+v", got)
	}
}

func TestChildTelemetryCompletionBetweenSnapshotAndReadUsesOneBoundary(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	seedEvent(t, fx.pool, child, time.Now(), "usage_updated", map[string]any{"scope": "turn", "model": "fixture", "usage": map[string]int{"input_tokens": 20, "output_tokens": 5, "cache_read_tokens": 0, "cache_write_tokens": 0}})
	snapshot, err := fx.api.readChildrenSnapshot(t.Context(), fx.row(fx.parent), "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	seedEvent(t, fx.pool, child, time.Now(), "turn_done", turnDonePayload("fixture", 20, 5, 0, 0))
	captured, ledger, err := fx.api.liveChildren(t.Context(), snapshot.Rows, "owner-1", true, false, nil)
	if err != nil || len(captured) != 1 || captured[0].Tokens == nil || *captured[0].Tokens != 25 || len(ledger) != 0 {
		t.Fatalf("completion raced snapshot: rows=%+v ledger=%+v err=%v", captured, ledger, err)
	}
	fresh, err := fx.api.readChildrenSnapshot(t.Context(), fx.row(fx.parent), "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	captured, ledger, err = fx.api.liveChildren(t.Context(), fresh.Rows, "owner-1", true, false, nil)
	if err != nil || *captured[0].Tokens != 25 || len(ledger) != 1 {
		t.Fatalf("completion double-counted or missing: rows=%+v ledger=%+v err=%v", captured, ledger, err)
	}
}
