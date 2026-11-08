package db

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

func CreatePersistentShellChallenge(ctx context.Context, pool *pgxpool.Pool, c protocol.ShellConsentV2) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize this owner's admission across concurrent requests and processes.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,269))`, c.Pins.Owner); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM shell_host_consents_v2 WHERE owner_id=$1 AND accepted_at IS NULL AND expires_at<=clock_timestamp()`, c.Pins.Owner); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM shell_host_consents_v2 WHERE owner_id=$1 AND accepted_at IS NULL`, c.Pins.Owner).Scan(&count); err != nil {
		return err
	}
	if count >= 32 {
		return errors.New("shell consent challenge limit")
	}
	_, err = tx.Exec(ctx, `INSERT INTO shell_host_consents_v2(challenge_id,acceptance_id,owner_id,login_id,session_id,consent,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, c.Host.ChallengeID, c.Host.ID, c.Pins.Owner, c.Pins.Login, c.Pins.Session, raw, time.Unix(c.ExpiresAt, 0))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func AcceptPersistentShellChallenge(ctx context.Context, pool *pgxpool.Pool, owner, login, session, challenge string) (protocol.ShellConsentV2, error) {
	var c protocol.ShellConsentV2
	var raw []byte
	err := pool.QueryRow(ctx, `UPDATE shell_host_consents_v2 SET accepted_at=clock_timestamp() WHERE challenge_id=$1 AND owner_id=$2 AND login_id=$3 AND session_id=$4 AND accepted_at IS NULL AND withdrawn_at IS NULL AND expires_at>clock_timestamp() RETURNING consent`, challenge, owner, login, session).Scan(&raw)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}
func ReadPersistentShellConsent(ctx context.Context, pool *pgxpool.Pool, owner, login, session, id string) (protocol.ShellConsentV2, error) {
	var c protocol.ShellConsentV2
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT consent FROM shell_host_consents_v2 WHERE acceptance_id=$1 AND owner_id=$2 AND login_id=$3 AND session_id=$4 AND accepted_at IS NOT NULL AND withdrawn_at IS NULL`, id, owner, login, session).Scan(&raw)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}
func WithdrawPersistentShellConsent(ctx context.Context, pool *pgxpool.Pool, owner, login, session, id string) error {
	_, err := WithdrawPersistentShellConsentMetadata(ctx, pool, owner, login, session, id)
	return err
}

// Metadata returned here is for denial/audit only, including after a restart.
func WithdrawPersistentShellConsentMetadata(ctx context.Context, pool *pgxpool.Pool, owner, login, session, id string) (protocol.ShellConsentV2, error) {
	c, _, err := WithdrawPersistentShellConsentOnce(ctx, pool, owner, login, session, id)
	return c, err
}

// WithdrawPersistentShellConsentOnce serializes repeats so only the first
// committed withdrawal emits an audit event, including across runner restarts.
func WithdrawPersistentShellConsentOnce(ctx context.Context, pool *pgxpool.Pool, owner, login, session, id string) (protocol.ShellConsentV2, bool, error) {
	return WithdrawPersistentShellConsentAudited(ctx, pool, owner, login, session, id, nil)
}

// WithdrawPersistentShellConsentAudited commits an opaque audit snapshot with
// the denial. A process crash before the API's optional log cannot lose it.
func WithdrawPersistentShellConsentAudited(ctx context.Context, pool *pgxpool.Pool, owner, login, session, id string, snapshot func(protocol.ShellConsentV2) ([]byte, error)) (protocol.ShellConsentV2, bool, error) {
	var c protocol.ShellConsentV2
	tx, err := pool.Begin(ctx)
	if err != nil {
		return c, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var raw []byte
	var withdrawn bool
	var audit []byte
	err = tx.QueryRow(ctx, `SELECT consent,withdrawn_at IS NOT NULL,withdrawal_audit FROM shell_host_consents_v2 WHERE acceptance_id=$1 AND owner_id=$2 AND login_id=$3 AND session_id=$4 AND accepted_at IS NOT NULL FOR UPDATE`, id, owner, login, session).Scan(&raw, &withdrawn, &audit)
	if err != nil {
		return c, false, err
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, false, err
	}
	if len(audit) == 0 {
		if snapshot != nil {
			audit, err = snapshot(c)
		} else {
			audit, err = json.Marshal(map[string]any{"event": "consent_withdraw", "actor_id": c.Pins.Owner, "login_id": c.Pins.Login, "session_id": c.Pins.Session, "custody_digest": c.PinsDigest(), "custody_kind": "consent_pins", "access_epoch": 0, "outcome": "withdrawn"})
		}
		if err != nil || !json.Valid(audit) {
			if err == nil {
				err = errors.New("invalid withdrawal audit")
			}
			return c, false, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE shell_host_consents_v2 SET withdrawn_at=coalesce(withdrawn_at,clock_timestamp()),withdrawal_audit=coalesce(withdrawal_audit,$2) WHERE acceptance_id=$1`, id, audit); err != nil {
		return c, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return c, false, err
	}
	return c, !withdrawn, nil
}

// ListPersistentShellConsents is denial metadata only. Pages stay bounded even
// after a restart; no returned row can replace affirmative process memory.
type PersistentConsentStatus struct {
	Consent   protocol.ShellConsentV2
	Withdrawn bool
}

func ListPersistentShellConsents(ctx context.Context, pool *pgxpool.Pool, owner, login, session, after string) ([]PersistentConsentStatus, string, error) {
	rows, err := pool.Query(ctx, `SELECT consent,withdrawn_at IS NOT NULL FROM shell_host_consents_v2 WHERE owner_id=$1 AND login_id=$2 AND session_id=$3 AND acceptance_id>$4 AND accepted_at IS NOT NULL AND octet_length(consent::text)<=8192 ORDER BY acceptance_id LIMIT 33`, owner, login, session, after)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	result := []PersistentConsentStatus{}
	for rows.Next() {
		var raw []byte
		var c protocol.ShellConsentV2
		var withdrawn bool
		if err := rows.Scan(&raw, &withdrawn); err != nil {
			return nil, "", err
		}
		if json.Unmarshal(raw, &c) != nil {
			return nil, "", errors.New("invalid consent metadata")
		}
		result = append(result, PersistentConsentStatus{Consent: c, Withdrawn: withdrawn})
	}
	if rows.Err() != nil {
		return nil, "", rows.Err()
	}
	next := ""
	if len(result) > 32 {
		result = result[:32]
		next = result[31].Consent.Host.ID
	}
	return result, next, nil
}
