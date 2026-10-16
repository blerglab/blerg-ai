package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrTokenInvalid is returned by ValidateBoardToken when the token is not
// found, has been revoked, or has expired.
var ErrTokenInvalid = errors.New("board token invalid")

// ErrTokenRevoked is the ErrTokenInvalid of a token that exists but was
// revoked or has expired: the caller once held a real credential, which is
// worth telling it (a session that holds one may be handed a fresh token,
// see protocol.SessionToken). errors.Is(err, ErrTokenInvalid) holds for it.
var ErrTokenRevoked = fmt.Errorf("%w: revoked or expired", ErrTokenInvalid)

// BoardTokenRow is a row from the board_tokens table.
type BoardTokenRow struct {
	ID           string
	BoardID      string
	SessionID    string
	Capabilities []string
	ExpiresAt    time.Time
	RevokedAt    *time.Time
	CreatedAt    time.Time
}

// MintBoardToken generates a cryptographically random 32-byte token, stores
// its SHA-256 hash in board_tokens, and returns the hex-encoded raw token
// alongside the inserted row. The raw token is never persisted — only the hash
// is stored. expires_at is set to now()+ttl (a negative ttl produces a token
// that is already expired).
func MintBoardToken(ctx context.Context, pool *pgxpool.Pool, boardID, sessionID string, caps []string, ttl time.Duration) (raw string, row BoardTokenRow, err error) {
	// Generate 32 random bytes.
	rawBytes := make([]byte, 32)
	if _, err = rand.Read(rawBytes); err != nil {
		return "", BoardTokenRow{}, fmt.Errorf("MintBoardToken generate random bytes: %w", err)
	}

	// Hex-encode the raw token for the caller.
	raw = hex.EncodeToString(rawBytes)

	// Store SHA-256 hash (never the raw bytes).
	hash := sha256.Sum256(rawBytes)
	tokenHash := hash[:]

	expiresAt := time.Now().Add(ttl)

	err = pool.QueryRow(ctx, `
		INSERT INTO board_tokens (board_id, session_id, token_hash, capabilities, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, COALESCE(board_id::text, ''), session_id, capabilities, expires_at, revoked_at, created_at
	`, boardID, sessionID, tokenHash, caps, expiresAt).Scan(
		&row.ID, &row.BoardID, &row.SessionID, &row.Capabilities,
		&row.ExpiresAt, &row.RevokedAt, &row.CreatedAt,
	)
	if err != nil {
		return "", BoardTokenRow{}, fmt.Errorf("MintBoardToken insert: %w", err)
	}
	return raw, row, nil
}

// MintSessionToken is MintBoardToken for a session that belongs to no board:
// the per-session messaging credential every spawned session gets in place of
// the daemon master token. board_id is NULL; ValidateBoardToken reports it as "".
// It is revoked with the session's other tokens by RevokeBoardTokensForSession
// (session end) and expires after ttl regardless.
func MintSessionToken(ctx context.Context, pool *pgxpool.Pool, sessionID string, caps []string, ttl time.Duration) (string, error) {
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		return "", fmt.Errorf("MintSessionToken generate random bytes: %w", err)
	}
	hash := sha256.Sum256(rawBytes)
	if _, err := pool.Exec(ctx, `
		INSERT INTO board_tokens (board_id, session_id, token_hash, capabilities, expires_at)
		VALUES (NULL, $1, $2, $3, $4)`, sessionID, hash[:], caps, time.Now().Add(ttl)); err != nil {
		return "", fmt.Errorf("MintSessionToken insert: %w", err)
	}
	return hex.EncodeToString(rawBytes), nil
}

