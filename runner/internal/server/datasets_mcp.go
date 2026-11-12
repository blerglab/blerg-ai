package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/runner/internal/livepoll"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This checks owner and workspace before reading payload bytes, and rechecks
// current Core authority at collection and queued delivery. Subscriber-only
// decisions are shared for at most one second and keyed by the sample sequence.
func (h *Hub) mcpDatasetAuthority(ctx context.Context, pool *pgxpool.Pool, owner, workspace, binding string) (ld.Scope, string) {
	if !h.liveDataPollingEnabled.Load() || h.liveDataAuthority == nil || pool == nil {
		return ld.Scope{}, "polling_disabled"
	}
	var raw []byte
	var sequence int64
	var state string
	var localExpiry time.Time
	if err := pool.QueryRow(ctx, `SELECT scope,sample_sequence,cause,expires_at FROM live_mcp_binding_state WHERE binding_id::text=$1 AND workspace_id::text=$2 AND scope->>'owner'=$3`, binding, workspace, owner).Scan(&raw, &sequence, &state, &localExpiry); err != nil {
		return ld.Scope{}, "access_removed"
	}
	if livepoll.PurgesSample(state) {
		return ld.Scope{}, "access_removed"
	}
	var scope ld.Scope
	if json.Unmarshal(raw, &scope) != nil || !scope.Valid() {
		return ld.Scope{}, "access_removed"
	}
	if !localExpiry.After(time.Now()) {
		fenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		err := (livepoll.Attempts{Pool: pool}).RevokeRevision(fenceCtx, scope, "grant_expired")
		cancel()
		if err != nil {
			datasetFailure("purge_store_unavailable")
		} else {
			datasetFailure("grant_expired")
		}
		return ld.Scope{}, "access_removed"
	}
	decision := h.mcpAuthority.check(ctx, scope, sequence, func() mcpAuthorityDecision { return h.checkMCPAuthority(ctx, scope) })
	cause := decision.cause
	if cause == "" && !decision.expiresAt.After(time.Now()) {
		cause = "access_removed"
	}
	if cause == "" {
		if e := (livepoll.Attempts{Pool: pool}).ObserveEpoch(ctx, scope); e != nil {
			cause = "access_removed"
		}
	}
	if cause != "" {
		if cause == "access_removed" {
			if _, err := pool.Exec(ctx, `UPDATE live_mcp_binding_state SET enabled=false,fence=fence+1,last_payload=NULL,payload_at=NULL,cause='authority_revoked' WHERE binding_id::text=$1 AND scope=$2::jsonb AND (enabled OR last_payload IS NOT NULL)`, binding, raw); err != nil {
				datasetFailure("purge_store_unavailable")
			}
		}
		return ld.Scope{}, cause
	}
	if metadata, ok := ctx.Value(datasetSampleMetadataContextKey{}).(*datasetSampleMetadata); ok {
		metadata.authorityUntil = decision.expiresAt
		metadata.intervalSeconds = decision.intervalSeconds
	}
	return scope, ""
}
func (h *Hub) checkMCPAuthority(ctx context.Context, scope ld.Scope) mcpAuthorityDecision {
	start := time.Now()
	snapshot, err := h.liveDataAuthority.Current(ctx, scope)
	end := time.Now()
	if err != nil {
		if errors.Is(err, ld.ErrDenied) {
			return mcpAuthorityDecision{cause: "access_removed"}
		}
		return mcpAuthorityDecision{cause: "authority_unavailable"}
	}
	rtt := end.Sub(start)
	offset := snapshot.Grant.CoreTime.Sub(start.Add(rtt / 2))
	if offset < 0 {
		offset = -offset
	}
	if rtt < 0 || rtt > 30*time.Second || offset+rtt/2 > 30*time.Second {
		return mcpAuthorityDecision{cause: "authority_unavailable"}
	}
	if snapshot.Grant.Scope != scope {
		return mcpAuthorityDecision{cause: "access_removed"}
	}
	approved, err := snapshot.Grant.ApprovedRevision()
	if err != nil {
		return mcpAuthorityDecision{cause: "access_removed"}
	}
	return mcpAuthorityDecision{expiresAt: snapshot.Grant.ExpiresAt, intervalSeconds: approved.IntervalSeconds}
}
func mcpDatasetRows(ctx context.Context, h *Hub, pool *pgxpool.Pool, owner string, q protocol.DatasetSubscribe) ([]protocol.DatasetRow, time.Time, string) {
	scope, cause := h.mcpDatasetAuthority(ctx, pool, owner, q.SessionID, q.Data.Parameters["binding"])
	if cause != "" {
		return nil, time.Time{}, cause
	}
	if metadata, ok := ctx.Value(datasetSampleMetadataContextKey{}).(*datasetSampleMetadata); ok {
		metadata.liveScope = scope
	}
	var raw []byte
	var at *time.Time
	var state string
	var nextAttempt time.Time
	scopeJSON, _ := json.Marshal(scope)
	if err := pool.QueryRow(ctx, `SELECT last_payload,payload_at,cause,next_due_at FROM live_mcp_binding_state WHERE binding_id::text=$1 AND scope=$2::jsonb AND workspace_id::text=$3 AND scope->>'owner'=$4 `, scope.Binding, scopeJSON, q.SessionID, owner).Scan(&raw, &at, &state, &nextAttempt); err != nil {
		return nil, time.Time{}, "sample_unavailable"
	}
	if at == nil || !at.Add(24*time.Hour).After(time.Now()) {
		if state == "sample_expired" || at != nil {
			return nil, time.Time{}, "sample_expired"
		}
		return nil, time.Time{}, "sample_unavailable"
	}
	var rows []protocol.DatasetRow
	if len(raw) > 5*1024*1024 || json.Unmarshal(raw, &rows) != nil || len(rows) > 10000 {
		return nil, time.Time{}, "sample_unavailable"
	}
	if metadata, ok := ctx.Value(datasetSampleMetadataContextKey{}).(*datasetSampleMetadata); ok {
		metadata.liveScope = scope
		metadata.validUntil = metadata.authorityUntil
		if ttl := at.Add(24 * time.Hour); ttl.Before(metadata.validUntil) {
			metadata.validUntil = ttl
		}
		metadata.nextAttempt = nextAttempt
	}
	return rows, *at, livepoll.SampleCause(state)
}

// Local sequence/fence polling is cheap and writes no audit. Remote authority
// runs on changed samples or the slower subscription refresh, not every tick.
func mcpDatasetVersion(ctx context.Context, pool *pgxpool.Pool, owner string, q protocol.DatasetSubscribe) string {
	if pool == nil {
		return "unavailable"
	}
	var version string
	if e := pool.QueryRow(ctx, `SELECT concat(sample_sequence,':',fence,':',cause,':',enabled) FROM live_mcp_binding_state WHERE binding_id::text=$1 AND workspace_id::text=$2 AND scope->>'owner'=$3 AND expires_at>clock_timestamp()`, q.Data.Parameters["binding"], q.SessionID, owner).Scan(&version); e != nil {
		return "unavailable"
	}
	return version
}
