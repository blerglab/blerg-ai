// Package livepoll implements an in-process dispatch boundary. It registers no
// HTTP/MCP execution handlers. The scheduler remains behind the disabled rollout gate.
package livepoll

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Attempts struct{ Pool *pgxpool.Pool }

// Shared across replicas, held only during bounded state transitions.
const attemptLock int64 = 0x626c673531346d63

func (a Attempts) begin(ctx context.Context) (pgx.Tx, error) {
	if a.Pool == nil {
		return nil, ld.ErrDenied
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, ld.ErrDenied
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, attemptLock); err != nil {
		_ = tx.Rollback(ctx)
		return nil, ld.ErrDenied
	}
	return tx, nil
}

func (a Attempts) ObserveEpoch(ctx context.Context, scope ld.Scope) error {
	if a.Pool == nil || !scope.Valid() {
		return refuse("epoch_unavailable")
	}
	var epoch int64
	err := a.Pool.QueryRow(ctx, `SELECT epoch FROM live_mcp_authority_epochs WHERE installation_id=$1 AND runner_id=$2`, scope.Installation, scope.Runner).Scan(&epoch)
	if err == nil {
		if epoch > scope.AuthorityEpoch {
			return refuse("authority_epoch_rollback")
		}
		if epoch == scope.AuthorityEpoch {
			return nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return refuse("epoch_store_unavailable")
	}
	err = a.Pool.QueryRow(ctx, `INSERT INTO live_mcp_authority_epochs(installation_id,runner_id,epoch) VALUES($1,$2,$3)
 ON CONFLICT(installation_id,runner_id) DO UPDATE SET epoch=EXCLUDED.epoch WHERE live_mcp_authority_epochs.epoch<=EXCLUDED.epoch RETURNING epoch`, scope.Installation, scope.Runner, scope.AuthorityEpoch).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return refuse("authority_epoch_rollback")
	}
	if err != nil {
		return refuse("epoch_store_unavailable")
	}
	return nil
}

