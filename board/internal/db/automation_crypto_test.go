package db_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

const plainTok = "header.payload.signature-PLAINTEXT"

func storedToken(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), `SELECT automation_token FROM boards WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// rawSet writes a value straight into the column, bypassing the sealing writer.
func rawSet(t *testing.T, pool *pgxpool.Pool, id, v string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE boards SET automation_token = $2 WHERE id = $1`, id, v); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationTokenSealedAtRestRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{})
	if err := db.SetBoardAutomationToken(ctx, pool, b.ID, db.AutomationTokenUpdate{Token: plainTok, AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	st := storedToken(t, pool, b.ID)
	if !strings.HasPrefix(st, "v1:") || strings.Contains(st, plainTok) {
		t.Fatalf("stored value not sealed: %.10q", st)
	}
	ba, err := db.GetBoardAutomation(ctx, pool, b.ID)
	if err != nil || ba.Token != plainTok {
		t.Fatalf("read back = %q, %v", ba.Token, err)
	}
	// the PATCH path seals too
	if _, err := db.UpdateBoardWithAutomation(ctx, pool, b.ID, db.BoardParams{},
		&db.AutomationTokenUpdate{Token: plainTok + "2", AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	if st := storedToken(t, pool, b.ID); !strings.HasPrefix(st, "v1:") || strings.Contains(st, plainTok) {
		t.Fatalf("PATCH path stored unsealed: %.10q", st)
	}
	// clearing needs no key and leaves ""
	if err := db.SetAutomationKey(nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetBoardAutomationToken(ctx, pool, b.ID, db.AutomationTokenUpdate{}); err != nil {
		t.Fatalf("clear without key: %v", err)
	}
	if st := storedToken(t, pool, b.ID); st != "" {
		t.Fatalf("cleared value = %q", st)
	}
}

func TestAutomationTokenFailsClosed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b1 := mkBoard(t, pool, db.BoardParams{Name: "one"})
	b2 := mkBoard(t, pool, db.BoardParams{Name: "two"})
	for _, b := range []db.Board{b1, b2} {
		if err := db.SetBoardAutomationToken(ctx, pool, b.ID, db.AutomationTokenUpdate{Token: plainTok, AccountID: "a"}); err != nil {
			t.Fatal(err)
		}
	}

	// a ciphertext copied to another board does not open there
	rawSet(t, pool, b2.ID, storedToken(t, pool, b1.ID))
	if _, err := db.GetBoardAutomation(ctx, pool, b2.ID); !errors.Is(err, db.ErrAutomationUndecryptable) {
		t.Fatalf("copied ciphertext: %v", err)
	}
	// corruption
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(storedToken(t, pool, b1.ID), "v1:"))
	raw[len(raw)-1] ^= 1
	rawSet(t, pool, b1.ID, "v1:"+base64.StdEncoding.EncodeToString(raw))
	if _, err := db.GetBoardAutomation(ctx, pool, b1.ID); !errors.Is(err, db.ErrAutomationUndecryptable) {
		t.Fatalf("corrupted: %v", err)
	}
	// a leftover plaintext row (never migrated) is refused, not used
	rawSet(t, pool, b1.ID, plainTok)
	if _, err := db.GetBoardAutomation(ctx, pool, b1.ID); !errors.Is(err, db.ErrAutomationUndecryptable) {
		t.Fatalf("plaintext row: %v", err)
	}
	// wrong key
	if err := db.SetBoardAutomationToken(ctx, pool, b1.ID, db.AutomationTokenUpdate{Token: plainTok, AccountID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAutomationKey([]byte("ffffffffffffffffffffffffffffffff")); err != nil {
		t.Fatal(err)
	}
	_, err := db.GetBoardAutomation(ctx, pool, b1.ID)
	if !errors.Is(err, db.ErrAutomationUndecryptable) || strings.Contains(err.Error(), plainTok) {
		t.Fatalf("wrong key: %v", err)
	}
	// no key: cannot read, cannot save
	if err := db.SetAutomationKey(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetBoardAutomation(ctx, pool, b1.ID); !errors.Is(err, db.ErrAutomationKeyMissing) {
		t.Fatalf("no key read: %v", err)
	}
	if err := db.SetBoardAutomationToken(ctx, pool, b1.ID, db.AutomationTokenUpdate{Token: "x", AccountID: "a"}); !errors.Is(err, db.ErrAutomationKeyMissing) {
		t.Fatalf("no key save: %v", err)
	}
	if _, err := db.UpdateBoardWithAutomation(ctx, pool, b1.ID, db.BoardParams{},
		&db.AutomationTokenUpdate{Token: "x", AccountID: "a"}); !errors.Is(err, db.ErrAutomationKeyMissing) {
		t.Fatalf("no key PATCH: %v", err)
	}
}

func TestInitAutomationEncryption(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	key := base64.StdEncoding.EncodeToString(testAutomationKey)
	b1 := mkBoard(t, pool, db.BoardParams{Name: "one"})
	b2 := mkBoard(t, pool, db.BoardParams{Name: "two"})
	mkBoard(t, pool, db.BoardParams{Name: "none"})
	rawSet(t, pool, b1.ID, plainTok)
	rawSet(t, pool, b2.ID, plainTok+"-2")
	if err := db.SetAutomationKey(nil); err != nil {
		t.Fatal(err)
	}

	// tokens exist, key unset: refuse, naming the variable, without the token
	_, err := db.InitAutomationEncryption(ctx, pool, "")
	if err == nil || !strings.Contains(err.Error(), "BLERG_BOARD_SECRET_KEY") || strings.Contains(err.Error(), plainTok) {
		t.Fatalf("missing key with tokens: %v", err)
	}
	// malformed keys refuse and never echo the key
	for _, bad := range []string{"short", strings.Repeat("ab", 32), base64.StdEncoding.EncodeToString([]byte("too-short"))} {
		_, err := db.InitAutomationEncryption(ctx, pool, bad)
		if err == nil || !strings.Contains(err.Error(), "BLERG_BOARD_SECRET_KEY") || strings.Contains(err.Error(), bad) {
			t.Fatalf("bad key %q: %v", bad, err)
		}
	}
	if storedToken(t, pool, b1.ID) != plainTok {
		t.Fatal("a refused boot modified the rows")
	}

	// migrate; a second run is idempotent and does not double-encrypt
	n, err := db.InitAutomationEncryption(ctx, pool, key)
	if err != nil || n != 2 {
		t.Fatalf("migrate = %d, %v; want 2", n, err)
	}
	first := storedToken(t, pool, b1.ID)
	if !strings.HasPrefix(first, "v1:") || strings.Contains(first, plainTok) {
		t.Fatalf("not sealed: %.10q", first)
	}
	n, err = db.InitAutomationEncryption(ctx, pool, key)
	if err != nil || n != 0 {
		t.Fatalf("second run = %d, %v; want 0", n, err)
	}
	if storedToken(t, pool, b1.ID) != first {
		t.Fatal("second run re-encrypted a sealed row")
	}
	for id, want := range map[string]string{b1.ID: plainTok, b2.ID: plainTok + "-2"} {
		ba, err := db.GetBoardAutomation(ctx, pool, id)
		if err != nil || ba.Token != want {
			t.Fatalf("after migrate %s = %q, %v", id, ba.Token, err)
		}
	}
	// sealed rows and no key still refuses to boot
	if _, err := db.InitAutomationEncryption(ctx, pool, ""); err == nil {
		t.Fatal("sealed tokens with no key booted")
	}
}

func TestInitAutomationEncryptionNoKeyNoTokensBoots(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{})
	n, err := db.InitAutomationEncryption(ctx, pool, "  ")
	if err != nil || n != 0 || db.AutomationKeyConfigured() {
		t.Fatalf("no key, no tokens: n=%d err=%v configured=%v", n, err, db.AutomationKeyConfigured())
	}
	// a board without automation is unaffected...
	ba, err := db.GetBoardAutomation(ctx, pool, b.ID)
	if err != nil || ba.Token != "" {
		t.Fatalf("read on a board with no token: %q, %v", ba.Token, err)
	}
	// ...and saving a token is refused with the key-missing error
	err = db.SetBoardAutomationToken(ctx, pool, b.ID, db.AutomationTokenUpdate{Token: "x", AccountID: "a"})
	if !errors.Is(err, db.ErrAutomationKeyMissing) || !strings.Contains(err.Error(), "BLERG_BOARD_SECRET_KEY") {
		t.Fatalf("save without key: %v", err)
	}
}
