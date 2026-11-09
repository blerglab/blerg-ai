package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
	"github.com/blerglab/blerg-ai/runner/internal/livepoll"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const mcpFixtureOwner = "00000000-0000-4000-8000-000000000003"
const mcpFixtureBinding = "00000000-0000-4000-8000-000000000514"

func setupMCPDataset(t *testing.T, fx *topicFx) (ld.Revision, *atomic.Bool) {
	t.Helper()
	definition, hash := mcpgw.CanonicalTool("quotes", "Simulated market quotes", json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`))
	r := ld.Revision{Scope: ld.Scope{Installation: "00000000-0000-4000-8000-000000000001", Runner: "00000000-0000-4000-8000-000000000002", Owner: mcpFixtureOwner, Binding: mcpFixtureBinding, Connection: "00000000-0000-4000-8000-000000000005", Tool: "quotes", Hash: hash, Revision: 1, OwnerEpoch: 1, ConnectionGeneration: 1, AuthorityEpoch: 1}, ToolDefinition: definition, Arguments: json.RawMessage(`{}`), Mapping: livemcp.Mapping{RowsPath: "/quotes", Fields: []livemcp.Field{{Name: "value", Path: "/close", Type: "number"}}, Keys: []string{"value"}}, IntervalSeconds: 60, ExpiresAt: time.Now().UTC().Add(time.Hour), OwnerOnly: true}
	r, configuration, err := r.Canonical(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = (livepoll.Attempts{Pool: fx.pool}).Activate(t.Context(), r, fx.sess); err != nil {
		t.Fatal(err)
	}
	if _, err = fx.pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET last_payload='[{"key":"quote","value":185.25}]',payload_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	denied := &atomic.Bool{}
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var scope ld.Scope
		if denied.Load() || json.NewDecoder(request.Body).Decode(&scope) != nil || scope != r.Scope {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(ld.Snapshot{Grant: ld.Grant{Scope: r.Scope, Configuration: configuration, Arguments: r.Arguments, ExpiresAt: r.ExpiresAt, CoreTime: time.Now(), UnsafeHostOptIn: true}})
	}))
	t.Cleanup(authority.Close)
	fx.api.SetLiveDataAuthority(&ld.ServiceClient{URL: authority.URL, HTTP: &http.Client{Transport: http.DefaultTransport}})
	fx.api.hub.liveDataPollingEnabled.Store(true)
	return r, denied
}
func TestDatasetMCPOwnerOnlyAndDeliveryRevocation(t *testing.T) {
	fx := newTopicFx(t)
	r, denied := setupMCPDataset(t, fx)
	base := time.Now()
	var offset atomic.Int64
	fx.api.hub.mcpAuthority.now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.mcp", Parameters: map[string]string{"binding": r.Scope.Binding}}}
	if _, err := auditDatasetSubscription(t.Context(), fx.pool, mcpFixtureOwner, q); err != nil {
		t.Fatal("MCP subscription audit catalog missing", err)
	}
	if rows, _, cause := mcpDatasetRows(t.Context(), fx.api.hub, fx.pool, topicOther, q); cause != "access_removed" || rows != nil {
		t.Fatal("shared workspace leaked private rows")
	}
	if rows, _, cause := mcpDatasetRows(t.Context(), fx.api.hub, fx.pool, mcpFixtureOwner, q); cause != "" || len(rows) != 1 || rows[0]["value"] != 185.25 {
		t.Fatal("owner sample unavailable", cause)
	}
	id := newUUID()
	epoch := newUUID()
	bc := &BrowserConn{AccountID: mcpFixtureOwner, datasetSubs: map[string]*datasetSubscription{id: {reference: q, epoch: epoch, deliveryFences: map[int64]datasetDeliveryFence{1: {liveScope: r.Scope, validUntil: time.Now().Add(time.Hour)}}}}}
	raw, _ := json.Marshal(protocol.DatasetDelta{Type: "dataset_delta", SessionID: fx.sess, SubscriptionID: id, Epoch: epoch, Sequence: 1, Snapshot: true, Status: "ready", Upserts: []protocol.DatasetRow{{"key": "quote", "value": 185.25}}})
	denied.Store(true)
	offset.Store(int64(time.Second))
	safe, ok := fx.api.hub.filterDatasetDelivery(t.Context(), bc, fx.pool, raw)
	if !ok || strings.Contains(string(safe), "185.25") || !strings.Contains(string(safe), "access_removed") {
		t.Fatal("queued frame leaked revoked rows", string(safe))
	}
	var retained bool
	if err := fx.pool.QueryRow(t.Context(), `SELECT last_payload IS NOT NULL FROM live_mcp_binding_state`).Scan(&retained); err != nil || retained {
		t.Fatal("revoked sample retained", err)
	}
}

type mcpCountingTransport struct {
	base  http.RoundTripper
	calls atomic.Int64
}

func (c *mcpCountingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return c.base.RoundTrip(r)
}
func TestDatasetMCPAuthoritySharedAcrossSubscribers(t *testing.T) {
	fx := newTopicFx(t)
	r, _ := setupMCPDataset(t, fx)
	base := time.Now()
	var offset atomic.Int64
	fx.api.hub.mcpAuthority.now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	counter := &mcpCountingTransport{base: fx.api.hub.liveDataAuthority.HTTP.Transport}
	fx.api.hub.liveDataAuthority.HTTP.Transport = counter
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.mcp", Parameters: map[string]string{"binding": r.Scope.Binding}}}
	var subscribers sync.WaitGroup
	for range 20 {
		subscribers.Go(func() {
			if rows, _, cause := mcpDatasetRows(t.Context(), fx.api.hub, fx.pool, mcpFixtureOwner, q); cause != "" || len(rows) != 1 {
				t.Error("owner subscription failed", cause)
			}
		})
	}
	subscribers.Wait()
	if n := counter.calls.Load(); n != 1 {
		t.Fatal("subscribers did not share one authority result", n)
	}
	if _, _, cause := mcpDatasetRows(t.Context(), fx.api.hub, fx.pool, topicOther, q); cause != "access_removed" {
		t.Fatal("shared cache authorized another account")
	}
	if n := counter.calls.Load(); n != 1 {
		t.Fatal("unauthorized subscriber reached Core", n)
	}
	version := mcpDatasetVersion(t.Context(), fx.pool, mcpFixtureOwner, q)
	if _, e := fx.pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET sample_sequence=sample_sequence+1`); e != nil {
		t.Fatal(e)
	}
	if _, _, cause := mcpDatasetRows(t.Context(), fx.api.hub, fx.pool, mcpFixtureOwner, q); cause != "" {
		t.Fatal(cause)
	}
	if version == mcpDatasetVersion(t.Context(), fx.pool, mcpFixtureOwner, q) {
		t.Fatal("local sequence polling missed a new sample")
	}
	if n := counter.calls.Load(); n != 2 {
		t.Fatal("new sample reused an earlier authority decision", n)
	}
	offset.Store(int64(time.Second))
	if _, _, cause := mcpDatasetRows(t.Context(), fx.api.hub, fx.pool, mcpFixtureOwner, q); cause != "" {
		t.Fatal(cause)
	}
	if n := counter.calls.Load(); n != 3 {
		t.Fatal("authority cache outlived its one-second bound", n)
	}
	id, epoch := newUUID(), newUUID()
	bc := &BrowserConn{AccountID: mcpFixtureOwner, datasetSubs: map[string]*datasetSubscription{id: {reference: q, epoch: epoch, deliveryFences: map[int64]datasetDeliveryFence{1: {liveScope: r.Scope, validUntil: time.Now().Add(time.Hour)}}}}}
	raw, _ := json.Marshal(protocol.DatasetDelta{Type: "dataset_delta", SessionID: q.SessionID, SubscriptionID: id, Epoch: epoch, Sequence: 1, Snapshot: true, Status: "ready", Upserts: []protocol.DatasetRow{{"key": "quote", "value": 185.25}}})
	if _, e := fx.pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET enabled=false,cause='authority_revoked',last_payload=NULL,payload_at=NULL`); e != nil {
		t.Fatal(e)
	}
	if safe, ok := fx.api.hub.filterDatasetDelivery(t.Context(), bc, fx.pool, raw); !ok || strings.Contains(string(safe), "185.25") || !strings.Contains(string(safe), "access_removed") {
		t.Fatal("warm authority cache leaked queued rows after a known local purge", string(safe))
	}
	if rows, _, cause := mcpDatasetRows(t.Context(), fx.api.hub, fx.pool, mcpFixtureOwner, q); cause != "access_removed" || rows != nil {
		t.Fatal("cached authority bypassed a local purge", cause)
	}

}

func TestDatasetMCPCachedAuthorityCannotLowerEpoch(t *testing.T) {
	fx := newTopicFx(t)
	r, _ := setupMCPDataset(t, fx)
	base := time.Now()
	fx.api.hub.mcpAuthority.now = func() time.Time { return base }
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.mcp", Parameters: map[string]string{"binding": r.Scope.Binding}}}
	if _, _, cause := mcpDatasetRows(t.Context(), fx.api.hub, fx.pool, mcpFixtureOwner, q); cause != "" {
		t.Fatal(cause)
	}
	newer := r.Scope
	newer.AuthorityEpoch++
	if e := (livepoll.Attempts{Pool: fx.pool}).ObserveEpoch(t.Context(), newer); e != nil {
		t.Fatal(e)
	}
	if rows, _, cause := mcpDatasetRows(t.Context(), fx.api.hub, fx.pool, mcpFixtureOwner, q); cause != "access_removed" || rows != nil {
		t.Fatal("read cache bypassed a newer durable authority epoch", cause)
	}
}

func TestDatasetMCPSubscriptionChecksOnlyChangedSamplesAndPeriodicAuthority(t *testing.T) {
	fx := newTopicFx(t)
	r, _ := setupMCPDataset(t, fx)
	counter := &mcpCountingTransport{base: fx.api.hub.liveDataAuthority.HTTP.Transport}
	fx.api.hub.liveDataAuthority.HTTP.Transport = counter
	base := time.Now()
	var offset atomic.Int64
	now := func() time.Time { return base.Add(time.Duration(offset.Load())) }
	fx.api.hub.mcpAuthority.now = now
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	ticks := make(chan time.Time)
	q := protocol.DatasetSubscribe{SessionID: fx.sess, SubscriptionID: newUUID(), Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.mcp", Parameters: map[string]string{"binding": r.Scope.Binding}}}
	sub := &datasetSubscription{now: now, ticks: ticks, cancel: cancel, heartbeat: make(chan struct{}, 1), reference: q, epoch: newUUID(), done: make(chan struct{})}
	bc := &BrowserConn{AccountID: mcpFixtureOwner, ctx: ctx, send: make(chan []byte, 16), datasetSubs: map[string]*datasetSubscription{q.SubscriptionID: sub}}
	go runDatasetSubscription(ctx, fx.api.hub, bc, fx.pool, q, sub)
	defer func() { cancel(); <-sub.done }()
	select {
	case <-bc.send:
	case <-ctx.Done():
		t.Fatal("subscription did not emit its initial snapshot")
	}
	if n := counter.calls.Load(); n != 1 {
		t.Fatal("initial demand and collection did not share authority", n)
	}
	tick := func(second int) {
		t.Helper()
		offset.Store(int64(time.Duration(second) * time.Second))
		// The second rendezvous proves the first iteration completed, even when
		// that iteration intentionally emitted no frame and made no Core request.
		for range 2 {
			select {
			case ticks <- now():
			case <-ctx.Done():
				t.Fatal("subscription tick did not complete")
			}
		}
	}
	for second := 1; second <= 3; second++ {
		tick(second)
	}
	if n := counter.calls.Load(); n != 1 {
		t.Fatal("unchanged samples rechecked Core every second", n)
	}
	if _, e := fx.pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET sample_sequence=sample_sequence+1,last_payload='[{"key":"quote","value":189.75}]',payload_at=clock_timestamp()`); e != nil {
		t.Fatal(e)
	}
	tick(4)
	if n := counter.calls.Load(); n != 2 {
		t.Fatal("sample sequence change did not reauthorize promptly", n)
	}
	for second := 5; second <= 8; second++ {
		tick(second)
	}
	if n := counter.calls.Load(); n != 2 {
		t.Fatal("unchanged sample bypassed the slower check interval", n)
	}
	tick(9)
	if n := counter.calls.Load(); n != 3 {
		t.Fatal("periodic authority check missing at five seconds", n)
	}
}

