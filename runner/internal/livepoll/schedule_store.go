package livepoll

import (
	"context"
	"encoding/json"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

// Claim grants a 35-second lease for a call bounded to 30 seconds. SKIP LOCKED
// and a new fence on every claim prevent two replicas dispatching one revision.
// The shared attempt transaction lock serializes owner rotation across replicas;
// claimers cannot hold different owner rows while waiting on each other.
func (a Attempts) Claim(ctx context.Context) ([]Lease, error) {
	if a.Pool == nil {
		return nil, refuse("schedule_store_unavailable")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, refuse("schedule_claim_failed")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `WITH queued AS (
 SELECT binding_id,scope,next_due_at,coalesce(o.last_claimed_turn,0) AS owner_turn
 FROM live_mcp_binding_state b LEFT JOIN live_mcp_owner_schedule o ON o.owner_id::text=b.scope->>'owner' WHERE enabled AND workspace_id IS NOT NULL
 AND demand_until>clock_timestamp() AND expires_at>clock_timestamp()
 AND lease_until<=clock_timestamp() AND next_due_at<=clock_timestamp()
 ORDER BY owner_turn,next_due_at,binding_id LIMIT 80
 ),candidates AS (SELECT binding_id,owner_turn,row_number() OVER(PARTITION BY scope->>'owner' ORDER BY next_due_at,binding_id) AS turn FROM queued),due AS (
 SELECT b.binding_id,c.owner_turn FROM live_mcp_binding_state b JOIN candidates c ON c.binding_id=b.binding_id WHERE enabled AND workspace_id IS NOT NULL
 AND demand_until>clock_timestamp() AND expires_at>clock_timestamp()
 AND lease_until<=clock_timestamp() AND next_due_at<=clock_timestamp()
 ORDER BY c.turn,c.owner_turn,next_due_at,b.binding_id FOR UPDATE OF b SKIP LOCKED LIMIT 4)
  ,claimed AS (UPDATE live_mcp_binding_state b SET fence=b.fence+1,lease_until=clock_timestamp()+interval '35 seconds',
 next_due_at=clock_timestamp()+make_interval(secs=>GREATEST(b.interval_seconds,15))
 FROM due WHERE b.binding_id=due.binding_id
 RETURNING b.scope,b.fence,b.interval_seconds,b.failure_count,due.owner_turn),rotated AS (
 INSERT INTO live_mcp_owner_schedule(owner_id,last_claimed_turn)
 SELECT owner_id,nextval('live_mcp_owner_turn') FROM (
 SELECT (scope->>'owner')::uuid AS owner_id,min(owner_turn) AS previous_turn FROM claimed GROUP BY scope->>'owner'
 ) owners ORDER BY previous_turn,owner_id
 ON CONFLICT(owner_id) DO UPDATE SET last_claimed_turn=EXCLUDED.last_claimed_turn RETURNING owner_id)
 SELECT c.scope,c.fence,c.interval_seconds,c.failure_count FROM claimed c
 JOIN rotated r ON r.owner_id::text=c.scope->>'owner'`)
	if err != nil {
		return nil, refuse("schedule_claim_failed")
	}
	defer rows.Close()
	var leases []Lease
	for rows.Next() {
		var l Lease
		var raw []byte
		if rows.Scan(&raw, &l.Fence, &l.Interval, &l.Failures) != nil || json.Unmarshal(raw, &l.Scope) != nil || !l.Scope.Valid() {
			return nil, refuse("invalid_schedule")
		}
		leases = append(leases, l)
	}
	if rows.Err() != nil {
		return nil, refuse("schedule_claim_failed")
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return nil, refuse("schedule_claim_failed")
	}
	return leases, nil
}

func (a Attempts) Complete(ctx context.Context, l Lease, rows json.RawMessage, cause string, delay time.Duration) error {
	if a.Pool == nil || !l.Scope.Valid() || l.Fence < 1 {
		return refuse("schedule_store_unavailable")
	}
	cause = SampleCause(cause)
	if cause != "" {
		rows = nil
	}
	if cause == "" && l.Attempt == "" {
		return refuse("sample_without_attempt")
	}
	if cause == "" && (len(rows) > 5*1024*1024 || !json.Valid(rows)) {
		return refuse("invalid_sample")
	}
	scope, err := json.Marshal(l.Scope)
	if err != nil {
		return refuse("invalid_schedule")
	}
	if l.Attempt != "" {
		var count int
		if cause == "" {
			var mapped []json.RawMessage
			if json.Unmarshal(rows, &mapped) != nil || len(mapped) > 10000 {
				return refuse("invalid_sample")
			}
			count = len(mapped)
		}
		if _, err = a.Pool.Exec(ctx, `UPDATE live_mcp_attempts SET
  outcome=CASE WHEN outcome='undispatched' THEN 'refused' ELSE outcome END,
  finished_at=coalesce(finished_at,clock_timestamp()),refusal_cause=$4,row_count=$5
  WHERE id::text=$1 AND scope=$2::jsonb AND fence=$3`, l.Attempt, scope, l.Fence, cause, count); err != nil {
			return refuse("attempt_finish_failed")
		}
	}
	var payload any
	if rows != nil {
		payload = []byte(rows)
	}
	tag, err := a.Pool.Exec(ctx, `UPDATE live_mcp_binding_state SET
 enabled=CASE WHEN $4 THEN false ELSE enabled END,
 last_payload=CASE WHEN $8 THEN NULL WHEN $5::jsonb IS NOT NULL THEN $5::jsonb ELSE last_payload END,
 payload_at=CASE WHEN $8 THEN NULL WHEN $5::jsonb IS NOT NULL THEN clock_timestamp() ELSE payload_at END,
 sample_sequence=sample_sequence+1,cause=$6,
 failure_count=CASE WHEN $6='' THEN 0 ELSE LEAST(failure_count+1,30) END,
 next_due_at=clock_timestamp()+make_interval(secs=>$7),lease_until=clock_timestamp()
 WHERE binding_id=$1 AND scope=$2::jsonb AND fence=$3 AND enabled
 AND lease_until>clock_timestamp() AND demand_until>clock_timestamp() AND expires_at>clock_timestamp()
 AND ($5::jsonb IS NULL OR EXISTS(SELECT 1 FROM live_mcp_attempts a WHERE a.id::text=$9 AND a.scope=$2::jsonb AND a.fence=$3 AND a.outcome='ok'))`,
		l.Scope.Binding, scope, l.Fence, terminal(cause), payload, cause, max(delay.Seconds(), float64(max(l.Interval, 15))), PurgesSample(cause), l.Attempt)
	if err != nil {
		return refuse("schedule_complete_failed")
	}
	if tag.RowsAffected() != 1 {
		return refuse("schedule_fenced")
	}
	return nil
}

// Demand is authenticated by the dataset path. A heartbeat extends at most 60 s;
// another dashboard viewer cannot create work for an owner-private binding.
func (a Attempts) Demand(ctx context.Context, owner, workspace, binding string) error {
	if a.Pool == nil {
		return refuse("schedule_store_unavailable")
	}
	tag, err := a.Pool.Exec(ctx, `UPDATE live_mcp_binding_state SET demand_until=clock_timestamp()+interval '60 seconds',last_view_at=clock_timestamp()
 WHERE binding_id::text=$1 AND scope->>'owner'=$2 AND workspace_id::text=$3 AND enabled AND expires_at>clock_timestamp()`, binding, owner, workspace)
	if err != nil {
		return refuse("demand_store_unavailable")
	}
	if tag.RowsAffected() != 1 {
		return refuse("demand_refused")
	}
	return nil
}

// Activate accepts only a Core-approved, canonical revision from the decision
// reconciliation path. This method itself grants no Core execution authority.
func (a Attempts) Activate(ctx context.Context, r ld.Revision, workspace string) error {
	approved, _, err := r.Canonical(time.Now())
	if err != nil {
		return refuse("invalid_schedule")
	}
	raw, err := json.Marshal(approved.Scope)
	if err != nil {
		return refuse("invalid_schedule")
	}
	if a.Pool == nil {
		return refuse("schedule_store_unavailable")
	}
	_, err = a.Pool.Exec(ctx, `INSERT INTO live_mcp_binding_state(binding_id,revision,scope,enabled,interval_seconds,fence,lease_until,demand_until,workspace_id,expires_at)
 VALUES($1,$2,$3,true,$4,1,clock_timestamp(),clock_timestamp(),$5,$6)
 ON CONFLICT(binding_id) DO UPDATE SET revision=EXCLUDED.revision,scope=EXCLUDED.scope,enabled=true,interval_seconds=EXCLUDED.interval_seconds,
 fence=live_mcp_binding_state.fence+1,lease_until=clock_timestamp(),demand_until=clock_timestamp(),workspace_id=EXCLUDED.workspace_id,
 expires_at=EXCLUDED.expires_at,last_view_at=clock_timestamp(),last_payload=NULL,payload_at=NULL,cause='',failure_count=0,next_due_at=clock_timestamp()
 WHERE live_mcp_binding_state.revision<EXCLUDED.revision`, approved.Scope.Binding, approved.Scope.Revision, raw, approved.IntervalSeconds, workspace, approved.ExpiresAt)
	if err != nil {
		return refuse("schedule_activation_failed")
	}
	return nil
}
