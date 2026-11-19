package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livepoll"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

type datasetSubscription struct {
	// Controlled clock/tick seams for subscription protocol tests.
	now            func() time.Time
	ticks          <-chan time.Time
	cancel         context.CancelFunc
	heartbeat      chan struct{}
	deliveryFences map[int64]datasetDeliveryFence
	forceSnapshot  atomic.Bool
	scope          *datasetScope
	reference      protocol.DatasetSubscribe
	epoch          string
	done           chan struct{}
}
type datasetKey struct{ name, scope string }
type datasetScope struct {
	revision atomic.Uint64
	viewers  int
}
type datasetService struct {
	mu          sync.Mutex
	scopes      map[datasetKey]*datasetScope
	boardChecks map[string]time.Time
}

func (s *datasetService) watch(key datasetKey) (*datasetScope, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scopes == nil {
		s.scopes = map[datasetKey]*datasetScope{}
	}
	scope := s.scopes[key]
	if scope == nil {
		scope = &datasetScope{}
		s.scopes[key] = scope
	}
	scope.viewers++
	return scope, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		scope.viewers--
		if scope.viewers == 0 {
			delete(s.scopes, key)
		}
	}
}
func (s *datasetService) invalidate(name, scope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := s.scopes[datasetKey{name, scope}]; v != nil {
		v.revision.Add(1)
	}
}

// Only session lifecycle facts affect worker counts. Transcript, typing, read,
// title and unrelated account traffic do not invalidate dataset subscriptions.
func (h *Hub) notifyDatasetBroadcast(raw []byte) {
	var event struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	switch event.Type {
	case "session_started", "session_ended", "session_state_changed", "session_privacy_changed", "session_deleted":
	default:
		return
	}
	h.datasets.mu.Lock()
	active := len(h.datasets.scopes) > 0
	h.datasets.mu.Unlock()
	if !active || h.desktopPool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h.invalidateDatasetSession(ctx, h.desktopPool, event.SessionID, false)
}
func (h *Hub) invalidateDatasetSession(ctx context.Context, pool *pgxpool.Pool, sid string, merges bool) {
	root, err := datasetWorkspaceRoot(ctx, pool, sid)
	if err != nil {
		datasetFailure("workspace_scope_unavailable")
		return
	}
	name := "blerg.workers"
	if merges {
		name = "blerg.cards.merged"
	}
	h.datasets.invalidate(name, root)
	if merges {
		h.datasets.invalidate("blerg.cards.merged.heatmap", root)
	} else {
		h.datasets.invalidate("blerg.workers.timeline", root)
		h.datasets.invalidate("blerg.gates.durations", root)
		h.datasets.invalidate("blerg.gates.bins", root)
	}
}

func datasetWorkspaceRoot(ctx context.Context, pool *pgxpool.Pool, sid string) (string, error) {
	var root string
	err := pool.QueryRow(ctx, `WITH RECURSIVE ancestors AS (
 SELECT id,parent_session_id,ARRAY[id] AS path FROM sessions WHERE id=$1
 UNION ALL SELECT s.id,s.parent_session_id,a.path||s.id FROM sessions s JOIN ancestors a ON s.id=a.parent_session_id
 WHERE NOT s.id=ANY(a.path) AND cardinality(a.path)<64)
 SELECT id::text FROM ancestors WHERE parent_session_id IS NULL`, sid).Scan(&root)
	return root, err
}

// Retention belongs to the service schedule, never the refresh hot path.
func (a *API) RunDatasetRetention(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pruneDatasetSources(ctx)
		}
	}
}

