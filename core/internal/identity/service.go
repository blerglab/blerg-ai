package identity

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/signing"
)

const agentTokenTTL = 15 * time.Minute

// signingKeyLockKey is a fixed Postgres advisory lock key, distinct from db's migration lock
// key (§8.2), used to serialize LoadOrGenerateSigningKey across concurrently booting replicas
// so they don't each generate and persist a different signing key for an empty table (IMPORTANT 5).
// 8 bytes ("blergkey") so the literal fits in int64.
const signingKeyLockKey int64 = 0x626c6572676b6579

type Service struct {
	st     db.Store
	signer signing.KeyPair

	// sessionTTLValue backs SetSessionTTL/sessionTTL (human_session.go). Zero means "never
	// configured" — sessionTTL() then falls back to defaultSessionTTL (720h).
	sessionTTLValue time.Duration
}

type AgentTokenInput struct {
	Sub, Aud, Project, OnBehalfOf, Lineage string
	Caps                                   []string

	// ExpiresAt is the token's exp. Zero (the field's zero value, which is what every caller
	// predating user-minted agent tokens passes) keeps the original agentTokenTTL default of
	// 15 minutes — short-lived machine-to-machine tokens. User-minted tokens
	// (CreateAgentToken) set it explicitly to creation + expires_in_days.
	ExpiresAt time.Time
}

// Revocation is one row of the shared revocations table as served by GET /revocations.
// RevokedAt scopes "sub"/"lineage" entries: consumers treat a token as revoked only when its
// iat is at or before RevokedAt (contracts/identity.RevocationSet), so a token minted by a
// later re-login is not caught by a consumer's still-cached entry. "kid" entries are
// unconditional regardless of the timestamp.
type Revocation struct {
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	RevokedAt time.Time `json:"revoked_at"`
}

func NewService(st db.Store, signer signing.KeyPair) *Service {
	return &Service{st: st, signer: signer}
}

// LoadOrGenerateSigningKey returns the persisted active keypair, or generates + persists a new
// one when none exists — so the kid is stable across restarts and JWKS keeps validating tokens
// (§4.1). ed25519 keys round-trip through bytea directly (PublicKey/PrivateKey are []byte).
func LoadOrGenerateSigningKey(ctx context.Context, st db.Store) (signing.KeyPair, error) {
	conn, err := st.Pool().Acquire(ctx)
	if err != nil {
		return signing.KeyPair{}, err
	}
	defer conn.Release()

	// Serialize the check-generate-insert against other replicas booting concurrently against
	// an empty signing_keys table, so they converge on the same key instead of each minting
	// their own (IMPORTANT 5) — the same technique db.Migrate uses, with a distinct lock key.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", signingKeyLockKey); err != nil {
		return signing.KeyPair{}, err
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", signingKeyLockKey); err != nil {
			log.Printf("identity: signing key: advisory unlock: %v", err)
		}
	}()

	var kid string
	var pub, priv []byte
	err = conn.QueryRow(ctx,
		`SELECT kid, public_key, private_key FROM signing_keys WHERE active ORDER BY created_at DESC LIMIT 1`).
		Scan(&kid, &pub, &priv)
	if err == nil {
		return signing.KeyPair{Kid: kid, Pub: pub, Priv: priv}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return signing.KeyPair{}, err
	}
	kp, err := signing.GenerateKeyPair()
	if err != nil {
		return signing.KeyPair{}, err
	}
	if _, err := conn.Exec(ctx,
		`INSERT INTO signing_keys(kid, public_key, private_key) VALUES ($1,$2,$3)`,
		kp.Kid, []byte(kp.Pub), []byte(kp.Priv)); err != nil {
		return signing.KeyPair{}, err
	}
	return kp, nil
}

func (s *Service) MintAgentToken(ctx context.Context, in AgentTokenInput) (string, error) {
	now := time.Now()
	exp := in.ExpiresAt
	if exp.IsZero() {
		exp = now.Add(agentTokenTTL)
	}
	return s.signer.Sign(cid.Claims{
		Sub: in.Sub, Aud: in.Aud, Project: in.Project, Kind: string(cid.Agent),
		Caps: in.Caps, OnBehalfOf: in.OnBehalfOf, Lineage: in.Lineage,
		IssuedAt: now.Unix(), ExpiresAt: exp.Unix(),
	})
}

// Revoke records (kind, value) as revoked as of now. Re-revoking an existing entry moves its
// revoked_at forward rather than leaving the original: every caller means "kill everything
// issued up to this moment", and a stale timestamp would let tokens minted between the two
// revocations survive.
func (s *Service) Revoke(ctx context.Context, kind, value string) error {
	_, err := s.st.Pool().Exec(ctx,
		`INSERT INTO revocations(kind, value, revoked_at) VALUES ($1,$2,now())
		 ON CONFLICT (kind, value) DO UPDATE SET revoked_at = now()`, kind, value)
	return err
}

// Unrevoke removes one revocations row: the inverse of Revoke, used when a disabled account is
// re-enabled (reconcile sees the login back in the org, or the account logs in successfully).
// Without it a "sub" revocation was permanent and re-joining an org was a lockout (audit I-5).
func (s *Service) Unrevoke(ctx context.Context, kind, value string) error {
	_, err := s.st.Pool().Exec(ctx, `DELETE FROM revocations WHERE kind = $1 AND value = $2`, kind, value)
	return err
}

func (s *Service) JWKS(ctx context.Context) (cid.KeySet, error) {
	rows, err := s.st.Pool().Query(ctx, `SELECT kid, public_key FROM signing_keys WHERE active`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ks := cid.KeySet{}
	for rows.Next() {
		var kid string
		var pub []byte
		if err := rows.Scan(&kid, &pub); err != nil {
			return nil, err
		}
		ks[kid] = pub
	}
	return ks, rows.Err()
}

func (s *Service) RevocationSnapshot(ctx context.Context) ([]Revocation, error) {
	rows, err := s.st.Pool().Query(ctx, `SELECT kind, value, revoked_at FROM revocations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revocation
	for rows.Next() {
		var r Revocation
		if err := rows.Scan(&r.Kind, &r.Value, &r.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
