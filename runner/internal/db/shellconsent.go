package db

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type ShellConsentBinding struct {
	Owner, Login, Session, Launch, Connection, Digest string
	Generation                                        int64
}

// Serialize observations and permanently revoke prior identities, including pending
// challenges. Returning to earlier labels cannot make an old row usable again.
func shellConsentIdentity(ctx context.Context, pool *pgxpool.Pool, b ShellConsentBinding) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	scope, _ := json.Marshal([]any{b.Owner, b.Session, b.Launch, b.Generation})
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, string(scope)); err == nil {
		_, err = tx.Exec(ctx, `UPDATE shell_host_consents SET revoked_at=clock_timestamp() WHERE owner_id=$1 AND session_id=$2 AND launch_generation=$3 AND runtime_generation=$4 AND (connection_id<>$5 OR identity_digest<>$6) AND revoked_at IS NULL`, b.Owner, b.Session, b.Launch, b.Generation, b.Connection, b.Digest)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func finishShellConsentLookup(ctx context.Context, tx pgx.Tx, lookup error) error {
	if lookup != nil && !errors.Is(lookup, pgx.ErrNoRows) {
		return lookup
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return lookup
}

func CreateShellChallenge(ctx context.Context, pool *pgxpool.Pool, b ShellConsentBinding, consent protocol.HostShellConsent, expires time.Time) error {
	raw, err := json.Marshal(consent)
	if err != nil {
		return err
	}
	tx, err := shellConsentIdentity(ctx, pool, b)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `INSERT INTO shell_host_consents(challenge_id,owner_id,login_id,session_id,launch_generation,runtime_generation,connection_id,identity_digest,consent,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, consent.ChallengeID, b.Owner, b.Login, b.Session, b.Launch, b.Generation, b.Connection, b.Digest, raw, expires)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func AcceptShellChallenge(ctx context.Context, pool *pgxpool.Pool, b ShellConsentBinding, id string) (protocol.HostShellConsent, error) {
	var raw []byte
	var consent protocol.HostShellConsent
	err := pool.QueryRow(ctx, `UPDATE shell_host_consents SET accepted_at=clock_timestamp() WHERE challenge_id=$1 AND owner_id=$2 AND login_id=$3 AND session_id=$4 AND launch_generation=$5 AND runtime_generation=$6 AND connection_id=$7 AND identity_digest=$8 AND expires_at>clock_timestamp() AND accepted_at IS NULL AND revoked_at IS NULL RETURNING consent`, id, b.Owner, b.Login, b.Session, b.Launch, b.Generation, b.Connection, b.Digest).Scan(&raw)
	if err != nil {
		return consent, err
	}
	err = json.Unmarshal(raw, &consent)
	return consent, err
}
func GetShellConsent(ctx context.Context, pool *pgxpool.Pool, b ShellConsentBinding) (protocol.HostShellConsent, error) {
	var raw []byte
	var consent protocol.HostShellConsent
	tx, err := shellConsentIdentity(ctx, pool, b)
	if err != nil {
		return consent, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `SELECT consent FROM shell_host_consents WHERE owner_id=$1 AND session_id=$2 AND launch_generation=$3 AND runtime_generation=$4 AND connection_id=$5 AND identity_digest=$6 AND accepted_at IS NOT NULL AND revoked_at IS NULL ORDER BY accepted_at DESC LIMIT 1`, b.Owner, b.Session, b.Launch, b.Generation, b.Connection, b.Digest).Scan(&raw)
	err = finishShellConsentLookup(ctx, tx, err)
	if err != nil {
		return consent, err
	}
	err = json.Unmarshal(raw, &consent)
	return consent, err
}
