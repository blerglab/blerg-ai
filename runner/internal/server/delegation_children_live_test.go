package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func childrenLiveGet(t *testing.T, fx *delegFx, viewer, etag string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.parent+"/live", nil)
	r.SetPathValue("id", fx.parent)
	r.Header.Set("Authorization", "Bearer "+fx.human(viewer))
	r.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	fx.api.HandleGetLive(w, r)
	return w
}

func TestChildrenLiveNotificationThrottlesAndScopesPrivateChild(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true})
	fx.api.children.throttle = 100 * time.Millisecond
	owner := &BrowserConn{ID: "child-live-owner", AccountID: "owner-1", send: make(chan []byte, 20), subs: map[string]context.CancelFunc{}}
	other := &BrowserConn{ID: "child-live-other", AccountID: "owner-2", send: make(chan []byte, 20), subs: map[string]context.CancelFunc{}}
	fx.hub.RegisterBrowser(owner)
	fx.hub.RegisterBrowser(other)
	for range 5 {
		fx.hub.ChildActivity(child)
	}
	awaitLifecycle(t, func() bool { return len(owner.send) >= 2 })
	if len(owner.send) != 2 || len(other.send) != 0 {
		t.Fatalf("notification throttle/privacy owner=%d other=%d", len(owner.send), len(other.send))
	}
	for range 2 {
		var msg map[string]string
		if err := json.Unmarshal(<-owner.send, &msg); err != nil {
			t.Fatal(err)
		}
		if msg["type"] != "children_changed" || msg["session_id"] != fx.parent || msg["source_session_id"] != child {
			t.Fatal("notification was not scoped to both parent and triggering child")
		}
	}
}

func TestChildrenLivePersistsTriggerBeforeNotifyingParent(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	viewer := &BrowserConn{ID: "child-event-viewer", AccountID: "owner-1", send: make(chan []byte, 20)}
	fx.hub.RegisterBrowser(viewer)
	before := childrenLiveGet(t, fx, "owner-1", "").Header().Get("ETag")
	raw, err := json.Marshal(turnDonePayload("fixture", 12, 3, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	HandleAgentEvent(t.Context(), fx.hub, fx.pool, fx.dc, protocol.AgentEvent{Type: "agent_event", SessionID: child, ClientEventID: newUUID(), Kind: "turn_done", Payload: raw})
	found := false
	for len(viewer.send) > 0 {
		var msg struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(<-viewer.send, &msg); err != nil {
			t.Fatal(err)
		}
		found = found || msg.Type == "children_changed" && msg.SessionID == fx.parent
	}
	if !found {
		t.Fatal("persisted child turn did not notify parent")
	}
	if got := childrenLiveGet(t, fx, "owner-1", before); got.Code != http.StatusOK {
		t.Fatal("child event notification raced its persisted parent ETag invalidation")
	}
}

func TestChildrenLivePricesAndMetadataInvalidateCache(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	seedEvent(t, fx.pool, child, time.Now(), "turn_done", turnDonePayload("fixture-price-change", 10, 0, 0, 0))
	before := childrenLiveGet(t, fx, "owner-1", "").Header().Get("ETag")
	if err := db.UpsertModelPrice(t.Context(), fx.pool, db.ModelPrice{Model: "fixture-price-change", InputPerMTok: 1}); err != nil {
		t.Fatal(err)
	}
	w := childrenLiveGet(t, fx, "owner-1", before)
	if w.Code != http.StatusOK {
		t.Fatal("new price did not invalidate cached child cost")
	}
	before = w.Header().Get("ETag")
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET title='renamed fixture',model='new-model',effort='high' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	w = childrenLiveGet(t, fx, "owner-1", before)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "renamed fixture") {
		t.Fatal("metadata-only change did not invalidate parent children cache")
	}
}

func TestChildrenLiveAndInsightsKeepTotalsSeparate(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	seedEvent(t, fx.pool, fx.parent, time.Now(), "turn_done", turnDonePayload("priced-fixture", 10, 20, 0, 0))
	seedEvent(t, fx.pool, child, time.Now(), "turn_done", turnDonePayload("priced-fixture", 100, 200, 0, 0))
	if err := db.UpsertModelPrice(t.Context(), fx.pool, db.ModelPrice{Model: "priced-fixture", InputPerMTok: 1, OutputPerMTok: 2}); err != nil {
		t.Fatal(err)
	}
	w := childrenLiveGet(t, fx, "owner-1", "")
	var live struct {
		Children []struct {
			ID      string   `json:"id"`
			Tokens  *int64   `json:"tokens"`
			CostUSD *float64 `json:"cost_usd"`
		} `json:"children"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(live.Children) != 1 || live.Children[0].ID != child || live.Children[0].Tokens == nil || *live.Children[0].Tokens != 300 || live.Children[0].CostUSD == nil {
		t.Fatalf("live children missing usage: code=%d body=%s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.parent+"/insights", nil)
	r.SetPathValue("id", fx.parent)
	r.Header.Set("Authorization", "Bearer "+fx.human("owner-1"))
	w = httptest.NewRecorder()
	fx.api.HandleGetSessionInsights(w, r)
	var insight struct {
		Tokens struct {
			Totals        tokenCounts  `json:"totals"`
			ChildrenTotal *tokenCounts `json:"children_total"`
		} `json:"tokens"`
		StartedSessions struct {
			Count int `json:"count"`
		} `json:"started_sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &insight); err != nil {
		t.Fatal(err)
	}
	var rawInsight map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rawInsight); err != nil {
		t.Fatal(err)
	}
	started, ok := rawInsight["started_sessions"].(map[string]any)
	if !ok {
		t.Fatal("started_sessions missing lowercase wire key")
	}
	entries, ok := started["sessions"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatal("started sessions omitted lowercase sessions key")
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatal("invalid child row")
	}
	for _, key := range []string{"id", "title", "engine", "model", "tokens"} {
		if _, ok := entry[key]; !ok {
			t.Fatalf("missing lowercase child wire key %s", key)
		}
	}
	if w.Code != http.StatusOK || insight.Tokens.Totals.Input != 10 || insight.Tokens.ChildrenTotal == nil || insight.Tokens.ChildrenTotal.Input != 100 || insight.StartedSessions.Count != 1 {
		t.Fatalf("insights folded child into parent or omitted rollup: %s", w.Body.String())
	}
}

