package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/blerglab/blerg-ai/board/internal/secretbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Encryption of boards.automation_token at rest.
//
// The column holds a live agent token (up to 90 days; it can start sessions as
// its owner), so it is stored sealed with AES-256-GCM under
// BLERG_BOARD_SECRET_KEY: "v1:" + base64(nonce||ciphertext), with the board id
// bound in as additional data so a ciphertext cannot be copied to another
// board. The plaintext exists only inside GetBoardAutomation's return value
// (dispatch) and inside the writers, for the instant of sealing.

// SecretKeyEnv is the environment variable that carries the key.
const SecretKeyEnv = "BLERG_BOARD_SECRET_KEY" //nolint:gosec // G101: the name of an environment variable, not a credential

var (
	// ErrAutomationKeyMissing: a token cannot be stored, or read, because no
	// encryption key is configured.
	ErrAutomationKeyMissing = errors.New(SecretKeyEnv + " is not set, so this board cannot store an automation token: " +
		"the administrator must set " + SecretKeyEnv + " (32 random bytes, base64) and restart blerg-board")
	// ErrAutomationUndecryptable: the stored value does not open under the
	// current key (rotated or lost key, corruption, plaintext left behind).
	ErrAutomationUndecryptable = errors.New("automation token cannot be decrypted; re-save it")

	automationBox atomic.Pointer[secretbox.Box]
)

// SetAutomationKey installs (raw != nil) or removes (nil) the key used for
// automation tokens. InitAutomationEncryption is the boot path; this exists
// for it and for tests.
func SetAutomationKey(raw []byte) error {
	if raw == nil {
		automationBox.Store(nil)
		return nil
	}
	b, err := secretbox.New(raw)
	if err != nil {
		return err
	}
	automationBox.Store(b)
	return nil
}

// AutomationKeyConfigured reports whether tokens can be stored and read.
func AutomationKeyConfigured() bool { return automationBox.Load() != nil }

func sealAutomationToken(boardID, token string) (string, error) {
	b := automationBox.Load()
	if b == nil {
		return "", ErrAutomationKeyMissing
	}
	return b.Seal(token, boardID)
}

// openAutomationToken decrypts a stored value for boardID. "" stays "".
func openAutomationToken(boardID, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	b := automationBox.Load()
	if b == nil {
		return "", ErrAutomationKeyMissing
	}
	pt, err := b.Open(stored, boardID)
	if err != nil {
		return "", ErrAutomationUndecryptable
	}
	return pt, nil
}

// InitAutomationEncryption is the boot-time key handling for automation
// tokens, from the raw value of BLERG_BOARD_SECRET_KEY:
//
//   - A set but malformed key is an error (fail closed, always).
//   - An unset key is fine when no board has a stored token: the board runs,
//     and saving a token is refused until the key is set. If any board has a
//     token the error says so — booting would leave it unusable, and never
//     silently stores new ones in plaintext.
//   - With a valid key, every row whose value lacks the versioned prefix
//     (a token saved before encryption existed) is sealed in one transaction.
//
// It returns how many rows it sealed. The error never contains the key.
func InitAutomationEncryption(ctx context.Context, pool *pgxpool.Pool, rawKey string) (int, error) {
	if strings.TrimSpace(rawKey) == "" {
		_ = SetAutomationKey(nil)
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM boards WHERE automation_token <> ''`).Scan(&n); err != nil {
			return 0, fmt.Errorf("counting stored automation tokens: %w", err)
		}
		if n > 0 {
			return 0, fmt.Errorf("%s is not set, but %d board(s) have a stored automation token: set it (32 random bytes, "+
				"base64; the key the tokens were encrypted with, or any new key for tokens saved before encryption existed)", SecretKeyEnv, n)
		}
		return 0, nil
	}
	key, err := secretbox.ParseKey(rawKey)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid: %w (expected 32 random bytes, base64 — e.g. `openssl rand -base64 32`)", SecretKeyEnv, err)
	}
	if err := SetAutomationKey(key); err != nil {
		return 0, fmt.Errorf("%s is invalid: %w", SecretKeyEnv, err)
	}
	return EncryptPlaintextAutomationTokens(ctx, pool)
}

// EncryptPlaintextAutomationTokens seals every automation token that does not
// carry the versioned prefix. Idempotent (sealed rows are never touched, so
// nothing is double-encrypted) and atomic (one transaction: a crash leaves the
// rows exactly as they were).
func EncryptPlaintextAutomationTokens(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	box := automationBox.Load()
	if box == nil {
		return 0, ErrAutomationKeyMissing
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	rows, err := tx.Query(ctx, `SELECT id, automation_token FROM boards
		WHERE automation_token <> '' AND left(automation_token, 3) <> $1 FOR UPDATE`, secretbox.V1Prefix)
	if err != nil {
		return 0, err
	}
	type row struct{ id, tok string }
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.tok); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range todo {
		sealed, err := box.Seal(r.tok, r.id)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE boards SET automation_token = $2 WHERE id = $1`, r.id, sealed); err != nil {
			return 0, err
		}
	}
	return len(todo), tx.Commit(ctx)
}