// SlideBoardTokenExpiry moves a live token's expires_at out to now()+ttl when less than half of
// ttl is left. A session's messaging token is minted with a fixed life, but a session may run for
// days; every use of the token while the session lives pushes its expiry along, and session end
// still revokes it (RevokeBoardTokensForSession). A revoked or already-distant token is left as it
// is. Returns whether the row was extended.
func SlideBoardTokenExpiry(ctx context.Context, pool *pgxpool.Pool, id string, ttl time.Duration) (bool, error) {
	now := time.Now()
	tag, err := pool.Exec(ctx, `
		UPDATE board_tokens SET expires_at = $2
		 WHERE id = $1 AND revoked_at IS NULL AND expires_at < $3`,
		id, now.Add(ttl), now.Add(ttl/2))
	if err != nil {
		return false, fmt.Errorf("SlideBoardTokenExpiry: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ValidateBoardToken hashes the raw hex token and looks it up by token_hash.
// Returns ErrTokenInvalid if not found, ErrTokenRevoked (which is also
// ErrTokenInvalid) if revoked or expired. A session token
// (no board) comes back with BoardID == "".
func ValidateBoardToken(ctx context.Context, pool *pgxpool.Pool, raw string) (BoardTokenRow, error) {
	rawBytes, err := hex.DecodeString(raw)
	if err != nil {
		return BoardTokenRow{}, ErrTokenInvalid
	}

	hash := sha256.Sum256(rawBytes)
	tokenHash := hash[:]

	var row BoardTokenRow
	err = pool.QueryRow(ctx, `
		SELECT id, COALESCE(board_id::text, ''), session_id, capabilities, expires_at, revoked_at, created_at
		  FROM board_tokens
		 WHERE token_hash = $1
	`, tokenHash).Scan(
		&row.ID, &row.BoardID, &row.SessionID, &row.Capabilities,
		&row.ExpiresAt, &row.RevokedAt, &row.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return BoardTokenRow{}, ErrTokenInvalid
	}
	if err != nil {
		return BoardTokenRow{}, fmt.Errorf("ValidateBoardToken query: %w", err)
	}

	// Reject revoked and expired tokens.
	if row.RevokedAt != nil || !time.Now().Before(row.ExpiresAt) {
		return BoardTokenRow{}, ErrTokenRevoked
	}

	return row, nil
}

// RevokeBoardTokensForSession sets revoked_at = now() on all non-revoked
// tokens belonging to the given session. It is idempotent — calling it when
// no tokens remain has no effect and returns nil.
func RevokeBoardTokensForSession(ctx context.Context, pool *pgxpool.Pool, sessionID string) error {
	_, err := pool.Exec(ctx, `
		UPDATE board_tokens
		   SET revoked_at = now()
		 WHERE session_id = $1
		   AND revoked_at IS NULL
	`, sessionID)
	if err != nil {
		return fmt.Errorf("RevokeBoardTokensForSession: %w", err)
	}
	return nil
}

// RevokeBoardTokenRaw revokes the one token whose raw value is raw: a token
// that was minted but could not be handed to anyone. Idempotent.
func RevokeBoardTokenRaw(ctx context.Context, pool *pgxpool.Pool, raw string) error {
	rawBytes, err := hex.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("RevokeBoardTokenRaw: %w", err)
	}
	hash := sha256.Sum256(rawBytes)
	if _, err := pool.Exec(ctx, `
		UPDATE board_tokens SET revoked_at = now()
		 WHERE token_hash = $1 AND revoked_at IS NULL`, hash[:]); err != nil {
		return fmt.Errorf("RevokeBoardTokenRaw: %w", err)
	}
	return nil
}

// SessionsWithoutMessageToken returns those of ids that belong to daemonID,
// are live or starting, and hold no valid (unrevoked, unexpired) token with
// the message capability: the sessions that run, or are about to, unable to
// use `blerg-runner`. Ended sessions are never returned — a token revoked at
// a session's end stays revoked.
func SessionsWithoutMessageToken(ctx context.Context, pool *pgxpool.Pool, daemonID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT s.id FROM sessions s
		 WHERE s.daemon_id = $1 AND s.id::text = ANY($2)
		   AND s.status IN ('starting', 'running', 'waiting', 'idle')
		   AND NOT EXISTS (
		       SELECT 1 FROM board_tokens t
		        WHERE t.session_id = s.id AND t.revoked_at IS NULL
		          AND t.expires_at > now() AND 'message' = ANY(t.capabilities))
		 ORDER BY s.id`, daemonID, ids)
	if err != nil {
		return nil, fmt.Errorf("SessionsWithoutMessageToken: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("SessionsWithoutMessageToken scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
