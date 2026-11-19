package livedata

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct{ Pool *pgxpool.Pool }

func (p Postgres) Current(ctx context.Context, binding string, revision int64) (Grant, error) {
	if p.Pool == nil {
		return Grant{}, ErrUnavailable
	}
	var g Grant
	var raw []byte
	err := p.Pool.QueryRow(ctx, `SELECT g.scope,g.arguments,g.approved_configuration,g.expires_at,clock_timestamp(),g.unsafe_host_opt_in AND coalesce(os.unsafe_host_opt_in AND os.consent_epoch=(g.scope->>'authority_epoch')::bigint,false)
 FROM live_data_grants g JOIN accounts a ON a.id=g.owner_id JOIN mcp_connections c ON c.id=g.connection_id
 LEFT JOIN live_data_owner_settings os ON os.account_id=a.id
 JOIN live_data_policy p ON p.connection_id=c.id AND p.tool=g.scope->>'tool'
 WHERE g.runner_committed AND g.approved_configuration IS NOT NULL AND g.binding_id::text=$1 AND g.revision=$2 AND g.revoked_at IS NULL AND g.approved_at<=clock_timestamp() AND g.expires_at>clock_timestamp()
 AND a.disabled_at IS NULL AND NOT a.must_change_password AND c.runner_session_id IS NULL AND c.account_id=a.id AND c.status='ok'
 AND p.mode='allow' AND p.hash=g.scope->>'hash'
 AND g.scope->>'owner'=a.id::text AND g.scope->>'binding'=g.binding_id::text AND g.scope->>'connection'=c.id::text
 AND (g.scope->>'revision')::bigint=g.revision
 AND (g.scope->>'owner_epoch')::bigint=a.live_data_epoch AND (g.scope->>'connection_generation')::bigint=c.live_data_generation`, binding, revision).Scan(&raw, &g.Arguments, &g.Configuration, &g.ExpiresAt, &g.CoreTime, &g.UnsafeHostOptIn)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ld.ErrDenied
	}
	if err != nil {
		return Grant{}, ErrUnavailable
	}
	if json.Unmarshal(raw, &g.Scope) != nil {
		return Grant{}, ld.ErrDenied
	}
	if _, err := g.ApprovedRevision(); err != nil {
		return Grant{}, ld.ErrDenied
	}
	return g, nil
}

func (p Postgres) Audit(ctx context.Context, binding string, revision int64, op, outcome string, metadata AuditMetadata) error {
	if p.Pool == nil {
		return ErrUnavailable
	}
	// Refusal audit is safe even for a malformed identifier. Never persist caller
	// strings as causes or operation labels.
	var id any
	if uuidID.MatchString(binding) {
		id = binding
	}
	if op != "mint" && op != "renew" && op != "check" && op != "revoke" {
		return ld.ErrDenied
	}
	switch outcome {
	case "ok", "denied", "unavailable", "unsafe_host", "clock_skew":
	default:
		return ld.ErrDenied
	}
	var installation, runner, epoch any
	if uuidID.MatchString(metadata.Installation) {
		installation = metadata.Installation
	}
	if uuidID.MatchString(metadata.Runner) {
		runner = metadata.Runner
	}
	if metadata.Epoch > 0 {
		epoch = metadata.Epoch
	}
	if metadata.KeyID != "" && !keyID.MatchString(metadata.KeyID) {
		return ld.ErrDenied
	}
	_, err := p.Pool.Exec(ctx, `INSERT INTO live_data_credential_audit(binding_id,revision,operation,outcome,owner_id,installation_id,runner_id,key_id,authority_epoch,attempt_id)
 VALUES($1,$2,$3,$4,(SELECT owner_id FROM live_data_grants WHERE binding_id=$1 AND revision=$2),$5,$6,NULLIF($7,''),$8,$9)`, id, revision, op, outcome, installation, runner, metadata.KeyID, epoch, auditAttempt(metadata.AttemptID))
	return err
}

var uuidID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func (p Postgres) Revoke(ctx context.Context, scope ld.Scope) error {
	if p.Pool == nil {
		return ErrUnavailable
	}
	_, err := p.Pool.Exec(ctx, `UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE binding_id::text=$1 AND revision=$2 AND scope->>'digest'=$3`, scope.Binding, scope.Revision, scope.Digest)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func auditAttempt(id string) any {
	if uuidID.MatchString(id) {
		return id
	}
	return nil
}