func TestChildrenLiveETagTracksEveryChildNotOnlyLargestLocalSequence(t *testing.T) {
	fx := newDelegFx(t)
	first, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	second, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	for range 4 {
		seedEvent(t, fx.pool, first, time.Now(), "turn_done", turnDonePayload("fixture", 1, 0, 0, 0))
	}
	w := childrenLiveGet(t, fx, "owner-1", "")
	before := w.Header().Get("ETag")
	if got := childrenLiveGet(t, fx, "owner-1", before); got.Code != http.StatusNotModified {
		t.Fatalf("unchanged child state did not cache: %d", got.Code)
	}
	seedEvent(t, fx.pool, second, time.Now(), "turn_done", turnDonePayload("fixture", 10, 0, 0, 0))
	after := childrenLiveGet(t, fx, "owner-1", before)
	if after.Code != http.StatusOK || after.Header().Get("ETag") == before {
		t.Fatal("lower-sequence child's turn failed to invalidate parent live ETag")
	}
	if _, err := db.CreateMessage(t.Context(), fx.pool, second, "ask", "fixture ask"); err != nil {
		t.Fatal(err)
	}
	if got := childrenLiveGet(t, fx, "owner-1", after.Header().Get("ETag")); got.Code != http.StatusOK {
		t.Fatal("child ask without event failed to invalidate parent live ETag")
	}
}

func TestChildrenLivePrivacyAndTokenGating(t *testing.T) {
	fx := newDelegFx(t)
	visible, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	hidden, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true, title: "hidden-fixture-title"})
	seedEvent(t, fx.pool, visible, time.Now(), "turn_done", turnDonePayload("unpriced-fixture", 11, 0, 0, 0))
	w := childrenLiveGet(t, fx, "owner-2", "")
	if strings.Contains(w.Body.String(), hidden) || strings.Contains(w.Body.String(), "hidden-fixture-title") {
		t.Fatal("public parent exposed a private child")
	}
	var body struct {
		Children []struct {
			Tokens  *int64   `json:"tokens"`
			CostUSD *float64 `json:"cost_usd"`
		} `json:"children"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(body.Children) != 1 || body.Children[0].Tokens != nil || body.Children[0].CostUSD != nil {
		t.Fatalf("foreign viewer's child usage gating: %s", w.Body.String())
	}
	w = childrenLiveGet(t, fx, "owner-1", "")
	if strings.Contains(w.Body.String(), `"cost_usd":`) {
		t.Fatal("unpriced child advertised a cost")
	}
}

func TestChildrenLiveAdminUsageDoesNotBypassPrivateChildVisibility(t *testing.T) {
	fx := newDelegFx(t)
	visible, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	hidden, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true})
	seedEvent(t, fx.pool, visible, time.Now(), "turn_done", turnDonePayload("fixture", 11, 0, 0, 0))
	r := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.parent+"/live", nil)
	r.SetPathValue("id", fx.parent)
	r.Header.Set("Authorization", "Bearer "+fx.coreToken(identity.Claims{Kind: "human", Sub: "owner-2", Sid: testSID, Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap, insightsAdminCap}}))
	w := httptest.NewRecorder()
	fx.api.HandleGetLive(w, r)
	var out struct {
		Children []struct {
			ID     string `json:"id"`
			Tokens *int64 `json:"tokens"`
		} `json:"children"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(out.Children) != 1 || out.Children[0].ID != visible || out.Children[0].Tokens == nil || *out.Children[0].Tokens != 11 || strings.Contains(w.Body.String(), hidden) {
		t.Fatal("admin usage permission failed or widened canonical private-session visibility")
	}
}

