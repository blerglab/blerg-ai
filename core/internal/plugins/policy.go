package plugins

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/jackc/pgx/v5"
)

// Policy seeds once. Subsequent Core replicas and environment edits never overwrite it.
func (s *Service) Policy(ctx context.Context) (pluginspec.Policy, error) {
	p, err := readPolicy(ctx, s.st.Pool(), false)
	if !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	seed := pluginspec.PolicyFromAllowlist(s.allow)
	if err := seed.Normalize(); err != nil {
		return p, err
	}
	raw, err := json.Marshal(seed.Marketplaces)
	if err != nil {
		return p, err
	}
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return p, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `INSERT INTO plugin_marketplace_policy(singleton,revision,marketplaces,any_marketplace) VALUES(true,1,$1,$2) ON CONFLICT DO NOTHING RETURNING policy_id::text`, raw, seed.AnyMarketplace).Scan(&seed.PolicyID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	if err == nil {
		body, _ := json.Marshal(seed)
		if _, err = tx.Exec(ctx, `INSERT INTO plugin_marketplace_audit(revision,after_policy) VALUES(1,$1)`, body); err != nil {
			return p, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return p, err
	}
	return readPolicy(ctx, s.st.Pool(), false)
}

type policyReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readPolicy(ctx context.Context, q policyReader, lock bool) (pluginspec.Policy, error) {
	p := pluginspec.Policy{Version: 1}
	var raw []byte
	query := `SELECT policy_id::text,revision,marketplaces,any_marketplace,COALESCE(updated_by::text,''),updated_at FROM plugin_marketplace_policy WHERE singleton`
	if lock {
		query += ` FOR SHARE`
	}
	err := q.QueryRow(ctx, query).Scan(&p.PolicyID, &p.Revision, &raw, &p.AnyMarketplace, &p.UpdatedBy, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p.Marketplaces); err != nil {
		return p, err
	}
	err = p.Normalize()
	return p, err
}

// SetPolicy commits both state and an immutable before/after audit row. Recheck the
// exact admin login under row locks; a stale admin JWT alone grants no authority.
func (s *Service) SetPolicy(ctx context.Context, actor, login string, expected int64, p pluginspec.Policy) (pluginspec.Policy, error) {
	if err := p.Normalize(); err != nil {
		return p, &ValidationError{err.Error()}
	}
	if _, err := s.Policy(ctx); err != nil {
		return p, err
	}
	tx, err := s.st.Pool().Begin(ctx)
	if err != nil {
		return p, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return p, err
	}
	// Serializes policy edits and plugin writes, without holding identity locks while waiting.
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT revision FROM plugin_marketplace_policy WHERE singleton FOR UPDATE`).Scan(&revision); err != nil {
		return p, err
	}
	if revision != expected {
		return p, ErrConflict
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('human_sessions:' || $1::text,0))`, actor); err != nil {
		return p, err
	}
	var role string
	if err = tx.QueryRow(ctx, `SELECT role FROM accounts WHERE id=$1 AND disabled_at IS NULL AND NOT must_change_password FOR UPDATE`, actor).Scan(&role); err != nil {
		return p, err
	}
	if role != "admin" {
		return p, ErrHumanProof
	}
	var proof string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM human_sessions WHERE account_id=$1 AND chain_id=$2 AND revoked_at IS NULL AND expires_at>clock_timestamp() ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, actor, login).Scan(&proof); err != nil {
		return p, err
	}
	before, err := readPolicy(ctx, tx, false)
	if err != nil {
		return p, err
	}
	raw, _ := json.Marshal(p.Marketplaces)
	p.PolicyID = before.PolicyID
	p.Version, p.Revision, p.UpdatedBy = 1, revision+1, actor
	if err = tx.QueryRow(ctx, `UPDATE plugin_marketplace_policy SET revision=$1,marketplaces=$2,any_marketplace=$3,updated_by=$4,updated_at=clock_timestamp() WHERE singleton AND EXISTS(SELECT 1 FROM human_sessions WHERE id=$5 AND revoked_at IS NULL AND expires_at>clock_timestamp()) RETURNING updated_at`, p.Revision, raw, p.AnyMarketplace, actor, proof).Scan(&p.UpdatedAt); err != nil {
		return p, err
	}
	oldJSON, _ := json.Marshal(before)
	newJSON, _ := json.Marshal(p)
	if _, err = tx.Exec(ctx, `INSERT INTO plugin_marketplace_audit(revision,actor_id,login_id,before_policy,after_policy) VALUES($1,$2,$3,$4,$5)`, p.Revision, actor, login, oldJSON, newJSON); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}
