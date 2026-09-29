// Package credentials implements the per-account encrypted credential vault (spec §5):
// accounts store an opaque credential per engine (e.g. an API token for "claude"), encrypted at
// rest via a keybackend.Backend. Only the encrypting/decrypting Service ever sees plaintext;
// everything else (List's CredentialSummary, the HTTP layer in core/internal/api) only ever
// sees {engine, updated_at} metadata.
package credentials

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
)

// ErrNotFound is returned by Fetch when accountID has no stored credential for engine.
var ErrNotFound = errors.New("credentials: not found")

// CredentialSummary is what List returns: metadata only, never ciphertext or plaintext.
type CredentialSummary struct {
	Engine    string
	UpdatedAt time.Time
}

// Service is the credentials.Service seam described in the plan: it owns the
// encrypt/decrypt/persist lifecycle for the user_credentials table, via a keybackend.Backend for
// key material and a db.Store for persistence.
type Service struct {
	st      db.Store
	backend keybackend.Backend
}

// NewService wires a Service against st (persistence) and backend (envelope encryption).
func NewService(st db.Store, backend keybackend.Backend) *Service {
	return &Service{st: st, backend: backend}
}

// Store encrypts plaintext via the backend and upserts the (account_id, engine) row —
// PRIMARY KEY (account_id, engine) means a second Store for the same account+engine replaces
// the existing credential rather than erroring or creating a duplicate row.
func (s *Service) Store(ctx context.Context, accountID, engine string, plaintext []byte) error {
	ciphertext, keyID, err := s.backend.Encrypt(ctx, plaintext)
	if err != nil {
		return err
	}
	_, err = s.st.Pool().Exec(ctx,
		`INSERT INTO user_credentials (account_id, engine, ciphertext, key_id, updated_at)
		 VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (account_id, engine)
		 DO UPDATE SET ciphertext = EXCLUDED.ciphertext, key_id = EXCLUDED.key_id, updated_at = now()`,
		accountID, engine, ciphertext, keyID)
	return err
}

// List returns metadata only ({engine, updated_at}) for every credential accountID has stored —
// never ciphertext or plaintext (spec §5: GET /api/credentials must never return secret
// material).
func (s *Service) List(ctx context.Context, accountID string) ([]CredentialSummary, error) {
	rows, err := s.st.Pool().Query(ctx,
		`SELECT engine, updated_at FROM user_credentials WHERE account_id = $1 ORDER BY engine`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CredentialSummary
	for rows.Next() {
		var c CredentialSummary
		if err := rows.Scan(&c.Engine, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Fetch decrypts and returns accountID's plaintext credential for engine — the ONLY place in
// this package (or, by design, anywhere outside it) plaintext credential material comes back
// into memory. Callers (core/internal/api's internal-only fetch handler) must gate this behind
// their own component-authentication and live-session checks BEFORE calling it; Fetch itself
// does no authorization — it is a pure lookup+decrypt, trusting the caller entirely. Returns
// ErrNotFound if accountID has no stored credential for engine.
func (s *Service) Fetch(ctx context.Context, accountID, engine string) ([]byte, error) {
	var ciphertext []byte
	var keyID string
	err := s.st.Pool().QueryRow(ctx,
		`SELECT ciphertext, key_id FROM user_credentials WHERE account_id = $1 AND engine = $2`,
		accountID, engine).Scan(&ciphertext, &keyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.backend.Decrypt(ctx, ciphertext, keyID)
}

// Delete removes accountID's credential for engine, if any. Deleting a nonexistent row is a
// no-op, not an error — consistent with the rest of the codebase's idempotent-delete convention
// (e.g. RevokeHumanSession).
func (s *Service) Delete(ctx context.Context, accountID, engine string) error {
	_, err := s.st.Pool().Exec(ctx,
		`DELETE FROM user_credentials WHERE account_id = $1 AND engine = $2`, accountID, engine)
	return err
}
