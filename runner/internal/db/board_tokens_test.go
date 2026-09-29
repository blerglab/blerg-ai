package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// TestBoardTokenMintValidate covers the mint→validate round-trip: returned row
// has the right board id, session id, and capabilities; raw token validates.
func TestBoardTokenMintValidate(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "10000000-0000-0000-0000-000000000001"
	sessionID := "20000000-0000-0000-0000-000000000001"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "token-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/projects/x", "repo-x", "Token Test Session", "claude-3"); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	board, err := db.CreateBoard(ctx, pool, "Token Test Board", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	boardID := board.ID

	caps := []string{"read", "write"}
	raw, row, err := db.MintBoardToken(ctx, pool, boardID, sessionID, caps, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken: %v", err)
	}
	if raw == "" {
		t.Fatal("MintBoardToken: raw token is empty")
	}
	if row.ID == "" {
		t.Fatal("MintBoardToken: row.ID is empty")
	}
	if row.BoardID != boardID {
		t.Errorf("MintBoardToken: BoardID = %q, want %q", row.BoardID, boardID)
	}
	if row.SessionID != sessionID {
		t.Errorf("MintBoardToken: SessionID = %q, want %q", row.SessionID, sessionID)
	}
	if len(row.Capabilities) != 2 || row.Capabilities[0] != "read" || row.Capabilities[1] != "write" {
		t.Errorf("MintBoardToken: Capabilities = %v, want [read write]", row.Capabilities)
	}
	if row.RevokedAt != nil {
		t.Errorf("MintBoardToken: RevokedAt should be nil, got %v", row.RevokedAt)
	}
	if row.ExpiresAt.Before(time.Now()) {
		t.Error("MintBoardToken: ExpiresAt is in the past")
	}

	// Validate the raw token — should round-trip to the same row.
	validated, err := db.ValidateBoardToken(ctx, pool, raw)
	if err != nil {
		t.Fatalf("ValidateBoardToken: %v", err)
	}
	if validated.ID != row.ID {
		t.Errorf("ValidateBoardToken: ID = %q, want %q", validated.ID, row.ID)
	}
	if validated.BoardID != boardID {
		t.Errorf("ValidateBoardToken: BoardID = %q, want %q", validated.BoardID, boardID)
	}
	if len(validated.Capabilities) != 2 || validated.Capabilities[0] != "read" || validated.Capabilities[1] != "write" {
		t.Errorf("ValidateBoardToken: Capabilities = %v, want [read write]", validated.Capabilities)
	}
}

// TestBoardTokenTampered covers that an unknown/tampered raw token → ErrTokenInvalid.
func TestBoardTokenTampered(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// Totally unknown token — no rows in DB.
	_, err := db.ValidateBoardToken(ctx, pool, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if !errors.Is(err, db.ErrTokenInvalid) {
		t.Fatalf("ValidateBoardToken (unknown): got %v, want ErrTokenInvalid", err)
	}

	// Set up a real token, then tamper with one character.
	daemonID := "10000000-0000-0000-0000-000000000002"
	sessionID := "20000000-0000-0000-0000-000000000002"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "token-daemon2", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/projects/y", "repo-y", "Tamper Session", "claude-3"); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	board, err := db.CreateBoard(ctx, pool, "Tamper Board", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	raw, _, err := db.MintBoardToken(ctx, pool, board.ID, sessionID, []string{"read"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken: %v", err)
	}

	// Flip the last hex character to produce a different hash.
	last := raw[len(raw)-1]
	var replacement byte
	if last != 'f' {
		replacement = 'f'
	} else {
		replacement = 'e'
	}
	tampered := raw[:len(raw)-1] + string(replacement)

	_, err = db.ValidateBoardToken(ctx, pool, tampered)
	if !errors.Is(err, db.ErrTokenInvalid) {
		t.Fatalf("ValidateBoardToken (tampered): got %v, want ErrTokenInvalid", err)
	}
}