// Create returns only after the pre-dispatch audit row commits.
func (a Attempts) Create(ctx context.Context, scope ld.Scope, fence int64) (string, error) {
	if !scope.Valid() || fence < 1 {
		return "", refuse("invalid_attempt")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return "", refuse("attempt_store_unavailable")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", refuse("invalid_attempt")
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO live_mcp_attempts(binding_id,revision,owner_id,connection_id,scope,fence,due_at,expires_at)
 SELECT b.binding_id,b.revision,$3::uuid,$4::uuid,b.scope,b.fence,clock_timestamp(),LEAST(b.lease_until,b.demand_until,clock_timestamp()+interval '30 seconds')
 FROM live_mcp_binding_state b WHERE b.binding_id=$1 AND b.revision=$2 AND b.scope=$5::jsonb AND b.fence=$6 AND b.enabled
 AND b.lease_until>clock_timestamp() AND b.demand_until>clock_timestamp()
 AND (SELECT count(*) FROM live_mcp_attempts WHERE outcome='undispatched' AND expires_at>clock_timestamp())<80
 AND NOT EXISTS(SELECT 1 FROM live_mcp_attempts a WHERE a.binding_id=b.binding_id AND (a.outcome='undispatched' AND a.expires_at>clock_timestamp() OR a.outcome='dispatched' AND a.dispatched_at>clock_timestamp()-interval '30 seconds'))
 AND (SELECT count(*) FROM live_mcp_binding_state WHERE enabled AND expires_at>clock_timestamp() AND scope->>'owner'=$3::uuid::text)<=20
 RETURNING id::text`, scope.Binding, scope.Revision, scope.Owner, scope.Connection, raw, fence).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", refuse("attempt_not_eligible")
	}
	if err != nil {
		return "", refuse("attempt_store_unavailable")
	}
	if err = tx.Commit(ctx); err != nil {
		return "", refuse("attempt_commit_failed")
	}
	return id, nil
}

// Consume atomically uses an unused committed row. A successful commit must
// precede the provider call. Consumed attempt IDs are never reset or reused.
func (a Attempts) Consume(ctx context.Context, id string, scope ld.Scope, approvedInterval int) error {
	if !scope.Valid() || approvedInterval < 15 || approvedInterval > 86400 {
		return refuse("invalid_scope")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return refuse("attempt_store_unavailable")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	raw, err := json.Marshal(scope)
	if err != nil {
		return refuse("invalid_scope")
	}
	var used string
	err = tx.QueryRow(ctx, `UPDATE live_mcp_attempts a SET dispatched_at=clock_timestamp(),outcome='dispatched'
 FROM live_mcp_binding_state b WHERE a.id::text=$1 AND a.binding_id::text=$6 AND a.revision=$7 AND a.owner_id::text=$8 AND a.connection_id::text=$9 AND a.binding_id=b.binding_id AND a.revision=b.revision AND a.scope=b.scope AND a.scope=$2::jsonb
 AND b.enabled AND b.interval_seconds>=$10 AND a.fence=b.fence AND b.lease_until>clock_timestamp() AND b.demand_until>clock_timestamp()
 AND NOT EXISTS(SELECT 1 FROM live_mcp_attempts prior WHERE prior.binding_id=a.binding_id AND prior.dispatched_at>clock_timestamp()-make_interval(secs=>GREATEST(b.interval_seconds,$10)))
 AND a.outcome='undispatched' AND a.dispatched_at IS NULL AND a.due_at<=clock_timestamp() AND a.expires_at>clock_timestamp()
 AND EXISTS(SELECT 1 FROM live_mcp_authority_epochs e WHERE e.installation_id::text=$3 AND e.runner_id::text=$4 AND e.epoch=$5)
 AND (SELECT count(*) FROM live_mcp_attempts x WHERE x.dispatched_at>clock_timestamp()-interval '1 minute' AND x.owner_id=a.owner_id)<60
 AND (SELECT count(*) FROM live_mcp_attempts x WHERE x.dispatched_at>clock_timestamp()-interval '1 minute' AND x.connection_id=a.connection_id)<30
 AND (SELECT count(*) FROM live_mcp_attempts x WHERE x.outcome='dispatched' AND x.dispatched_at>clock_timestamp()-interval '30 seconds')<4
 AND (SELECT count(*) FROM live_mcp_attempts x WHERE x.outcome='dispatched' AND x.dispatched_at>clock_timestamp()-interval '30 seconds' AND x.owner_id=a.owner_id)<2
 AND (SELECT count(*) FROM live_mcp_attempts x WHERE x.outcome='dispatched' AND x.dispatched_at>clock_timestamp()-interval '30 seconds' AND x.connection_id=a.connection_id)<1
 RETURNING a.id::text`, id, raw, scope.Installation, scope.Runner, scope.AuthorityEpoch, scope.Binding, scope.Revision, scope.Owner, scope.Connection, approvedInterval).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		return refuse("attempt_refused")
	}
	if err != nil {
		return refuse("attempt_store_unavailable")
	}
	if err = tx.Commit(ctx); err != nil {
		return refuse("attempt_commit_failed")
	}
	return nil
}

func (a Attempts) Finish(ctx context.Context, id string, ok bool) error {
	if a.Pool == nil {
		return refuse("attempt_store_unavailable")
	}
	outcome := "failed"
	if ok {
		outcome = "ok"
	}
	tag, err := a.Pool.Exec(ctx, `UPDATE live_mcp_attempts SET outcome=$2,finished_at=clock_timestamp() WHERE id::text=$1 AND outcome='dispatched'`, id, outcome)
	if err != nil || tag.RowsAffected() != 1 {
		return refuse("attempt_finish_failed")
	}
	return nil
}

func refuse(cause string) error {
	failurelog.Default.Event("live_poll:"+cause, slog.LevelWarn, "component", "runner", "operation", "live_poll", "cause", cause)
	return &Failure{cause: cause}
}

func (a Attempts) Reconcile(ctx context.Context) error {
	if a.Pool == nil {
		return refuse("attempt_store_unavailable")
	}
	_, err := a.Pool.Exec(ctx, `UPDATE live_mcp_attempts SET outcome='interrupted',finished_at=clock_timestamp() WHERE outcome='dispatched' AND dispatched_at<=clock_timestamp()-interval '30 seconds'`)
	if err != nil {
		return refuse("attempt_reconcile_failed")
	}
	tag, err := a.Pool.Exec(ctx, `UPDATE live_mcp_attempts SET outcome='refused',refusal_cause='attempt_expired',finished_at=clock_timestamp() WHERE outcome='undispatched' AND expires_at<=clock_timestamp()`)
	if err != nil {
		return refuse("attempt_reconcile_failed")
	}
	if tag.RowsAffected() > 0 {
		_ = refuse("attempt_expired")
	}
	return nil
}

// Maintain enforces retention even while polling is disabled. No provider call.
func (a Attempts) Maintain(ctx context.Context) error {
	if a.Pool == nil {
		return refuse("retention_unavailable")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return refuse("retention_unavailable")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var causes []string
	for _, statement := range []struct{ query, cause string }{
		{`UPDATE live_mcp_binding_state SET enabled=false,fence=fence+1,last_payload=NULL,payload_at=NULL,cause='grant_expired',sample_sequence=sample_sequence+1 WHERE expires_at<=clock_timestamp() AND (enabled OR last_payload IS NOT NULL)`, "grant_expired"},
		{`UPDATE live_mcp_binding_state SET enabled=false,fence=fence+1,last_payload=NULL,payload_at=NULL,cause='no_view_auto_pause',sample_sequence=sample_sequence+1 WHERE enabled AND last_view_at<=clock_timestamp()-interval '7 days'`, "no_view_auto_pause"},
		{`UPDATE live_mcp_binding_state SET last_payload=NULL,payload_at=NULL,cause='sample_expired',sample_sequence=sample_sequence+1 WHERE payload_at<=clock_timestamp()-interval '24 hours'`, "sample_expired"},
		{`DELETE FROM live_mcp_attempts WHERE id IN (SELECT id FROM live_mcp_attempts WHERE created_at<clock_timestamp()-interval '180 days' LIMIT 1000)`, ""},
		{`DELETE FROM live_mcp_decision_outbox WHERE ask_id IN (SELECT ask_id FROM live_mcp_decision_outbox WHERE delivered AND created_at<clock_timestamp()-interval '180 days' LIMIT 1000)`, ""},
	} {
		tag, execErr := tx.Exec(ctx, statement.query)
		if execErr != nil {
			return refuse("retention_unavailable")
		}
		if tag.RowsAffected() > 0 && statement.cause != "" {
			causes = append(causes, statement.cause)
		}
	}
	if tx.Commit(ctx) != nil {
		return refuse("retention_commit_failed")
	}
	for _, cause := range causes {
		_ = refuse(cause)
	}
	return nil
}

// RevokeRevision makes observed tool loss sticky even if Core's revoke request
// fails. An expired demand/lease is no authority to keep a known bad revision.
// A newer owner-approved scope is unaffected; repeated fences are idempotent.
func (a Attempts) RevokeRevision(ctx context.Context, scope ld.Scope, cause string) error {
	if cause != "connection_needs_sign_in" && cause != "grant_expired" {
		cause = "tool_changed"
	}
	if a.Pool == nil || !scope.Valid() {
		return refuse("attempt_store_unavailable")
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return refuse("invalid_scope")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return refuse("attempt_revoke_failed")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `UPDATE live_mcp_binding_state SET enabled=false,last_payload=NULL,payload_at=NULL,cause=$3,
 fence=fence+1,lease_until=clock_timestamp(),demand_until=clock_timestamp(),sample_sequence=sample_sequence+1
 WHERE binding_id::text=$1 AND scope=$2::jsonb AND (enabled OR cause<>$3 OR last_payload IS NOT NULL)`, scope.Binding, raw, cause)
	if err != nil {
		return refuse("attempt_revoke_failed")
	}
	// Undispatched receipts cannot remain retryable or block a newer approval.
	if _, err = tx.Exec(ctx, `UPDATE live_mcp_attempts SET outcome='refused',finished_at=clock_timestamp(),refusal_cause=$2
 WHERE scope=$1::jsonb AND outcome='undispatched'`, raw, cause); err != nil {
		return refuse("attempt_revoke_failed")
	}
	if err = tx.Commit(ctx); err != nil {
		return refuse("attempt_revoke_failed")
	}
	return nil
}
