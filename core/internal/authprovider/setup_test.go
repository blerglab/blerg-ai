package authprovider_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
)

func countAccounts(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM accounts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSetupOpenOnlyWithZeroAccounts(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	open, err := local.SetupOpen(ctx)
	if err != nil || !open {
		t.Fatalf("empty table: SetupOpen = %v, %v; want true", open, err)
	}
	insertMember(t, pool, "someone")
	if open, err := local.SetupOpen(ctx); err != nil || open {
		t.Fatalf("with an account: SetupOpen = %v, %v; want false", open, err)
	}
	if _, err := local.MintSetupCode(ctx); !errors.Is(err, authprovider.ErrSetupComplete) {
		t.Fatalf("MintSetupCode with an account: %v, want ErrSetupComplete", err)
	}
}

func insertMember(t *testing.T, pool *pgxpool.Pool, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO accounts (provider, provider_subject, role) VALUES ('local', $1, 'member')`, subject); err != nil {
		t.Fatal(err)
	}
}

func TestSetupCodeIsLongAndStoredOnlyAsAHash(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	code, err := authprovider.NewLocal(st).MintSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(raw) < 16 {
		t.Fatalf("code %d bytes (%v), want at least 128 bits", len(raw), err)
	}
	var stored string
	var hours float64
	if err := pool.QueryRow(ctx, `SELECT code_hash, extract(epoch FROM expires_at - now())/3600 FROM setup_code`).Scan(&stored, &hours); err != nil {
		t.Fatal(err)
	}
	if stored == code || len(stored) != 64 {
		t.Fatalf("stored value is not a SHA-256 hex digest")
	}
	if hours < 23.9 || hours > 24.1 {
		t.Fatalf("code expires in %.2f h, want 24", hours)
	}
}

func TestCreateFirstAdminWithCode(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	code, err := local.MintSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "owner", Password: "a-good-long-password", Code: code})
	if err != nil {
		t.Fatalf("CreateFirstAdmin: %v", err)
	}
	if acc.ID == "" || acc.ProviderSubject != "owner" {
		t.Fatalf("account = %+v", acc)
	}
	var role string
	var mustChange bool
	if err := pool.QueryRow(ctx, `SELECT role, must_change_password FROM accounts WHERE id = $1`, acc.ID).Scan(&role, &mustChange); err != nil {
		t.Fatal(err)
	}
	if role != "admin" || mustChange {
		t.Fatalf("role %q must_change %v, want admin without a forced change", role, mustChange)
	}
	if _, err := local.Callback(ctx, loginRequest("owner", "a-good-long-password")); err != nil {
		t.Fatalf("chosen password does not sign in: %v", err)
	}
	var codes int
	pool.QueryRow(ctx, `SELECT count(*) FROM setup_code`).Scan(&codes)
	if codes != 0 {
		t.Fatal("the setup code was not consumed")
	}
	// The same code again: setup is closed.
	_, err = local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "second", Password: "a-good-long-password", Code: code})
	if !errors.Is(err, authprovider.ErrSetupComplete) {
		t.Fatalf("reused code: %v, want ErrSetupComplete", err)
	}
	if open, _ := local.SetupOpen(ctx); open {
		t.Fatal("setup still open after the first account")
	}
}

func TestCreateFirstAdminRefusesBadCodes(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	try := func(code string) error {
		_, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "owner", Password: "a-good-long-password", Code: code})
		return err
	}
	// No code minted at all.
	if err := try("anything"); !errors.Is(err, authprovider.ErrSetupCode) {
		t.Fatalf("no live code: %v, want ErrSetupCode", err)
	}
	first, err := local.MintSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := try(""); !errors.Is(err, authprovider.ErrSetupCode) {
		t.Fatalf("empty code: %v, want ErrSetupCode", err)
	}
	if err := try(first + "x"); !errors.Is(err, authprovider.ErrSetupCode) {
		t.Fatalf("wrong code: %v, want ErrSetupCode", err)
	}
	// A newer code (the next boot, or setup-link) supersedes the older one.
	second, err := local.MintSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := try(first); !errors.Is(err, authprovider.ErrSetupCode) {
		t.Fatalf("superseded code: %v, want ErrSetupCode", err)
	}
	// Expired.
	if _, err := pool.Exec(ctx, `UPDATE setup_code SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := try(second); !errors.Is(err, authprovider.ErrSetupCode) {
		t.Fatalf("expired code: %v, want ErrSetupCode", err)
	}
	if n := countAccounts(t, pool); n != 0 {
		t.Fatalf("refused codes created %d accounts", n)
	}
	// Input rules come first and are the same as for a username/password change.
	third, _ := local.MintSetupCode(ctx)
	if _, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "Owner!", Password: "a-good-long-password", Code: third}); !errors.Is(err, authprovider.ErrInvalidUsername) {
		t.Fatalf("bad username: %v", err)
	}
	if _, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "owner", Password: "short", Code: third}); !errors.Is(err, authprovider.ErrWeakPassword) {
		t.Fatalf("short password: %v", err)
	}
	if _, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "owner", Password: string(make([]byte, 73)), Code: third}); !errors.Is(err, authprovider.ErrWeakPassword) {
		t.Fatalf("73-byte password: %v", err)
	}
	if err := try(third); err != nil {
		t.Fatalf("the live code after refusals: %v", err)
	}
}