// Independent deadlines keep one unavailable source from suppressing another's cleanup.
func (a *API) pruneDatasetSources(ctx context.Context) {
	audit, cancel := context.WithTimeout(ctx, 30*time.Second)
	if _, err := a.dbPool.Exec(audit, `DELETE FROM live_dataset_attempts WHERE started_at<clock_timestamp()-interval '180 days'`); err != nil {
		performanceFailure("audit_retention_failed", err)
	}
	cancel()
	tests, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := pruneTestRuns(tests, a.dbPool); err != nil {
		performanceFailure("test_retention_failed", err)
	}
	cancel()
	gates, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := pruneGateReports(gates, a.dbPool); err != nil {
		performanceFailure("gate_retention_failed", err)
	}
	cancel()
}

func auditDatasetSubscription(ctx context.Context, pool *pgxpool.Pool, account string, q protocol.DatasetSubscribe) (string, error) {
	id := newUUID()
	_, err := pool.Exec(ctx, `INSERT INTO live_dataset_attempts(id,session_id,owner_id,source,outcome) VALUES($1,$2,$3,$4,'subscribed')`, id, q.SessionID, account, q.Data.Name)
	return id, err
}

var datasetSlots = make(chan struct{}, 2)

func datasetFailure(cause string) {
	failurelog.Default.Event("live_dataset:"+cause, slog.LevelWarn, "component", "runner", "operation", "live_dataset", "cause", cause)
}

func handleDatasetCommand(h *Hub, bc *BrowserConn, pool *pgxpool.Pool, raw []byte) {
	var q protocol.DatasetSubscribe
	if json.Unmarshal(raw, &q) != nil || !protocol.ValidChildBranchID(q.SubscriptionID) {
		datasetFailure("invalid_subscription")
		return
	}
	bc.subMu.Lock()
	defer bc.subMu.Unlock()
	old := bc.datasetSubs[q.SubscriptionID]
	switch q.Type {
	case "dataset_diagnostic":
		if q.Cause == "sequence_gap" || q.Cause == "invalid_sample" || q.Cause == "invalid_document" || q.Cause == "sample_expired" {
			datasetFailure(q.Cause)
		} else {
			datasetFailure("invalid_diagnostic")
		}
	case "unsubscribe_dataset":
		if old != nil {
			old.cancel()
			delete(bc.datasetSubs, q.SubscriptionID)
		}
	case "dataset_heartbeat":
		if old != nil {
			select {
			case old.heartbeat <- struct{}{}:
			default:
			}
		}
	case "subscribe_dataset":
		if !q.Data.Valid() || !protocol.ValidChildBranchID(q.SessionID) {
			datasetFailure("invalid_reference")
			return
		}
		if old == nil && len(bc.datasetSubs) >= 32 {
			datasetFailure("subscription_limit")
			return
		}
		if old != nil {
			old.cancel()
		}
		if bc.datasetSubs == nil {
			bc.datasetSubs = map[string]*datasetSubscription{}
		}
		ctx, cancel := context.WithCancel(bc.ctx)
		sub := &datasetSubscription{cancel: cancel, heartbeat: make(chan struct{}, 1), reference: q, epoch: newUUID(), done: make(chan struct{})}
		bc.datasetSubs[q.SubscriptionID] = sub
		go runDatasetSubscription(ctx, h, bc, pool, q, sub)
	}
}