func TestChildrenLivePendingRequestsAndOwnerOnlyGrants(t *testing.T) {
	fx := newDelegFx(t)
	request := fx.ask(fx.parent, fx.parentTok, codexBody())
	fx.grant(fx.parent, "codex", "docker", "app", false)
	for _, account := range []string{"owner-1", "owner-2"} {
		w := childrenLiveGet(t, fx, account, "")
		var body struct {
			Requests []struct {
				ID string `json:"request_id"`
			} `json:"delegation_requests"`
			Grants []json.RawMessage `json:"delegation_grants"`
			Counts liveCounts        `json:"counts"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if account == "owner-1" {
			var rawGrant map[string]any
			if len(body.Grants) != 1 {
				t.Fatal("missing owner grant")
			}
			if err := json.Unmarshal(body.Grants[0], &rawGrant); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"id", "engine", "runtime"} {
				if _, ok := rawGrant[key]; !ok {
					t.Fatalf("missing lowercase grant wire key %s", key)
				}
			}
		}
		if len(body.Requests) != 1 || body.Requests[0].ID != request || body.Counts.NeedsYou != 1 || (account == "owner-1") != (len(body.Grants) == 1) {
			t.Fatalf("pending requests / owner grant controls %s: %s", account, w.Body.String())
		}
	}
}

func TestChildrenLiveOwnerChangeCannotExposeUsageOrPartialRollup(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-2", parent: fx.parent})
	seedEvent(t, fx.pool, child, time.Now(), "turn_done", turnDonePayload("fixture", 99, 0, 0, 0))
	r := httptest.NewRequest(http.MethodGet, "/api/sessions/"+fx.parent+"/insights", nil)
	r.SetPathValue("id", fx.parent)
	r.Header.Set("Authorization", "Bearer "+fx.human("owner-1"))
	w := httptest.NewRecorder()
	fx.api.HandleGetSessionInsights(w, r)
	var out struct {
		Tokens struct {
			ChildrenTotal *tokenCounts `json:"children_total"`
		} `json:"tokens"`
		StartedSessions struct {
			Sessions []struct {
				Tokens *int64 `json:"tokens"`
			} `json:"sessions"`
		} `json:"started_sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(out.StartedSessions.Sessions) != 1 || out.StartedSessions.Sessions[0].Tokens != nil || out.Tokens.ChildrenTotal != nil {
		t.Fatal("parent ownership widened child token visibility or advertised an incomplete total")
	}
}

func TestChildrenLiveSessionPayloadCarriesParentID(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	info := sessionRowToInfo(*fx.row(child), "owner-1")
	if info.ParentSessionID == nil || *info.ParentSessionID != fx.parent {
		t.Fatal("session list/initial-state projection omitted parent")
	}
	viewer := &BrowserConn{ID: "child-start-viewer", AccountID: "owner-1", send: make(chan []byte, 20)}
	fx.hub.RegisterBrowser(viewer)
	HandleSessionStarted(t.Context(), fx.hub, fx.pool, fx.dc.ID, protocol.SessionStarted{SessionID: child, Kind: "agent"})
	found := false
	for len(viewer.send) > 0 {
		var msg struct {
			Type    string               `json:"type"`
			Session protocol.SessionInfo `json:"session"`
		}
		if err := json.Unmarshal(<-viewer.send, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == "session_started" && msg.Session.ID == child {
			found = msg.Session.ParentSessionID != nil && *msg.Session.ParentSessionID == fx.parent
		}
	}
	if !found {
		t.Fatal("native started broadcast omitted parent")
	}
}

func TestChildrenLiveParentLookupCacheIsBounded(t *testing.T) {
	var n childrenNotifier
	for i := 0; i < maxChildParentCacheEntries+100; i++ {
		n.rememberParent(strconv.Itoa(i), "")
	}
	if len(n.parentOf) != maxChildParentCacheEntries || len(n.cacheOrder) != maxChildParentCacheEntries {
		t.Fatal("parent lookup cache grew beyond its bound")
	}
	if _, old := n.parentOf["0"]; old {
		t.Fatal("old negative lookup was never evicted")
	}
	n.rememberParent("0", "parent")
	if n.parentOf["0"] != "parent" || len(n.parentOf) != maxChildParentCacheEntries {
		t.Fatal("eviction blocked a fresh authoritative parent result")
	}
}

func TestChildrenLiveThrottleStateEvictsIdleButPreservesPendingParent(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	n := &fx.api.children
	n.last = make(map[string]time.Time)
	n.origin = make(map[string]string)
	n.pending = map[string]bool{"pending-fixture": true}
	n.rememberParent(child, fx.parent)
	old := time.Now().Add(-time.Hour)
	n.last["pending-fixture"], n.origin["pending-fixture"] = old.Add(-time.Hour), "pending-child"
	for i := 1; i < maxChildParentCacheEntries; i++ {
		id := "idle-" + strconv.Itoa(i)
		n.last[id], n.origin[id] = old, "idle-child"
	}
	fx.hub.ChildActivity(child)
	if len(n.last) != maxChildParentCacheEntries || len(n.origin) != maxChildParentCacheEntries || len(n.pending) != 1 {
		t.Fatal("per-parent notification state exceeded its bound")
	}
	if _, exists := n.last[fx.parent]; !exists || !n.pending["pending-fixture"] || n.origin["pending-fixture"] != "pending-child" {
		t.Fatal("capacity eviction lost the new parent or a pending timer's state")
	}
}

func TestChildrenLiveThrottleCapacityPreservesScheduledTimerAndScopesOverflowHint(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true})
	otherParent, _ := fx.session(sessOpts{owner: "owner-1"})
	overflow, _ := fx.session(sessOpts{owner: "owner-1", parent: otherParent, private: true})
	owner := &BrowserConn{ID: "cap-owner", AccountID: "owner-1", send: make(chan []byte, 20)}
	foreign := &BrowserConn{ID: "cap-other", AccountID: "owner-2", send: make(chan []byte, 20)}
	fx.hub.RegisterBrowser(owner)
	fx.hub.RegisterBrowser(foreign)
	n := &fx.api.children
	n.throttle = 500 * time.Millisecond
	for range 5 {
		fx.hub.ChildActivity(child)
	}
	// Keep the real scheduled parent, then fill every other slot with busy
	// entries. No extra timers are created just to manufacture this capacity.
	n.mu.Lock()
	for i := 1; i < maxChildParentCacheEntries; i++ {
		id := "busy-" + strconv.Itoa(i)
		n.last[id], n.origin[id], n.pending[id] = time.Now(), "busy-child", true
	}
	n.mu.Unlock()
	fx.hub.ChildActivity(overflow)
	n.mu.Lock()
	_, retained := n.last[otherParent]
	bounded := len(n.last) == maxChildParentCacheEntries && len(n.origin) == maxChildParentCacheEntries && len(n.pending) <= maxChildParentCacheEntries
	n.mu.Unlock()
	if retained || !bounded {
		t.Fatal("overflow hint retained new parent state or exceeded the capacity")
	}
	awaitLifecycle(t, func() bool { return len(owner.send) >= 3 })
	if len(owner.send) != 3 || len(foreign.send) != 0 {
		t.Fatalf("scheduled/overflow notification loss, duplication or privacy failure: owner=%d other=%d", len(owner.send), len(foreign.send))
	}
	counts := map[string]int{}
	for range 3 {
		var msg map[string]string
		if err := json.Unmarshal(<-owner.send, &msg); err != nil {
			t.Fatal(err)
		}
		counts[msg["session_id"]]++
		if msg["source_session_id"] != child && msg["source_session_id"] != overflow {
			t.Fatal("overflow hint lost its triggering child privacy scope")
		}
	}
	if counts[fx.parent] != 2 || counts[otherParent] != 1 {
		t.Fatal("capacity handling replaced or duplicated the original trailing timer")
	}
}