func TestDatasetMCPDeadlineUsesCoreApproval(t *testing.T) {
	fx := newTopicFx(t)
	r, _ := setupMCPDataset(t, fx)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET expires_at=clock_timestamp()+interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	metadata := &datasetSampleMetadata{}
	ctx := context.WithValue(t.Context(), datasetSampleMetadataContextKey{}, metadata)
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.mcp", Parameters: map[string]string{"binding": r.Scope.Binding}}}
	rows, _, cause := mcpDatasetRows(ctx, fx.api.hub, fx.pool, mcpFixtureOwner, q)
	if cause != "" || len(rows) != 1 || !metadata.validUntil.Equal(r.ExpiresAt) || metadata.intervalSeconds != 60 {
		t.Fatal("Runner-local expiry extended Core-approved browser retention")
	}
}
func TestDatasetMCPReadCacheCannotExtendExpiredApproval(t *testing.T) {
	fx := newTopicFx(t)
	r, _ := setupMCPDataset(t, fx)
	fx.api.hub.mcpAuthority.entries = map[string]*mcpAuthorityResult{r.Scope.Binding: {scope: r.Scope, sequence: 0, ready: true, until: time.Now().Add(time.Second), decision: mcpAuthorityDecision{expiresAt: time.Now().Add(-time.Second)}}}
	if scope, cause := fx.api.hub.mcpDatasetAuthority(t.Context(), fx.pool, mcpFixtureOwner, fx.sess, r.Scope.Binding); scope.Valid() || cause != "access_removed" {
		t.Fatal("read cache extended expired Core approval")
	}
}
func TestDatasetMCPQueuedDeadlineCannotUseNewerSample(t *testing.T) {
	fx := newTopicFx(t)
	r, _ := setupMCPDataset(t, fx)
	id, epoch := newUUID(), newUUID()
	bc := &BrowserConn{AccountID: mcpFixtureOwner, datasetSubs: map[string]*datasetSubscription{id: {reference: protocol.DatasetSubscribe{Data: protocol.DatasetReference{Name: "blerg.mcp", Parameters: map[string]string{"binding": r.Scope.Binding}}}, epoch: epoch, deliveryFences: map[int64]datasetDeliveryFence{1: {liveScope: r.Scope, validUntil: time.Now().Add(-time.Second)}}}}}
	raw, _ := json.Marshal(protocol.DatasetDelta{Type: "dataset_delta", SessionID: fx.sess, SubscriptionID: id, Epoch: epoch, Sequence: 1, Snapshot: true, Status: "ready", Upserts: []protocol.DatasetRow{{"key": "old", "value": 175.25}}})
	safe, ok := fx.api.hub.filterDatasetDelivery(t.Context(), bc, fx.pool, raw)
	if !ok || strings.Contains(string(safe), "175.25") || !strings.Contains(string(safe), "sample_expired") {
		t.Fatal("expired queued frame borrowed a newer sample's validity")
	}
}

