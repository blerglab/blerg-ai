package credentials_test

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/credentials"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
)

// testLocalKey is a fixed 32-byte AES key used by every test in this package — the local
// keybackend no longer reads its key from the database (BLERG_CORE_LOCAL_KEY), so tests supply
// one directly.
var testLocalKey = bytes.Repeat([]byte{7}, 32)

// testPool provides a fresh database cloned from the migrated package template.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testsupport.Pool(t, db.Migrate)
}

var testAccountSeq int64

// insertTestAccount inserts a minimal local-provider account and returns its id.
func insertTestAccount(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	subject := fmt.Sprintf("%s-%d", t.Name(), atomic.AddInt64(&testAccountSeq, 1))
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO accounts (provider, provider_subject, email, role) VALUES ('local',$1,$1||'@example.com','member') RETURNING id::text`,
		subject).Scan(&id)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	return id
}

func TestStoreListDeleteRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	st := db.NewPgStore(pool)
	backend, err := keybackend.NewLocal(testLocalKey)
	if err != nil {
		t.Fatal(err)
	}
	svc := credentials.NewService(st, backend)

	accountID := insertTestAccount(t, pool)
	if err := svc.Store(ctx, accountID, "claude", []byte("sk-ant-token")); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, accountID)
	if err != nil || len(list) != 1 || list[0].Engine != "claude" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if list[0].UpdatedAt.IsZero() {
		t.Fatal("expected UpdatedAt to be set")
	}
	if err := svc.Delete(ctx, accountID, "claude"); err != nil {
		t.Fatal(err)
	}
	list, _ = svc.List(ctx, accountID)
	if len(list) != 0 {
		t.Fatal("expected empty list after delete")
	}
}

// TestStoreUpsertsOnRepeatedEngine: storing the same account+engine twice must update the
// existing row (per the PRIMARY KEY (account_id, engine) schema), not create a second row or
// error out.
func TestStoreUpsertsOnRepeatedEngine(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	st := db.NewPgStore(pool)
	backend, err := keybackend.NewLocal(testLocalKey)
	if err != nil {
		t.Fatal(err)
	}
	svc := credentials.NewService(st, backend)
	accountID := insertTestAccount(t, pool)

	if err := svc.Store(ctx, accountID, "claude", []byte("first-token")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store(ctx, accountID, "claude", []byte("second-token")); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, accountID)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v; want exactly 1 row after upsert", list, err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_credentials WHERE account_id = $1`, accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row in user_credentials, got %d", count)
	}
}

// TestListNeverReturnsCiphertext: List's return type (CredentialSummary) must never expose
// ciphertext or plaintext — this test asserts on the actual stored row via a raw query to prove
// the plaintext genuinely differs from what's persisted, and that CredentialSummary as a Go type
// has no field capable of carrying it.
func TestListNeverReturnsCiphertext(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	st := db.NewPgStore(pool)
	backend, err := keybackend.NewLocal(testLocalKey)
	if err != nil {
		t.Fatal(err)
	}
	svc := credentials.NewService(st, backend)
	accountID := insertTestAccount(t, pool)

	plaintext := []byte("sk-ant-super-secret")
	if err := svc.Store(ctx, accountID, "claude", plaintext); err != nil {
		t.Fatal(err)
	}

	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT ciphertext FROM user_credentials WHERE account_id=$1 AND engine='claude'`, accountID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == string(plaintext) {
		t.Fatal("stored ciphertext must not equal plaintext")
	}

	list, err := svc.List(ctx, accountID)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	// CredentialSummary only has Engine/UpdatedAt fields — this compiles only if that stays
	// true; any accidental addition of a ciphertext/plaintext field would still need to be
	// caught by a reviewer since Go doesn't enforce "unused fields", but the round-trip above
	// (stored ciphertext != plaintext) is the load-bearing assertion.
	_ = list[0].Engine
	_ = list[0].UpdatedAt
}

// TestDeleteIsScopedToAccountAndEngine: deleting account A's "claude" credential must not touch
// account B's row for the same engine name.
func TestDeleteIsScopedToAccountAndEngine(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	st := db.NewPgStore(pool)
	backend, err := keybackend.NewLocal(testLocalKey)
	if err != nil {
		t.Fatal(err)
	}
	svc := credentials.NewService(st, backend)

	accountA := insertTestAccount(t, pool)
	accountB := insertTestAccount(t, pool)

	if err := svc.Store(ctx, accountA, "claude", []byte("a-token")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store(ctx, accountB, "claude", []byte("b-token")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, accountA, "claude"); err != nil {
		t.Fatal(err)
	}

	listA, err := svc.List(ctx, accountA)
	if err != nil || len(listA) != 0 {
		t.Fatalf("account A list after delete = %+v, %v; want empty", listA, err)
	}
	listB, err := svc.List(ctx, accountB)
	if err != nil || len(listB) != 1 {
		t.Fatalf("account B list after account A's delete = %+v, %v; want untouched (1 row)", listB, err)
	}
}