// TestBoardTokenExpired covers that a token minted with a negative TTL is
// immediately expired and ValidateBoardToken returns ErrTokenInvalid.
func TestBoardTokenExpired(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "10000000-0000-0000-0000-000000000003"
	sessionID := "20000000-0000-0000-0000-000000000003"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "token-daemon3", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/projects/z", "repo-z", "Expired Session", "claude-3"); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	board, err := db.CreateBoard(ctx, pool, "Expired Board", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	// Negative TTL → expires_at is in the past.
	raw, _, err := db.MintBoardToken(ctx, pool, board.ID, sessionID, []string{"read"}, -time.Second)
	if err != nil {
		t.Fatalf("MintBoardToken (expired ttl): %v", err)
	}

	_, err = db.ValidateBoardToken(ctx, pool, raw)
	if !errors.Is(err, db.ErrTokenInvalid) {
		t.Fatalf("ValidateBoardToken (expired): got %v, want ErrTokenInvalid", err)
	}
}

// TestBoardTokenRevokeBySession covers that RevokeBoardTokensForSession makes
// all previously-valid tokens for a session invalid (and is idempotent).
func TestBoardTokenRevokeBySession(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "10000000-0000-0000-0000-000000000004"
	sessionID := "20000000-0000-0000-0000-000000000004"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "token-daemon4", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/projects/r", "repo-r", "Revoke Session", "claude-3"); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	board, err := db.CreateBoard(ctx, pool, "Revoke Board", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	raw1, _, err := db.MintBoardToken(ctx, pool, board.ID, sessionID, []string{"read"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken (1): %v", err)
	}
	raw2, _, err := db.MintBoardToken(ctx, pool, board.ID, sessionID, []string{"write"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken (2): %v", err)
	}

	// Both tokens must validate before revocation.
	if _, err := db.ValidateBoardToken(ctx, pool, raw1); err != nil {
		t.Fatalf("ValidateBoardToken (pre-revoke, token1): %v", err)
	}
	if _, err := db.ValidateBoardToken(ctx, pool, raw2); err != nil {
		t.Fatalf("ValidateBoardToken (pre-revoke, token2): %v", err)
	}

	if err := db.RevokeBoardTokensForSession(ctx, pool, sessionID); err != nil {
		t.Fatalf("RevokeBoardTokensForSession: %v", err)
	}

	_, err = db.ValidateBoardToken(ctx, pool, raw1)
	if !errors.Is(err, db.ErrTokenInvalid) {
		t.Fatalf("ValidateBoardToken (post-revoke, token1): got %v, want ErrTokenInvalid", err)
	}
	_, err = db.ValidateBoardToken(ctx, pool, raw2)
	if !errors.Is(err, db.ErrTokenInvalid) {
		t.Fatalf("ValidateBoardToken (post-revoke, token2): got %v, want ErrTokenInvalid", err)
	}

	// Revoking again must be idempotent (no error).
	if err := db.RevokeBoardTokensForSession(ctx, pool, sessionID); err != nil {
		t.Fatalf("RevokeBoardTokensForSession (idempotent): %v", err)
	}
}

// TestMintSessionTokenValidatesWithoutBoard: the per-session messaging token
// (the credential that replaces the daemon master token in every session's
// env) belongs to a session with no board. board_id is NULL in the row and
// ValidateBoardToken reports it as "" with the session and caps intact; the
// TTL is applied and RevokeBoardTokensForSession (session end) revokes it.
func TestMintSessionTokenValidatesWithoutBoard(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if err := db.UpsertDaemon(ctx, pool, "11111111-1111-4111-8111-111111111111", "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	sessionID := "22222222-2222-4222-8222-222222222222"
	if err := db.InsertSession(ctx, pool, sessionID, "11111111-1111-4111-8111-111111111111", "starting", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := db.MintSessionToken(ctx, pool, sessionID, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.ValidateBoardToken(ctx, pool, raw)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if row.BoardID != "" || row.SessionID != sessionID || len(row.Capabilities) != 1 || row.Capabilities[0] != "message" {
		t.Fatalf("row = %+v", row)
	}
	if !row.ExpiresAt.After(time.Now().Add(50 * time.Minute)) {
		t.Fatalf("ttl not applied: expires %v", row.ExpiresAt)
	}
	if err := db.RevokeBoardTokensForSession(ctx, pool, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ValidateBoardToken(ctx, pool, raw); !errors.Is(err, db.ErrTokenInvalid) {
		t.Fatalf("revoked session token still validates: %v", err)
	}
}