type mcpDeliveryMutationTransport struct {
	base   http.RoundTripper
	before func()
}

func (m mcpDeliveryMutationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	m.before()
	return m.base.RoundTrip(r)
}
func TestDatasetMCPQueuedFrameCannotBorrowNewScope(t *testing.T) {
	fx := newTopicFx(t)
	r, _ := setupMCPDataset(t, fx)
	changed := r.Scope
	changed.Revision++
	rawScope, _ := json.Marshal(changed)
	var once sync.Once
	client := fx.api.hub.liveDataAuthority.HTTP
	client.Transport = mcpDeliveryMutationTransport{base: client.Transport, before: func() {
		once.Do(func() {
			if _, err := fx.pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET revision=$1,scope=$2`, changed.Revision, rawScope); err != nil {
				t.Fatal("new scope fixture")
			}
		})
	}}
	id, epoch := newUUID(), newUUID()
	bc := &BrowserConn{AccountID: mcpFixtureOwner, datasetSubs: map[string]*datasetSubscription{id: {reference: protocol.DatasetSubscribe{Data: protocol.DatasetReference{Name: "blerg.mcp", Parameters: map[string]string{"binding": r.Scope.Binding}}}, epoch: epoch, deliveryFences: map[int64]datasetDeliveryFence{1: {liveScope: r.Scope, validUntil: time.Now().Add(time.Hour)}}}}}
	raw, _ := json.Marshal(protocol.DatasetDelta{Type: "dataset_delta", SessionID: fx.sess, SubscriptionID: id, Epoch: epoch, Sequence: 1, Snapshot: true, Status: "ready", Upserts: []protocol.DatasetRow{{"key": "old", "value": 175.25}}})
	safe, ok := fx.api.hub.filterDatasetDelivery(t.Context(), bc, fx.pool, raw)
	if !ok || strings.Contains(string(safe), "175.25") {
		t.Fatal("queued old scope borrowed a newer scope's payload validity")
	}
}

func TestDatasetMCPFailureCauseChangesEmitWithoutNewRows(t *testing.T) {
	fx := newTopicFx(t)
	r, _ := setupMCPDataset(t, fx)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET payload_at=clock_timestamp()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	ticks := make(chan time.Time)
	q := protocol.DatasetSubscribe{SessionID: fx.sess, SubscriptionID: newUUID(), Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.mcp", Parameters: map[string]string{"binding": r.Scope.Binding}}}
	sub := &datasetSubscription{ticks: ticks, cancel: cancel, heartbeat: make(chan struct{}, 1), reference: q, epoch: newUUID(), done: make(chan struct{})}
	bc := &BrowserConn{AccountID: mcpFixtureOwner, ctx: ctx, send: make(chan []byte, 16), datasetSubs: map[string]*datasetSubscription{q.SubscriptionID: sub}}
	go runDatasetSubscription(ctx, fx.api.hub, bc, fx.pool, q, sub)
	defer func() { cancel(); <-sub.done }()
	select {
	case <-bc.send:
	case <-ctx.Done():
		t.Fatal("initial sample missing")
	}
	var observed time.Time
	for _, cause := range []string{"provider_rate_limited", "mapping_failed", "execution_unavailable", "execution_unavailable"} {
		if _, err := fx.pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET cause=$1,sample_sequence=sample_sequence+1,next_due_at=clock_timestamp()+interval '2 minutes'`, cause); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			select {
			case ticks <- time.Now():
			case <-ctx.Done():
				t.Fatal("subscription tick blocked")
			}
		}
		select {
		case raw := <-bc.send:
			var delta protocol.DatasetDelta
			if json.Unmarshal(raw, &delta) != nil || delta.Cause != cause || delta.Status != "unavailable" {
				t.Fatal("failure cause not delivered", cause)
			}
			if !observed.IsZero() && !delta.ObservedAt.Equal(observed) {
				t.Fatal("failure changed fetch time")
			}
			observed = delta.ObservedAt
		default:
			t.Fatal("failure cause change suppressed", cause)
		}
	}
}
