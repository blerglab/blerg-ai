package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/gorilla/websocket"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestDatasetMergesBucketsAndDST(t *testing.T) {
	now := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	history := &protocol.MergeHistory{Commits: []protocol.GraphCommit{
		{SHA: fixtureSHA, Date: "2026-11-01T01:30:00-04:00"},
		{SHA: "abcdef0123456789abcdef0123456789abcdef01", Date: "2026-11-01T01:30:00-05:00"},
		{SHA: fixtureSHA, Date: "2026-11-01T01:30:00-04:00"},
	}}
	rows, cause := mergeDatasetRows(history, map[string]string{"bucket": "hour", "timezone": "America/New_York"}, now)
	if cause != "" || len(rows) != 3 || rows[0]["count"] != 1 || rows[1]["count"] != 1 || rows[2]["count"] != 0 {
		t.Fatalf("hour buckets: %+v %s", rows, cause)
	}
	rows, cause = mergeDatasetRows(history, map[string]string{"bucket": "day", "timezone": "America/New_York"}, now)
	if cause != "" || len(rows) != 1 || rows[0]["count"] != 2 || rows[0]["end"] != "2026-11-02T05:00:00Z" {
		t.Fatalf("day buckets: %+v %s", rows, cause)
	}
}

func TestDatasetWorkersSocketDeltaAndRevocation(t *testing.T) {
	fx := newTopicFx(t)
	hub := fx.api.hub
	fx.mux.HandleFunc("/ws/browser", hub.ServeBrowser(fx.pool, allowWSForTest))
	server := httptest.NewServer(fx.mux)
	defer server.Close()
	socket := dialBrowserTestServer(t, server)
	t.Cleanup(func() { _ = socket.Close() })
	readMessageWithDeadline(t, socket)
	id := newUUID()
	q := protocol.DatasetSubscribe{Type: "subscribe_dataset", SessionID: fx.sess, SubscriptionID: id, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.workers", Parameters: map[string]string{}}}
	if err := socket.WriteJSON(q); err != nil {
		t.Fatal(err)
	}
	next := func() protocol.DatasetDelta {
		t.Helper()
		for {
			var d protocol.DatasetDelta
			if err := json.Unmarshal(readMessageWithDeadline(t, socket), &d); err != nil {
				t.Fatal(err)
			}
			if d.Type == "dataset_delta" {
				return d
			}
		}
	}
	first := next()
	if !first.Snapshot || first.Status != "ready" || len(first.Upserts) != 1 || first.Upserts[0]["running"] != float64(0) {
		t.Fatalf("snapshot: %+v", first)
	}
	child := newUUID()
	if err := db.InsertSessionAs(t.Context(), fx.pool, child, topicDaemonID, "running", "/fixture", "example/repo", "Worker", "", db.SessionOrigin{SpawningAccount: topicOwner, ParentSessionID: fx.sess}); err != nil {
		t.Fatal(err)
	}
	hub.invalidateDatasetSession(t.Context(), fx.pool, fx.sess, false)
	delta := next()
	if delta.Snapshot || delta.Epoch != first.Epoch || delta.BaseSequence != first.Sequence || len(delta.Upserts) != 1 || delta.Upserts[0]["running"] != float64(1) {
		t.Fatalf("delta: %+v", delta)
	}
	// The socket fixture is account-less, so a private parent is access removed.
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET private=true WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	hub.invalidateDatasetSession(t.Context(), fx.pool, fx.sess, false)
	removed := next()
	if removed.Status != "access_removed" || !removed.Snapshot || len(removed.Upserts) != 0 {
		t.Fatalf("revocation: %+v", removed)
	}
}

func TestDatasetCatalogRejectsUnknownParameters(t *testing.T) {
	for _, d := range []protocol.DatasetReference{
		{Kind: "dataset", Name: "sql", Parameters: map[string]string{}},
		{Kind: "dataset", Name: "blerg.workers", Parameters: map[string]string{"token": "private-canary"}},
		{Kind: "dataset", Name: "blerg.cards.merged", Parameters: map[string]string{"bucket": "day", "timezone": "UTC", "release": "0.6.0"}},
	} {
		if d.Valid() {
			t.Fatalf("invalid reference accepted")
		}
	}
}

func TestDatasetUsageAuthorizationAndUnknown(t *testing.T) {
	fx := newCommitFx(t)
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.engine.usage", Parameters: map[string]string{"engine": "codex"}}}
	var history *protocol.MergeHistory
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "codex", FetchedAt: time.Now().UTC(), Windows: []protocol.UsageWindow{{ID: "7d", UsedPercent: 0}}})
	rows, _, cause := resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 1 || rows[0]["percent"] != float64(0) {
		t.Fatalf("observed zero: %+v %s", rows, cause)
	}
	rows, _, cause = resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOther, q, &history, false)
	if cause == "" || len(rows) != 0 {
		t.Fatal("foreign viewer read owner usage")
	}
	fx.dc.SetUsageLimits(protocol.UsageLimits{Provider: "codex"})
	rows, _, cause = resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "usage_unknown" || len(rows) != 0 {
		t.Fatal("missing usage became zero")
	}
	var audits int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM live_dataset_attempts WHERE session_id=$1 AND finished_at IS NOT NULL`, fx.sess).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("audited attempts: %d %v", audits, err)
	}
}

func TestDatasetWorkerVisibilityBeforeAggregation(t *testing.T) {
	fx := newTopicFx(t)
	for _, private := range []bool{false, true} {
		if err := db.InsertSessionAs(t.Context(), fx.pool, newUUID(), topicDaemonID, "running", "/fixture", "example/repo", "Worker", "", db.SessionOrigin{SpawningAccount: topicOwner, ParentSessionID: fx.sess, Private: private}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InsertSessionAs(t.Context(), fx.pool, newUUID(), topicDaemonID, "running", "/fixture", "example/other", "Other workspace worker", "", db.SessionOrigin{SpawningAccount: topicOwner, ParentSessionID: fx.ended}); err != nil {
		t.Fatal(err)
	}
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.workers", Parameters: map[string]string{}}}
	var history *protocol.MergeHistory
	rows, _, cause := resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOther, q, &history, false)
	if cause != "" || len(rows) != 1 || rows[0]["running"] != 1 {
		t.Fatalf("hidden worker leaked in aggregate: %+v %s", rows, cause)
	}
	rows, _, cause = resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 1 || rows[0]["running"] != 2 {
		t.Fatalf("owner aggregate: %+v %s", rows, cause)
	}
}

func TestDatasetDiagnosticsAreBoundedAndRedacted(t *testing.T) {
	var logs bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = old }()
	bc := &BrowserConn{}
	handleDatasetCommand(NewHub(), bc, nil, []byte(`{"type":"subscribe_dataset","subscription_id":"private-canary","data":{"token":"private-canary"}}`))
	if !strings.Contains(logs.String(), "invalid_subscription") || strings.Contains(logs.String(), "private-canary") {
		t.Fatalf("unsafe diagnostic: %s", logs.String())
	}
}

func TestDatasetQueuedDeliveryPurgesRevokedAuthority(t *testing.T) {
	fx := newTopicFx(t)
	id := newUUID()
	bc := &BrowserConn{AccountID: topicOwner, datasetSubs: map[string]*datasetSubscription{id: {reference: protocol.DatasetSubscribe{Data: protocol.DatasetReference{Name: "blerg.workers"}}}}}
	delta := protocol.DatasetDelta{Type: "dataset_delta", SessionID: fx.privSess, SubscriptionID: id, Status: "ready", Upserts: []protocol.DatasetRow{{"key": "workers", "running": 42}}}
	raw, _ := json.Marshal(delta)
	safe, ok := fx.api.hub.filterDatasetDelivery(t.Context(), bc, fx.pool, raw)
	var result protocol.DatasetDelta
	if err := json.Unmarshal(safe, &result); err != nil {
		t.Fatal(err)
	}
	if !ok || result.Status != "access_removed" || len(result.Upserts) != 0 {
		t.Fatalf("queued row survived revocation: %+v", result)
	}
}

func TestDatasetMergeOwnerRejectedBeforeDispatch(t *testing.T) {
	fx := newCommitFx(t)
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.cards.merged", Parameters: map[string]string{"bucket": "day", "timezone": "UTC"}}}
	var history *protocol.MergeHistory
	rows, _, cause := resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOther, q, &history, true)
	if cause != "access_removed" || len(rows) != 0 {
		t.Fatalf("foreign Git read: %s", cause)
	}
	select {
	case <-fx.dc.send:
		t.Fatal("foreign Git dispatch")
	default:
	}
}

func TestDatasetRejectsQueuedOldEpoch(t *testing.T) {
	id := newUUID()
	bc := &BrowserConn{datasetSubs: map[string]*datasetSubscription{id: {epoch: "new"}}}
	raw, _ := json.Marshal(protocol.DatasetDelta{Type: "dataset_delta", SubscriptionID: id, Epoch: "old", Status: "ready", Upserts: []protocol.DatasetRow{{"key": "old", "count": 3}}})
	if _, ok := NewHub().filterDatasetDelivery(t.Context(), bc, nil, raw); ok {
		t.Fatal("superseded source generation delivered")
	}
}

func TestDatasetSixCardsReceiveReadySnapshots(t *testing.T) {
	fx := newTopicFx(t)
	hub := fx.api.hub

	fx.mux.HandleFunc("/ws/browser", hub.ServeBrowser(fx.pool, fx.api.AuthorizeBrowserWS))
	server := httptest.NewServer(fx.mux)
	defer server.Close()
	var sockets []*websocket.Conn
	for _, account := range []string{topicOwner, topicOther} {
		dialer := websocket.Dialer{Subprotocols: []string{"bearer", fx.token(account, "human")}}
		socket, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/browser", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = socket.Close() })
		sockets = append(sockets, socket)
		readMessageWithDeadline(t, socket)
		for i := 0; i < 6; i++ {
			if err := socket.WriteJSON(protocol.DatasetSubscribe{Type: "subscribe_dataset", SessionID: fx.sess, SubscriptionID: newUUID(), Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.workers", Parameters: map[string]string{}}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, socket := range sockets {
		seen := map[string]bool{}
		for len(seen) < 6 {
			var delta protocol.DatasetDelta
			if err := json.Unmarshal(readMessageWithDeadline(t, socket), &delta); err != nil {
				t.Fatal(err)
			}
			if delta.Type != "dataset_delta" {
				continue
			}
			if delta.Status != "ready" {
				t.Fatalf("six-card dashboard source failure: %s", delta.Cause)
			}
			seen[delta.SubscriptionID] = true
		}
	}
	var audits int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM live_dataset_attempts WHERE session_id=$1`, fx.sess).Scan(&audits); err != nil || audits != 12 {
		t.Fatalf("subscription audits=%d: %v", audits, err)
	}
	hub.mu.RLock()
	var subs []*datasetSubscription
	for _, browser := range hub.browsers {
		browser.subMu.Lock()
		for _, sub := range browser.datasetSubs {
			subs = append(subs, sub)
		}
		browser.subMu.Unlock()
	}
	hub.mu.RUnlock()
	for _, socket := range sockets {
		_ = socket.Close()
	}
	for _, sub := range subs {
		select {
		case <-sub.done:
		case <-time.After(5 * time.Second):
			t.Fatal("subscription did not finish")
		}
	}
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM live_dataset_attempts WHERE session_id=$1 AND outcome='unsubscribed' AND finished_at IS NOT NULL`, fx.sess).Scan(&audits); err != nil || audits != 12 {
		t.Fatalf("unsubscribe audits=%d: %v", audits, err)
	}

}

func TestDatasetTriggersIgnoreUnrelatedBroadcastsAndWorkspaces(t *testing.T) {
	fx := newTopicFx(t)
	h := fx.api.hub
	scope, release := h.datasets.watch(datasetKey{"blerg.workers", fx.sess})
	defer release()
	other, releaseOther := h.datasets.watch(datasetKey{"blerg.workers", fx.ended})
	defer releaseOther()
	h.BroadcastJSON(protocol.SessionOutput{Type: "session_output", SessionID: fx.sess, Data: "unrelated"})
	if scope.revision.Load() != 0 || other.revision.Load() != 0 {
		t.Fatal("transcript traffic refreshed datasets")
	}
	h.BroadcastJSON(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: fx.sess, Status: "running"})
	if scope.revision.Load() != 1 || other.revision.Load() != 0 {
		t.Fatal("lifecycle change crossed workspace scope")
	}
	usage, stop := h.datasets.watch(datasetKey{"blerg.engine.usage", "desktop-a"})
	defer stop()
	h.datasets.invalidate("blerg.engine.usage", "desktop-b")
	if usage.revision.Load() != 0 {
		t.Fatal("other desktop usage invalidated this source")
	}
}

func TestDatasetCachedMergeObservationDoesNotAdvance(t *testing.T) {
	// Timestamp belongs to the actual Git read, not repeated aggregation.
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	h := &protocol.MergeHistory{ObservedAt: at, Commits: []protocol.GraphCommit{}}
	_, cause := mergeDatasetRows(h, map[string]string{"timezone": "UTC", "bucket": "day"}, at.Add(time.Hour))
	if cause != "" || !h.ObservedAt.Equal(at) {
		t.Fatal("cached history acquired a new observation time")
	}
}
