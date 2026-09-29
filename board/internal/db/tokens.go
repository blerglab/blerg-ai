package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrTokenInvalid = errors.New("invalid token")

type Token struct {
	ID           string     `json:"id"`
	BoardID      *string    `json:"board_id"`
	Kind         string     `json:"kind"`
	Label        string     `json:"label"`
	Capabilities []string   `json:"capabilities"`
	LineageID    string     `json:"lineage_id"`
	ExpiresAt    *time.Time `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

// HasCap reports whether the token carries a capability.
func (t Token) HasCap(capability string) bool {
	for _, c := range t.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

func newRawToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "blergb_" + hex.EncodeToString(b), nil
}

//nolint:gosec // G101: a SQL column list, not a credential
const tokenCols = `id, board_id, kind, label, capabilities, lineage_id,
	expires_at, revoked_at, last_used_at, created_at`

func scanToken(row pgx.Row) (Token, error) {
	var t Token
	err := row.Scan(&t.ID, &t.BoardID, &t.Kind, &t.Label, &t.Capabilities, &t.LineageID,
		&t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrTokenInvalid
	}
	return t, err
}

// MintToken creates a token and returns the raw secret exactly once.
// boardID nil ⇒ service key. ttl 0 ⇒ no expiry (service keys).
func MintToken(ctx context.Context, pool *pgxpool.Pool, boardID *string, kind, label string, caps []string, ttl time.Duration) (Token, string, error) {
	raw, err := newRawToken()
	if err != nil {
		return Token{}, "", err
	}
	var expires *time.Time
	if ttl > 0 {
		e := time.Now().Add(ttl)
		expires = &e
	}
	t, err := scanToken(pool.QueryRow(ctx, `
		INSERT INTO tokens (board_id, kind, label, token_hash, capabilities, lineage_id, expires_at)
		VALUES ($1,$2,$3,$4,$5, gen_random_uuid(), $6)
		RETURNING `+tokenCols,
		boardID, kind, label, hashToken(raw), caps, expires))
	if err != nil {
		return Token{}, "", err
	}
	return t, raw, nil
}

// ValidateToken resolves a raw token to a live row, stamping last_used_at.
func ValidateToken(ctx context.Context, pool *pgxpool.Pool, raw string) (Token, error) {
	t, err := scanToken(pool.QueryRow(ctx, `
		UPDATE tokens SET last_used_at = now()
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR now() < expires_at)
		RETURNING `+tokenCols, hashToken(raw)))
	if err != nil {
		return Token{}, ErrTokenInvalid
	}
	return t, nil
}

// MintTokenForLineage mints a token under a caller-chosen lineage rather than
// a fresh one. Standing agents are referenced by token_lineage_id (see
// standing_agents.token_lineage_id) so the re-entrancy guard can recognise a
// session's writes as "this agent's own" regardless of which token row the
// worker minted for that particular session.
func MintTokenForLineage(ctx context.Context, pool *pgxpool.Pool, boardID *string, lineageID, kind, label string, caps []string, ttl time.Duration) (Token, string, error) {
	raw, err := newRawToken()
	if err != nil {
		return Token{}, "", err
	}
	var expires *time.Time
	if ttl > 0 {
		e := time.Now().Add(ttl)
		expires = &e
	}
	t, err := scanToken(pool.QueryRow(ctx, `
		INSERT INTO tokens (board_id, kind, label, token_hash, capabilities, lineage_id, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING `+tokenCols,
		boardID, kind, label, hashToken(raw), caps, lineageID, expires))
	if err != nil {
		return Token{}, "", err
	}
	return t, raw, nil
}

// RefreshToken issues a successor sharing the lineage and revokes the
// predecessor after a 5-minute overlap (so in-flight requests don't fail).
func RefreshToken(ctx context.Context, pool *pgxpool.Pool, current Token, ttl time.Duration) (Token, string, error) {
	raw, err := newRawToken()
	if err != nil {
		return Token{}, "", err
	}
	var expires *time.Time
	if ttl > 0 {
		e := time.Now().Add(ttl)
		expires = &e
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Token{}, "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	t, err := scanToken(tx.QueryRow(ctx, `
		INSERT INTO tokens (board_id, kind, label, token_hash, capabilities, lineage_id, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING `+tokenCols,
		current.BoardID, current.Kind, current.Label, hashToken(raw),
		current.Capabilities, current.LineageID, expires))
	if err != nil {
		return Token{}, "", err
	}
	// Predecessor stays valid for a short overlap window, then dies.
	if _, err := tx.Exec(ctx, `
		UPDATE tokens SET expires_at = LEAST(coalesce(expires_at, now() + interval '5 minutes'),
			now() + interval '5 minutes')
		WHERE id = $1`, current.ID); err != nil {
		return Token{}, "", err
	}
	return t, raw, tx.Commit(ctx)
}

func RevokeToken(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx,
		`UPDATE tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetToken loads one token row by id (revoked or not); ErrNotFound if absent.
func GetToken(ctx context.Context, pool *pgxpool.Pool, id string) (Token, error) {
	t, err := scanToken(pool.QueryRow(ctx, `SELECT `+tokenCols+` FROM tokens WHERE id = $1`, id))
	if errors.Is(err, ErrTokenInvalid) {
		return Token{}, ErrNotFound
	}
	return t, err
}

func ListTokens(ctx context.Context, pool *pgxpool.Pool) ([]Token, error) {
	rows, err := pool.Query(ctx, `SELECT `+tokenCols+` FROM tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