func TestCreateFirstAdminLocalNeedsNoCode(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	if _, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "owner", Password: "a-good-long-password", Local: true}); err != nil {
		t.Fatalf("local setup: %v", err)
	}
	if _, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "again", Password: "a-good-long-password", Local: true}); !errors.Is(err, authprovider.ErrSetupComplete) {
		t.Fatalf("second local setup: %v, want ErrSetupComplete", err)
	}
	if n := countAccounts(t, pool); n != 1 {
		t.Fatalf("%d accounts, want 1", n)
	}
}

// Two (here: many) people submitting /setup at once: exactly one gets the account, whatever the
// usernames and whether they come with a code or as local.
func TestCreateFirstAdminConcurrentExactlyOneWins(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	code, err := local.MintSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{
				Username: fmt.Sprintf("owner%d", i), Password: "a-good-long-password", Code: code, Local: i%2 == 0,
			})
		}(i)
	}
	close(start)
	wg.Wait()
	wins := 0
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, authprovider.ErrSetupComplete), errors.Is(err, authprovider.ErrSetupCode):
		default:
			t.Errorf("attempt %d: unexpected error %v", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d attempts succeeded, want exactly 1", wins)
	}
	if got := countAccounts(t, pool); got != 1 {
		t.Fatalf("%d accounts, want 1", got)
	}
}

// The deterministic form of the race: another transaction has inserted an account but not yet
// committed. Setup must wait for it and then refuse, not read "no accounts yet" and insert a
// second one beside it.
func TestCreateFirstAdminWaitsForAnUncommittedAccount(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO accounts (provider, provider_subject, role) VALUES ('local', 'racer', 'member')`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "owner", Password: "a-good-long-password", Local: true})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("setup finished (%v) while another account was being created; it must wait", err)
	case <-time.After(500 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, authprovider.ErrSetupComplete) {
		t.Fatalf("after the other account committed: %v, want ErrSetupComplete", err)
	}
	if n := countAccounts(t, pool); n != 1 {
		t.Fatalf("%d accounts, want 1", n)
	}
}

// A set-up install answers setup attempts from a cheap read: no bcrypt, no table lock. Here a
// writer holds an uncommitted insert into accounts; an attempt that took the lock would wait for it.
func TestCreateFirstAdminWhenClosedTakesNoLock(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	insertMember(t, pool, "existing")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO accounts (provider, provider_subject, role) VALUES ('local', 'writer', 'member')`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "owner", Password: "a-good-long-password", Code: "bogus"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, authprovider.ErrSetupComplete) {
			t.Fatalf("closed setup: %v, want ErrSetupComplete", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a setup attempt on a set-up install waited behind a writer on accounts")
	}
}

// The first-account transaction gives up on the accounts lock after SetupLockTimeout instead of
// queueing (and holding a pool connection) behind a long writer.
func TestCreateFirstAdminLockTimeout(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	old := authprovider.SetupLockTimeout
	authprovider.SetupLockTimeout = 300 * time.Millisecond
	t.Cleanup(func() { authprovider.SetupLockTimeout = old })
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE accounts IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := local.CreateFirstAdmin(ctx, authprovider.FirstAdmin{Username: "owner", Password: "a-good-long-password", Local: true})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("setup succeeded while another transaction held the accounts lock")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("setup waited past its lock timeout")
	}
	if n := countAccounts(t, pool); n != 0 {
		t.Fatalf("%d accounts, want 0", n)
	}
}

// Once an account has been seen, setup stays closed for the life of the process without asking
// the database again.
func TestSetupClosedLatches(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	insertMember(t, pool, "someone")
	if open, _ := local.SetupOpen(ctx); open {
		t.Fatal("open with an account")
	}
	// Only an operator emptying the table by hand could do this; the running process stays closed.
	if _, err := pool.Exec(ctx, `DELETE FROM accounts`); err != nil {
		t.Fatal(err)
	}
	if open, _ := local.SetupOpen(ctx); open {
		t.Fatal("setup reopened in a process that had seen an account")
	}
	if open, _ := authprovider.NewLocal(st).SetupOpen(ctx); !open {
		t.Fatal("a fresh process over an empty table must be in setup mode")
	}
}