// Every connection starts a new epoch/snapshot. No rows or credentials survive
// reconnect; the record-derived cache is rebuilt under current viewer authority.
func runDatasetSubscription(ctx context.Context, h *Hub, bc *BrowserConn, pool *pgxpool.Pool, q protocol.DatasetSubscribe, sub *datasetSubscription) {
	defer close(sub.done)
	retainPurge := false
	defer func() {
		if retainPurge {
			return
		}
		bc.subMu.Lock()
		defer bc.subMu.Unlock()
		if bc.datasetSubs[q.SubscriptionID] == sub {
			delete(bc.datasetSubs, q.SubscriptionID)
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := ticker.C
	if sub.ticks != nil {
		ticks = sub.ticks
	}
	now := time.Now
	if sub.now != nil {
		now = sub.now
	}
	lease := time.NewTimer(60 * time.Second)
	defer lease.Stop()
	scopeID := q.SessionID
	if pool != nil {
		if q.Data.Name == "blerg.engine.usage" {
			if row, err := db.GetSession(ctx, pool, q.SessionID); err == nil && row != nil {
				scopeID = row.DaemonID
			}
		} else {
			if root, err := datasetWorkspaceRoot(ctx, pool, q.SessionID); err == nil {
				scopeID = root
			}
		}
	}
	scope, unwatch := h.datasets.watch(datasetKey{q.Data.Name, scopeID})
	defer unwatch()
	sub.scope = scope
	auditID := ""
	if pool != nil && datasetVisible(ctx, pool, bc.AccountID, q.SessionID) {
		auditCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var err error
		auditID, err = auditDatasetSubscription(auditCtx, pool, bc.AccountID, q)
		cancel()
		if err != nil {
			datasetFailure("audit_unavailable")
			return
		}
		defer func() {
			auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			if _, err := pool.Exec(auditCtx, `UPDATE live_dataset_attempts SET finished_at=clock_timestamp(),outcome='unsubscribed' WHERE id=$1`, auditID); err != nil {
				datasetFailure("audit_finish_failed")
			}
		}()
	}
	demand := func() {
		if q.Data.Name != "blerg.mcp" {
			return
		}
		if _, cause := h.mcpDatasetAuthority(ctx, pool, bc.AccountID, q.SessionID, q.Data.Parameters["binding"]); cause != "" {
			datasetFailure(cause)
			return
		}
		if err := (livepoll.Attempts{Pool: pool}).Demand(ctx, bc.AccountID, q.SessionID, q.Data.Parameters["binding"]); err != nil {
			datasetFailure("demand_refused")
		}
	}
	demand()
	refreshInterval := 15 * time.Second
	if q.Data.Name == "blerg.mcp" {
		refreshInterval = 5 * time.Second
	}
	epoch := sub.epoch
	var seq int64
	previous := map[string]protocol.DatasetRow{}
	var history *protocol.MergeHistory
	var mergeDaemon *DaemonConn
	var lastMCPVersion string
	var lastRevision uint64
	var lastRefresh time.Time
	status := ""
	var lastObserved time.Time
	lastTruncated := false
	lastCause := ""
	var lastNextAttempt time.Time
	var retryAt time.Time
	failures := 0
	for {
		revision := scope.revision.Load()
		var mcpVersion string
		if q.Data.Name == "blerg.mcp" {
			check, cancel := context.WithTimeout(ctx, 2*time.Second)
			mcpVersion = mcpDatasetVersion(check, pool, bc.AccountID, q)
			cancel()
		}
		if lastRefresh.IsZero() || mcpVersion != lastMCPVersion || (sub.forceSnapshot.Load() && !time.Now().Before(retryAt)) || revision != lastRevision || (!time.Now().Before(retryAt) && (failures > 0 || now().Sub(lastRefresh) >= refreshInterval)) {
			refreshCtx, cancel := context.WithTimeout(context.WithValue(ctx, datasetViewerContextKey{}, bc.verified), 8*time.Second)
			if dc := h.FindDaemonForSession(q.SessionID); dc != mergeDaemon {
				history = nil
				mergeDaemon = dc
			}
			var privacyFence map[string]string
			var fenceErr error
			if descendantDataset(q.Data.Name) {
				privacyFence, fenceErr = datasetPrivacyFence(refreshCtx, pool, q.SessionID)
			}
			metadata := &datasetSampleMetadata{}
			refreshCtx = context.WithValue(refreshCtx, datasetSampleMetadataContextKey{}, metadata)
			rows, observed, cause := resolveDataset(refreshCtx, h, pool, bc.AccountID, q, &history, lastRefresh.IsZero() || ((q.Data.Name == "blerg.cards.merged" || q.Data.Name == "blerg.cards.merged.heatmap") && revision != lastRevision))
			if fenceErr != nil {
				rows = nil
				cause = "authority_unavailable"
			}
			cancel()
			if ctx.Err() != nil {
				return
			}
			lastRefresh = now()
			lastRevision = revision
			lastMCPVersion = mcpVersion
			if cause == "" {
				lastObserved = observed
			} else if !lastObserved.IsZero() {
				observed = lastObserved
			}
			if cause != "" {
				failures++
				delay := time.Second * time.Duration(1<<min(failures-1, 6))
				if delay > 60*time.Second {
					delay = 60 * time.Second
				}
				if delay > time.Second {
					delay = delay*3/4 + time.Duration(rand.Int64N(int64(delay/4)+1)) //nolint:gosec // retry jitter is scheduling, not a credential
				}
				retryAt = time.Now().Add(delay)
			} else {
				failures = 0
				retryAt = time.Time{}
			}
			if cause != "" {
				metadata.truncated = lastTruncated
			}
			nextStatus := "ready"
			if cause != "" {
				datasetFailure(cause)
				nextStatus = "unavailable"
			}
			if datasetPurgeCause(cause) {
				nextStatus = "access_removed"
				rows = []protocol.DatasetRow{}
			}
			// Recheck at delivery. A revoked source cannot retain last-good data.
			if visibilityCause := datasetVisibilityCause(ctx, pool, bc.AccountID, q.SessionID); visibilityCause != "" {
				cause = visibilityCause
				nextStatus = "unavailable"
				if datasetPurgeCause(visibilityCause) {
					nextStatus = "access_removed"
					rows = []protocol.DatasetRow{}
				}
			}
			next := map[string]protocol.DatasetRow{}
			upserts := []protocol.DatasetRow{}
			removals := []string{}
			if cause == "" || datasetPurgeCause(cause) || (q.Data.Name == "blerg.mcp" && rows != nil) {
				for _, row := range rows {
					key, _ := row["key"].(string)
					next[key] = row
					a, _ := json.Marshal(row)
					b, _ := json.Marshal(previous[key])
					if string(a) != string(b) {
						upserts = append(upserts, row)
					}
				}
				for key := range previous {
					if next[key] == nil {
						removals = append(removals, key)
					}
				}
			} else {
				next = previous
			}
			force := cause == "" && sub.forceSnapshot.Swap(false)
			if force {
				upserts = rows
			}
			// Heartbeats carry the true observation time; no change to document revision.
			deltaCause := cause
			if cause == "" && metadata.truncated {
				deltaCause = "truncated"
			}
			delta := protocol.DatasetDelta{Type: "dataset_delta", SessionID: q.SessionID, SubscriptionID: q.SubscriptionID, Epoch: epoch, BaseSequence: seq, Sequence: seq + 1, Snapshot: seq == 0 || force || datasetPurgeCause(cause), Upserts: upserts, Removals: removals, ObservedAt: observed, Status: nextStatus, Cause: deltaCause, ValidUntil: metadata.validUntil, ExpectedIntervalSeconds: metadata.intervalSeconds, NextAttemptAt: metadata.nextAttempt}
			if delta.Snapshot || seq == 0 || len(upserts) > 0 || len(removals) > 0 || status != nextStatus || lastCause != deltaCause || !lastNextAttempt.Equal(metadata.nextAttempt) || lastTruncated != metadata.truncated || time.Since(observed) < time.Second {
				payload, err := json.Marshal(delta)
				if err != nil {
					datasetFailure("serialization")
					return
				}
				bc.subMu.Lock()
				if sub.deliveryFences == nil {
					sub.deliveryFences = map[int64]datasetDeliveryFence{}
				}
				sub.deliveryFences[seq+1] = datasetDeliveryFence{privacy: privacyFence, revision: revision, liveScope: metadata.liveScope, validUntil: metadata.validUntil}
				bc.subMu.Unlock()
				if !emitDatasetDelta(ctx, bc, payload) {
					return
				}
				seq++
				previous = next
				status = nextStatus
				lastTruncated = metadata.truncated
				lastCause = deltaCause
				lastNextAttempt = metadata.nextAttempt
			}
			// Payload expiry clears data but leaves an authorized subscription
			// alive for a new sample. Only authority loss ends it.
			if cause == "access_removed" {
				retainPurge = true
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-lease.C:
			datasetFailure("lease_expired")
			return
		case <-sub.heartbeat:
			demand()
			if !lease.Stop() {
				select {
				case <-lease.C:
				default:
				}
			}
			lease.Reset(60 * time.Second)
		case <-ticks:
		}
	}
}

func datasetVisibilityCause(ctx context.Context, pool *pgxpool.Pool, account, sid string) string {
	if pool == nil {
		return "authority_unavailable"
	}
	row, err := db.GetSession(ctx, pool, sid)
	if err != nil {
		return "authority_unavailable"
	}
	if row == nil || !canSeeAccount(account, row) {
		return "access_removed"
	}
	return ""
}

func datasetVisible(ctx context.Context, pool *pgxpool.Pool, account, sid string) bool {
	row, err := db.GetSession(ctx, pool, sid)
	return err == nil && row != nil && canSeeAccount(account, row)
}

// Fail closed before dispatch if audit or authority is unavailable. Catalogued
// local records need no external service capability and never reuse agent tokens.
func resolveDataset(ctx context.Context, h *Hub, pool *pgxpool.Pool, account string, q protocol.DatasetSubscribe, history **protocol.MergeHistory, readMerges bool) (rows []protocol.DatasetRow, observed time.Time, cause string) {
	observed = time.Now().UTC()
	if pool == nil {
		return nil, observed, "database_unavailable"
	}
	if cause := datasetVisibilityCause(ctx, pool, account, q.SessionID); cause != "" {
		return nil, observed, cause
	}
	select {
	case datasetSlots <- struct{}{}:
		defer func() { <-datasetSlots }()
	case <-ctx.Done():
		return nil, observed, "refresh_canceled"
	}
	if cause := datasetVisibilityCause(ctx, pool, account, q.SessionID); cause != "" {
		return nil, observed, cause
	}
	switch q.Data.Name {
	case "blerg.mcp":
		return mcpDatasetRows(ctx, h, pool, account, q)
	case "blerg.cards.time", "blerg.reviews.pairs":
		if h.datasetCardAccess == nil {
			return nil, observed, "board_unavailable"
		}
		if cause := h.datasetCardAccess(ctx, account, q.SessionID, q.Data.Parameters); cause != "" {
			return nil, observed, cause
		}
		if q.Data.Name == "blerg.reviews.pairs" {
			return reviewPairDatasetRows(ctx, pool, account, q.Data.Parameters)
		}
		return cardTimeDatasetRows(ctx, pool, account, q.Data.Parameters)
	case "blerg.workers.timeline":
		return timelineDatasetRows(ctx, pool, account, q.SessionID, q.Data.Parameters)
	case "blerg.gates.durations", "blerg.gates.bins":
		rows, at, cause := gateDatasetRows(ctx, pool, account, q.SessionID, q.Data.Parameters)
		if cause == "" && q.Data.Name == "blerg.gates.bins" {
			rows, cause = gateBinRows(rows, q.Data.Parameters)
		}
		if cause == "" && q.Data.Name == "blerg.gates.durations" && q.Data.Parameters["suite"] != "" {
			filtered := []protocol.DatasetRow{}
			for _, r := range rows {
				if r["suite"] == q.Data.Parameters["suite"] {
					filtered = append(filtered, r)
				}
			}
			rows = filtered
		}
		return rows, at, cause
	case "blerg.workers":

		root, err := datasetWorkspaceRoot(ctx, pool, q.SessionID)
		if err != nil {
			return nil, observed, "workspace_unavailable"
		}
		result, err := pool.Query(ctx, `WITH RECURSIVE children AS (
 SELECT id,status,private,spawning_account_id,ARRAY[id] AS path FROM sessions WHERE parent_session_id=$1
 UNION ALL SELECT s.id,s.status,s.private,s.spawning_account_id,c.path||s.id FROM sessions s JOIN children c ON s.parent_session_id=c.id
 WHERE NOT s.id=ANY(c.path) AND cardinality(c.path)<64)
 SELECT id::text,status,private,spawning_account_id FROM children`, root)
		if err != nil {
			return nil, observed, "workers_unavailable"
		}
		defer result.Close()
		running, idle := 0, 0
		for result.Next() {
			var s db.SessionRow
			if err := result.Scan(&s.ID, &s.Status, &s.Private, &s.SpawningAccountID); err != nil {
				return nil, observed, "workers_unavailable"
			}
			if !canSeeAccount(account, &s) {
				continue
			}
			switch s.Status {
			case "running":
				running++
			case "idle":
				idle++
			}
		}
		if result.Err() != nil {
			return nil, observed, "workers_unavailable"
		}
		return []protocol.DatasetRow{{"key": "workers", "running": running, "idle": idle}}, observed, ""
	case "blerg.engine.usage":
		row, err := db.GetSession(ctx, pool, q.SessionID)
		if err != nil || row == nil {
			return nil, observed, "session_unavailable"
		}
		dc := h.ownedHostDaemon(ctx, account, row.DaemonID)
		if dc == nil {
			return nil, observed, "usage_unavailable"
		}
		for _, u := range dc.UsageLimits() {
			if u.Provider == q.Data.Parameters["engine"] {
				for _, w := range u.Windows {
					if w.ID == "7d" {
						return []protocol.DatasetRow{{"key": u.Provider, "percent": w.UsedPercent, "reset_at": w.ResetsAt.Format(time.RFC3339)}}, u.FetchedAt, ""
					}
				}
			}
		}
		return nil, observed, "usage_unknown"
	case "blerg.cards.merged", "blerg.cards.merged.heatmap":
		row, err := db.GetSession(ctx, pool, q.SessionID)
		if err != nil || row == nil || !ownsSession(account, row) {
			return nil, observed, "access_removed"
		}
		dc := h.FindDaemonForSession(q.SessionID)
		if dc == nil || (derefOrEmpty(row.Runtime) != "cluster" && h.ownedHostDaemon(ctx, account, dc.ID) != dc) {
			return nil, observed, "daemon_unavailable"
		}

		manifest, generation, err := loadExplorerBinding(ctx, pool, q.SessionID)
		if err != nil {
			return nil, observed, "workspace_unavailable"
		}
		member := q.Data.Parameters["member"]
		if member == "" && len(manifest.Members) == 1 {
			member = manifest.Members[0].ID
		}
		found := false
		for _, m := range manifest.Members {
			if m.ID == member {
				found = true
			}
		}
		if !found {
			return nil, observed, "access_removed"
		}
		ref := protocol.WorkspaceGitRef{MemberID: member, WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}}
		changed := *history != nil && (*history).WorkspaceGitRef != ref
		if readMerges || *history == nil || changed {
			if changed {
				*history = nil
			}
			result, err := h.requestCommitRead(ctx, dc, account, protocol.ReadCommit{SessionID: q.SessionID, DatasetMerges: true, Release: q.Data.Parameters["release"], ReleaseBase: q.Data.Parameters["base"], WorkspaceGitRef: ref}, 5*time.Second)
			if err != nil || !result.OK || result.Merges == nil || len(result.Merges.Commits) > 10000 || result.Merges.ObservedAt.IsZero() {
				return nil, observed, datasetMergeFailure(err, result.ErrorCode)
			}
			*history = result.Merges
			(*history).WorkspaceGitRef = ref
		}
		if q.Data.Name == "blerg.cards.merged.heatmap" {
			rows, cause := mergeHeatmapRows(*history, q.Data.Parameters)
			return rows, (*history).ObservedAt, cause
		}
		rows, cause := mergeDatasetRows(*history, q.Data.Parameters, (*history).ObservedAt)
		return rows, (*history).ObservedAt, cause
	}
	return nil, observed, "invalid_reference"
}

func mergeDatasetRows(history *protocol.MergeHistory, p map[string]string, now time.Time) ([]protocol.DatasetRow, string) {
	zone, err := time.LoadLocation(p["timezone"])
	if err != nil {
		return nil, "invalid_timezone"
	}
	bucket := func(t time.Time) time.Time {
		t = t.In(zone)
		y, m, d := t.Date()
		if p["bucket"] == "hour" {
			return t.Add(-time.Duration(t.Minute())*time.Minute - time.Duration(t.Second())*time.Second - time.Duration(t.Nanosecond()))
		}
		return time.Date(y, m, d, 0, 0, 0, 0, zone)
	}
	counts := map[time.Time]int{bucket(now): 0}
	seen := map[string]bool{}
	for _, commit := range history.Commits {
		at, err := time.Parse(time.RFC3339, commit.Date)
		if err != nil || !protocol.ValidSessionOriginCommit(commit.SHA) {
			return nil, "invalid_merge_sample"
		}
		if !seen[commit.SHA] {
			counts[bucket(at)]++
			seen[commit.SHA] = true
		}
	}
	if len(counts) > 10000 {
		return nil, "row_limit"
	}
	keys := make([]time.Time, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })
	rows := make([]protocol.DatasetRow, 0, len(keys))
	for _, k := range keys {
		end := k.Add(time.Hour)
		if p["bucket"] == "day" {
			end = k.AddDate(0, 0, 1)
		}
		rows = append(rows, protocol.DatasetRow{"key": k.UTC().Format(time.RFC3339), "bucket": k.UTC().Format(time.RFC3339), "end": end.UTC().Format(time.RFC3339), "count": counts[k], "release": p["release"]})
	}
	return rows, ""
}

// Reauthorize queued delivery as well as collection. Removed subscriptions and
// old generations cannot deliver after a view closes or changes its source.
func (h *Hub) filterDatasetDelivery(ctx context.Context, bc *BrowserConn, pool *pgxpool.Pool, raw []byte) ([]byte, bool) {
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Type != "dataset_delta" {
		return raw, true
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var delta protocol.DatasetDelta
	if json.Unmarshal(raw, &delta) != nil {
		datasetFailure("invalid_delivery")
		return nil, false
	}
	bc.subMu.Lock()
	sub := bc.datasetSubs[delta.SubscriptionID]
	bc.subMu.Unlock()
	if sub != nil && sub.epoch != delta.Epoch {
		return nil, false
	}
	if delta.Status == "access_removed" {
		return raw, true
	}
	if sub == nil {
		return nil, false
	}
	select {
	case <-ctx.Done():
		return nil, false
	default:
	}
	cause := ""
	var row *db.SessionRow
	var err error
	if pool == nil {
		err = errors.New("database_unavailable")
	} else {
		row, err = db.GetSession(ctx, pool, delta.SessionID)
	}
	switch {
	case err != nil:
		cause = "authority_unavailable"
	case row == nil || !canSeeAccount(bc.AccountID, row):
		cause = "access_removed"
	}
	if cause == "" && sub.reference.Data.Name == "blerg.engine.usage" && h.ownedHostDaemon(ctx, bc.AccountID, row.DaemonID) == nil {
		cause = "access_removed"
	}
	if cause == "" && (sub.reference.Data.Name == "blerg.cards.merged" || sub.reference.Data.Name == "blerg.cards.merged.heatmap") && !ownsSession(bc.AccountID, row) {
		cause = "access_removed"
	}
	if cause == "" && (sub.reference.Data.Name == "blerg.cards.time" || sub.reference.Data.Name == "blerg.reviews.pairs") {
		cause = "board_unavailable"
		if h.datasetCardAccess != nil {
			cause = h.datasetCardAccess(context.WithValue(context.WithValue(ctx, datasetViewerContextKey{}, bc.verified), datasetDeliveryContextKey{}, true), bc.AccountID, delta.SessionID, sub.reference.Data.Parameters)
		}
	}

	bc.subMu.Lock()
	fence, hasFence := sub.deliveryFences[delta.Sequence]
	delete(sub.deliveryFences, delta.Sequence)
	bc.subMu.Unlock()
	if cause == "" && sub.reference.Data.Name == "blerg.mcp" {
		var current ld.Scope
		current, cause = h.mcpDatasetAuthority(ctx, pool, bc.AccountID, delta.SessionID, sub.reference.Data.Parameters["binding"])
		if cause == "" && (!hasFence || fence.liveScope != current) {
			cause = "access_removed"
		}
		if cause == "" && len(delta.Upserts) > 0 {
			var fresh bool
			scopeJSON, _ := json.Marshal(current)
			if e := pool.QueryRow(ctx, `SELECT last_payload IS NOT NULL AND payload_at>clock_timestamp()-interval '24 hours' FROM live_mcp_binding_state WHERE binding_id::text=$1 AND scope=$2::jsonb AND workspace_id::text=$3`, current.Binding, scopeJSON, delta.SessionID).Scan(&fresh); e != nil || !fresh || !time.Now().Before(fence.validUntil) {
				cause = "sample_expired"
			}
		}
	}

	if cause == "" && descendantDataset(sub.reference.Data.Name) && delta.Status == "ready" {
		current, err := datasetPrivacyFence(ctx, pool, delta.SessionID)
		switch {
		case err != nil:
			cause = "authority_unavailable"
		case !hasFence:
			cause = "access_removed"
		default:
			cause = datasetFenceChange(fence.privacy, current)
		}
	}
	// Suppress stale membership frames without changing the viewer's status.
	// The next collection is a full snapshot, so dropping this sequence cannot
	// apply a later delta against an incorrect base.
	if cause == "source_changed" {
		sub.forceSnapshot.Store(true)
		return nil, false
	}
	if cause != "" {
		sub.forceSnapshot.Store(true)
		datasetFailure(cause)
		delta.Cause = cause
		delta.Upserts = []protocol.DatasetRow{}
		delta.Removals = []string{}
		if datasetPurgeCause(cause) {
			delta.Status = "access_removed"
			delta.Snapshot = true
		} else {
			delta.Status = "unavailable"
			delta.Snapshot = delta.BaseSequence == 0
			sub.forceSnapshot.Store(true)
		}
		safe, err := json.Marshal(delta)
		return safe, err == nil
	}

	return raw, true
}

func emitDatasetDelta(ctx context.Context, bc *BrowserConn, payload []byte) bool {
	select {
	case bc.send <- payload:
		return true
	case <-ctx.Done():
		return false
	default:
		datasetFailure("slow_viewer")
		bc.cancel()
		return false
	}
}

func datasetMergeFailure(err error, code string) string {
	broker := "none"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		broker = "deadline"
	case errors.Is(err, context.Canceled):
		broker = "canceled"
	case errors.Is(err, errCommitBusy):
		broker = "busy"
	case err != nil:
		broker = "unavailable"
	}
	switch code {
	case "binding_unavailable", "invalid_release", "repository_unavailable", "repository_untrusted", "main_unavailable", "release_base_unavailable", "release_not_ancestor", "git_log_failed", "invalid_history", "read_canceled", "merge_history_unavailable":
	case "":
		code = "invalid_reply"
	default:
		code = "unknown_daemon_code"
	}
	cause := "merge_" + broker + "_" + code
	datasetFailure(cause)
	return cause
}

// Payload expiry is a purge, but it does not revoke the owner's right to request
// a new sample from an otherwise-current binding.
func datasetPurgeCause(cause string) bool {
	return cause == "access_removed" || cause == "sample_expired"
}
