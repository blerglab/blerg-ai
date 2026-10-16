package authprovider_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/db"
)

// testSchema is a fixed schema name reused (dropped+recreated) by every test
// in this package; tests here don't run in parallel with each other, so one
// name is enough.
const testSchema = "test_blerg_core_authprovider"

// testPool connects to DATABASE_URL and gives the caller a fresh, isolated
// schema, dropped and recreated before the test and dropped again on
// cleanup. Schema isolation (rather than a plain "SET search_path", which
// only affects the one pgxpool connection it runs on) is required because
// core's identity/authprovider tests otherwise share one live Postgres
// database with no per-row cleanup — see the identical helper and comment
// in core/internal/identity/accounts_test.go, and
// runner/internal/db/ticket_graph_test.go's connectWithSchema, which
// established this pattern in the monorepo.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()

	setupPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", testSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", testSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	setupPool.Close()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = testSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		pool.Exec(bg, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", testSchema))
		pool.Close()
	})

	return pool
}

func freshStore(t *testing.T) (*pgxpool.Pool, db.Store) {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool, db.NewPgStore(pool)
}

func TestEnsureBootstrapAdminSeedsOnceOnEmptyTable(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	if err := local.EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE role='admin'`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 admin account, got %d", count)
	}
	var mustChange bool
	if err := pool.QueryRow(ctx, `SELECT must_change_password FROM accounts WHERE role='admin'`).Scan(&mustChange); err != nil {
		t.Fatal(err)
	}
	if !mustChange {
		t.Error("expected seeded admin to have must_change_password=true")
	}

	// Idempotent: calling again on a non-empty table must not seed a second one.
	if err := local.EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE role='admin'`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected still exactly 1 admin account after second call, got %d", count)
	}
}

func TestLocalCallbackRejectsWrongPassword(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)
	if err := local.EnsureBootstrapAdmin(ctx); err != nil {
		t.Fatal(err)
	}

	var providerSubject string
	pool.QueryRow(ctx, `SELECT provider_subject FROM accounts WHERE role='admin'`).Scan(&providerSubject)
	wrongPW := "definitely-not-the-real-password"

	form := url.Values{"provider_subject": {providerSubject}, "password": {wrongPW}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := local.Callback(ctx, req); err == nil {
		t.Error("expected wrong password to fail")
	}
}

func TestLocalCallbackAcceptsCorrectPassword(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)

	// Insert a test account with a KNOWN bcrypt hash directly via SQL,
	// since the bootstrap admin's randomly-generated password can't be
	// recovered in a test.
	const knownPassword = "correct-horse-battery-staple"
	hash, err := bcrypt.GenerateFromPassword([]byte(knownPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	const subject = "known-test-user"
	_, err = pool.Exec(ctx,
		`INSERT INTO accounts (provider, provider_subject, email, role, must_change_password, password_hash)
		 VALUES ('local', $1, 'known@example.com', 'member', false, $2)`, subject, string(hash))
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"provider_subject": {subject}, "password": {knownPassword}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	acc, err := local.Callback(ctx, req)
	if err != nil {
		t.Fatalf("expected correct password to succeed, got err: %v", err)
	}
	if acc.ProviderSubject != subject {
		t.Errorf("expected ProviderSubject %q, got %q", subject, acc.ProviderSubject)
	}
	if acc.Provider != "local" {
		t.Errorf("expected Provider %q, got %q", "local", acc.Provider)
	}
	if acc.Email != "known@example.com" {
		t.Errorf("expected Email %q, got %q", "known@example.com", acc.Email)
	}
	if acc.ID == "" {
		t.Error("expected non-empty Account.ID")
	}
}

// TestLocalCallbackAcceptsCorrectPasswordWithNoEmailSet is a regression test
// for a bug where accounts.email (nullable; EnsureBootstrapAdmin's own
// INSERT never sets it) being NULL made Callback's Scan fail before the
// password comparison ever ran — meaning the bootstrap admin, and any other
// local-provider account with no email, could never log in even with the
// correct password.
func TestLocalCallbackAcceptsCorrectPasswordWithNoEmailSet(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)

	const knownPassword = "another-known-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(knownPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	const subject = "no-email-test-user"
	// Deliberately omit the email column, exactly as EnsureBootstrapAdmin's
	// INSERT does, so this account's row has email = NULL.
	_, err = pool.Exec(ctx,
		`INSERT INTO accounts (provider, provider_subject, role, must_change_password, password_hash)
		 VALUES ('local', $1, 'admin', true, $2)`, subject, string(hash))
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"provider_subject": {subject}, "password": {knownPassword}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	acc, err := local.Callback(ctx, req)
	if err != nil {
		t.Fatalf("expected correct password to succeed for an account with no email set, got err: %v", err)
	}
	if acc.Email != "" {
		t.Errorf("expected empty Email for a NULL email column, got %q", acc.Email)
	}
	if acc.ProviderSubject != subject {
		t.Errorf("expected ProviderSubject %q, got %q", subject, acc.ProviderSubject)
	}
}

// TestChangePasswordRejectsWeakOrOverlongNewPassword covers both length bounds directly at the
// authprovider layer: under 12 bytes is ErrWeakPassword (documented minimum), and over 72 bytes
// is ALSO ErrWeakPassword rather than falling through to bcrypt.GenerateFromPassword and
// surfacing as a raw ErrPasswordTooLong (which the API layer would otherwise turn into a 500
// instead of the mandated 400).
func TestChangePasswordRejectsWeakOrOverlongNewPassword(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	local := authprovider.NewLocal(st)

	const oldPassword = "current-password-123"
	hash, err := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	var accountID string
	err = pool.QueryRow(ctx,
		`INSERT INTO accounts (provider, provider_subject, email, role, must_change_password, password_hash)
		 VALUES ('local', 'weak-pw-test', 'weak@example.com', 'member', true, $1) RETURNING id::text`,
		string(hash)).Scan(&accountID)
	if err != nil {
		t.Fatal(err)
	}

	if err := local.ChangePassword(ctx, accountID, oldPassword, "short"); !errors.Is(err, authprovider.ErrWeakPassword) {
		t.Fatalf("11-byte new password: err = %v, want ErrWeakPassword", err)
	}
	if err := local.ChangePassword(ctx, accountID, oldPassword, strings.Repeat("x", 73)); !errors.Is(err, authprovider.ErrWeakPassword) {
		t.Fatalf("73-byte new password: err = %v, want ErrWeakPassword", err)
	}
	// The 72-byte boundary itself must be accepted, not rejected off-by-one.
	if err := local.ChangePassword(ctx, accountID, oldPassword, strings.Repeat("y", 72)); err != nil {
		t.Fatalf("72-byte new password (the boundary) should be accepted: %v", err)
	}
	var mustChange bool
	pool.QueryRow(ctx, `SELECT must_change_password FROM accounts WHERE id = $1`, accountID).Scan(&mustChange)
	if mustChange {
		t.Error("a successful ChangePassword must clear must_change_password")
	}
}

func TestCreateLocalAccountAndResetPassword(t *testing.T) {
	_, st := freshStore(t)
	ctx := context.Background()
	l := authprovider.NewLocal(st)
	pw, err := l.CreateLocalAccount(ctx, "eve", "member")
	if err != nil || len(pw) < 20 {
		t.Fatalf("create: %q %v", pw, err)
	}
	var role string
	var must bool
	if err := st.Pool().QueryRow(ctx, `SELECT role, must_change_password FROM accounts WHERE provider='local' AND provider_subject='eve'`).Scan(&role, &must); err != nil || role != "member" || !must {
		t.Fatalf("row role=%q must=%v err=%v", role, must, err)
	}
	if _, err := l.Callback(ctx, formRequest("eve", pw)); err != nil {
		t.Fatalf("callback with one-time password: %v", err)
	}
	if _, err := l.CreateLocalAccount(ctx, "eve", "member"); !errors.Is(err, authprovider.ErrSubjectExists) {
		t.Fatalf("duplicate create = %v, want ErrSubjectExists", err)
	}
	if _, err := l.CreateLocalAccount(ctx, "mallory", "owner"); err == nil {
		t.Fatal("bad role must be rejected")
	}

	var revokedID string
	pw2, err := l.ResetPassword(ctx, "eve", func(_ context.Context, id string) error { revokedID = id; return nil })
	if err != nil || pw2 == pw {
		t.Fatalf("reset: %q %v", pw2, err)
	}
	if revokedID == "" {
		t.Fatal("revoke callback not called")
	}
	if _, err := l.Callback(ctx, formRequest("eve", pw)); err == nil {
		t.Fatal("old password must fail after reset")
	}
	if _, err := l.Callback(ctx, formRequest("eve", pw2)); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if _, err := l.ResetPassword(ctx, "nobody", nil); !errors.Is(err, authprovider.ErrInvalidCredentials) {
		t.Fatalf("unknown subject = %v", err)
	}
}

func formRequest(subject, password string) *http.Request {
	form := url.Values{"provider_subject": {subject}, "password": {password}}
	r := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestValidateUsername(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"abc", true},
		{"alice", true},
		{"a.b_c-d", true},
		{"0day", true},
		{"x9", false},                    // too short
		{strings.Repeat("a", 64), true},  // the upper bound
		{strings.Repeat("a", 65), false}, // over it
		{"Alice", false},                 // uppercase is refused, not folded
		{"-alice", false},                // must start with a letter or digit
		{".alice", false},
		{"_alice", false},
		{"al ice", false},
		{"alice@example.com", false},
		{"élise", false},
		{"", false},
	}
	for _, c := range cases {
		err := authprovider.ValidateUsername(c.in)
		if c.ok && err != nil {
			t.Errorf("ValidateUsername(%q) = %v, want nil", c.in, err)
		}
		if !c.ok && !errors.Is(err, authprovider.ErrInvalidUsername) {
			t.Errorf("ValidateUsername(%q) = %v, want ErrInvalidUsername", c.in, err)
		}
	}
	if got := authprovider.ErrInvalidUsername.Error(); got != "use lowercase letters, digits, dots, dashes and underscores (3–64 characters)" {
		t.Errorf("ErrInvalidUsername message = %q", got)
	}
}

// insertAccount adds an account with a known password (nil hash for a non-local provider) and
// returns its id.
func insertAccount(t *testing.T, pool *pgxpool.Pool, provider, subject, password string) string {
	t.Helper()
	var hash *string
	if password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		s := string(h)
		hash = &s
	}
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO accounts (provider, provider_subject, role, must_change_password, password_hash)
		 VALUES ($1, $2, 'member', true, $3) RETURNING id::text`, provider, subject, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func subjectOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), `SELECT provider_subject FROM accounts WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestChangePasswordAndSubject(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	l := authprovider.NewLocal(st)
	const oldPW, newPW = "old-password-123", "a-long-enough-new-password"

	t.Run("renames and changes the password together", func(t *testing.T) {
		id := insertAccount(t, pool, "local", "Rand0mSubj_1", oldPW)
		got, err := l.ChangePasswordAndSubject(ctx, id, oldPW, newPW, "alice")
		if err != nil || got != "alice" {
			t.Fatalf("change = %q, %v; want alice, nil", got, err)
		}
		if s := subjectOf(t, pool, id); s != "alice" {
			t.Fatalf("subject = %q, want alice", s)
		}
		if _, err := l.Callback(ctx, formRequest("alice", newPW)); err != nil {
			t.Fatalf("login with the new username and password: %v", err)
		}
	})

	t.Run("same subject is a no-op rename", func(t *testing.T) {
		id := insertAccount(t, pool, "local", "same-subject", oldPW)
		got, err := l.ChangePasswordAndSubject(ctx, id, oldPW, newPW, "same-subject")
		if err != nil || got != "same-subject" {
			t.Fatalf("change = %q, %v", got, err)
		}
	})

	t.Run("echoing a legacy name the rules would refuse is a no-op", func(t *testing.T) {
		// A bootstrap name, or one created before the rules, may be mixed case; typing it back to keep it
		// must not be a validation error (the change-password page prefills it).
		id := insertAccount(t, pool, "local", "Legacy.Name_X", oldPW)
		got, err := l.ChangePasswordAndSubject(ctx, id, oldPW, newPW, "Legacy.Name_X")
		if err != nil || got != "Legacy.Name_X" {
			t.Fatalf("change = %q, %v; want the name kept, nil", got, err)
		}
		if _, err := l.Callback(ctx, formRequest("Legacy.Name_X", newPW)); err != nil {
			t.Fatalf("login with the kept name and new password: %v", err)
		}
	})

	t.Run("empty subject keeps the current one", func(t *testing.T) {
		id := insertAccount(t, pool, "local", "Keep-Me_X", oldPW)
		got, err := l.ChangePasswordAndSubject(ctx, id, oldPW, newPW, "")
		if err != nil || got != "Keep-Me_X" {
			t.Fatalf("change = %q, %v", got, err)
		}
	})

	t.Run("a taken username fails and leaves the password alone", func(t *testing.T) {
		insertAccount(t, pool, "local", "taken", oldPW)
		id := insertAccount(t, pool, "local", "clasher", oldPW)
		if _, err := l.ChangePasswordAndSubject(ctx, id, oldPW, newPW, "taken"); !errors.Is(err, authprovider.ErrUsernameTaken) {
			t.Fatalf("clash = %v, want ErrUsernameTaken", err)
		}
		if s := subjectOf(t, pool, id); s != "clasher" {
			t.Fatalf("subject after a clash = %q, want clasher", s)
		}
		// One transaction: the password change rolled back with the rename.
		if _, err := l.Callback(ctx, formRequest("clasher", oldPW)); err != nil {
			t.Fatalf("old password must still work after a failed rename: %v", err)
		}
	})

	t.Run("an invalid username is refused before anything changes", func(t *testing.T) {
		id := insertAccount(t, pool, "local", "valid-now", oldPW)
		if _, err := l.ChangePasswordAndSubject(ctx, id, oldPW, newPW, "Bad Name"); !errors.Is(err, authprovider.ErrInvalidUsername) {
			t.Fatalf("invalid = %v, want ErrInvalidUsername", err)
		}
		if _, err := l.Callback(ctx, formRequest("valid-now", oldPW)); err != nil {
			t.Fatalf("old password must still work: %v", err)
		}
	})

	t.Run("a wrong old password renames nothing", func(t *testing.T) {
		id := insertAccount(t, pool, "local", "wrong-pw", oldPW)
		if _, err := l.ChangePasswordAndSubject(ctx, id, "nope-nope-nope", newPW, "renamed"); !errors.Is(err, authprovider.ErrInvalidCredentials) {
			t.Fatalf("wrong password = %v, want ErrInvalidCredentials", err)
		}
		if s := subjectOf(t, pool, id); s != "wrong-pw" {
			t.Fatalf("subject = %q", s)
		}
	})

	t.Run("a non-local account cannot be renamed", func(t *testing.T) {
		id := insertAccount(t, pool, "github", "12345", "")
		if _, err := l.ChangePasswordAndSubject(ctx, id, oldPW, newPW, "octo"); !errors.Is(err, authprovider.ErrUsernameFromProvider) {
			t.Fatalf("non-local = %v, want ErrUsernameFromProvider", err)
		}
	})
}

func TestChangeUsername(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	l := authprovider.NewLocal(st)
	const pw = "the-password-123"

	id := insertAccount(t, pool, "local", "first", pw)
	insertAccount(t, pool, "local", "someone-else", pw)
	if got, err := l.ChangeUsername(ctx, id, pw, "second"); err != nil || got != "second" {
		t.Fatalf("ChangeUsername = %q, %v; want second, nil", got, err)
	}
	if s := subjectOf(t, pool, id); s != "second" {
		t.Fatalf("subject = %q, want second", s)
	}
	if _, err := l.ChangeUsername(ctx, id, pw, "UPPER"); !errors.Is(err, authprovider.ErrInvalidUsername) {
		t.Fatalf("ChangeUsername invalid = %v, want ErrInvalidUsername", err)
	}
	if _, err := l.ChangeUsername(ctx, id, "wrong-password-1", "third"); !errors.Is(err, authprovider.ErrInvalidCredentials) {
		t.Fatalf("ChangeUsername wrong password = %v, want ErrInvalidCredentials", err)
	}
	if s := subjectOf(t, pool, id); s != "second" {
		t.Fatalf("subject after a wrong password = %q, want second", s)
	}
	if got, err := l.ChangeUsername(ctx, id, pw, "third"); err != nil || got != "third" {
		t.Fatalf("ChangeUsername = %q, %v", got, err)
	}
	legacy := insertAccount(t, pool, "local", "Legacy.Name_Y", pw)
	if got, err := l.ChangeUsername(ctx, legacy, pw, "Legacy.Name_Y"); err != nil || got != "Legacy.Name_Y" {
		t.Fatalf("ChangeUsername echoing a legacy name = %q, %v; want a no-op", got, err)
	}
	if got, err := l.ChangeUsername(ctx, id, pw, "third"); err != nil || got != "third" {
		t.Fatalf("ChangeUsername to the same name = %q, %v; want a no-op", got, err)
	}
	if _, err := l.ChangeUsername(ctx, id, pw, "someone-else"); !errors.Is(err, authprovider.ErrUsernameTaken) {
		t.Fatalf("ChangeUsername clash = %v, want ErrUsernameTaken", err)
	}
	if s := subjectOf(t, pool, id); s != "third" {
		t.Fatalf("subject after a clash = %q, want third", s)
	}
	// The password is untouched by a rename, and the flag too.
	if _, err := l.Callback(ctx, formRequest("third", pw)); err != nil {
		t.Fatalf("login after rename: %v", err)
	}
	if _, err := l.ChangeUsername(ctx, "00000000-0000-0000-0000-000000000000", pw, "fourth"); !errors.Is(err, authprovider.ErrInvalidCredentials) {
		t.Fatalf("ChangeUsername unknown account = %v, want ErrInvalidCredentials", err)
	}

	gh := insertAccount(t, pool, "github", "999", "")
	if _, err := l.ChangeUsername(ctx, gh, pw, "octo"); !errors.Is(err, authprovider.ErrUsernameFromProvider) {
		t.Fatalf("ChangeUsername non-local = %v, want ErrUsernameFromProvider", err)
	}
}

// TestWrongPasswordDoesNotWaitForTheRowLock: the password is checked before the account row is
// locked, so with the row held FOR UPDATE elsewhere a wrong password still fails straight away
// (it neither queues on the lock nor holds a connection while bcrypt runs). A right password
// does wait for the lock, and goes through once it is released.
func TestWrongPasswordDoesNotWaitForTheRowLock(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	l := authprovider.NewLocal(st)
	const pw = "the-password-123"
	id := insertAccount(t, pool, "local", "locked-row", pw)

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}

	// Were the lock taken first, these would block until the context ran out.
	short, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := l.ChangeUsername(short, id, "wrong-password-1", "renamed"); !errors.Is(err, authprovider.ErrInvalidCredentials) {
		t.Fatalf("ChangeUsername wrong password with the row locked = %v, want ErrInvalidCredentials", err)
	}
	if _, err := l.ChangePasswordAndSubject(short, id, "wrong-password-1", "a-long-enough-new-password", "renamed"); !errors.Is(err, authprovider.ErrInvalidCredentials) {
		t.Fatalf("ChangePasswordAndSubject wrong password with the row locked = %v, want ErrInvalidCredentials", err)
	}
	if short.Err() != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("wrong passwords took %v with the row locked; want them to fail without waiting for the lock", time.Since(start))
	}

	done := make(chan error, 1)
	go func() {
		_, err := l.ChangeUsername(ctx, id, pw, "renamed")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("a right password returned (%v) while the row was locked; want it to wait", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("ChangeUsername after the lock was released: %v", err)
	}
	if s := subjectOf(t, pool, id); s != "renamed" {
		t.Fatalf("subject = %q, want renamed", s)
	}
}

// TestRowChangedBetweenCheckAndLock: the password is checked before the row lock, so a change
// that lands in between (here a password reset, or the account being deleted, by whoever held
// the lock) must fail the request with ErrInvalidCredentials rather than act on a password that
// is no longer the account's.
func TestRowChangedBetweenCheckAndLock(t *testing.T) {
	pool, st := freshStore(t)
	ctx := context.Background()
	l := authprovider.NewLocal(st)
	const pw, resetPW = "the-password-123", "the-reset-password-1"

	// holdAndChange locks id's row, starts call with the right password, and once call is
	// waiting on the lock runs change in the holder's transaction and commits.
	holdAndChange := func(t *testing.T, id string, call func() error, change string, args ...any) error {
		t.Helper()
		holder, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback(ctx) }()
		if _, err := holder.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, id); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			t.Fatalf("call returned (%v) while the row was locked; want it to wait", err)
		case <-time.After(300 * time.Millisecond):
		}
		if _, err := holder.Exec(ctx, change, args...); err != nil {
			t.Fatal(err)
		}
		if err := holder.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return <-done
	}
	newHash := func(t *testing.T, password string) string {
		t.Helper()
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		return string(h)
	}

	t.Run("ChangeUsername after a concurrent password reset", func(t *testing.T) {
		id := insertAccount(t, pool, "local", "reset-under-rename", pw)
		err := holdAndChange(t, id, func() error {
			_, err := l.ChangeUsername(ctx, id, pw, "renamed-a")
			return err
		}, `UPDATE accounts SET password_hash = $2 WHERE id = $1`, id, newHash(t, resetPW))
		if !errors.Is(err, authprovider.ErrInvalidCredentials) {
			t.Fatalf("ChangeUsername = %v, want ErrInvalidCredentials", err)
		}
		if s := subjectOf(t, pool, id); s != "reset-under-rename" {
			t.Fatalf("subject = %q, want it unchanged", s)
		}
		if _, err := l.Callback(ctx, formRequest("reset-under-rename", resetPW)); err != nil {
			t.Fatalf("the reset password must be the one that logs in: %v", err)
		}
		if _, err := l.Callback(ctx, formRequest("reset-under-rename", pw)); err == nil {
			t.Fatal("the old password must not log in")
		}
	})

	t.Run("ChangePasswordAndSubject after a concurrent password reset", func(t *testing.T) {
		id := insertAccount(t, pool, "local", "reset-under-change", pw)
		err := holdAndChange(t, id, func() error {
			_, err := l.ChangePasswordAndSubject(ctx, id, pw, "a-long-enough-new-password", "renamed-b")
			return err
		}, `UPDATE accounts SET password_hash = $2 WHERE id = $1`, id, newHash(t, resetPW))
		if !errors.Is(err, authprovider.ErrInvalidCredentials) {
			t.Fatalf("ChangePasswordAndSubject = %v, want ErrInvalidCredentials", err)
		}
		if s := subjectOf(t, pool, id); s != "reset-under-change" {
			t.Fatalf("subject = %q, want it unchanged", s)
		}
		if _, err := l.Callback(ctx, formRequest("reset-under-change", resetPW)); err != nil {
			t.Fatalf("the reset password must be the one that logs in: %v", err)
		}
	})

	t.Run("ChangeUsername after the account is deleted", func(t *testing.T) {
		id := insertAccount(t, pool, "local", "deleted-under-rename", pw)
		err := holdAndChange(t, id, func() error {
			_, err := l.ChangeUsername(ctx, id, pw, "renamed-c")
			return err
		}, `DELETE FROM accounts WHERE id = $1`, id)
		if !errors.Is(err, authprovider.ErrInvalidCredentials) {
			t.Fatalf("ChangeUsername = %v, want ErrInvalidCredentials", err)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE provider_subject IN ('deleted-under-rename', 'renamed-c')`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("rows = %d (%v), want 0", n, err)
		}
	})
}

func TestCreateLocalAccountFollowsUsernameRules(t *testing.T) {
	_, st := freshStore(t)
	ctx := context.Background()
	l := authprovider.NewLocal(st)
	for _, bad := range []string{"Bad Name", "Alice", "", "x9"} {
		if _, err := l.CreateLocalAccount(ctx, bad, "member"); !errors.Is(err, authprovider.ErrInvalidUsername) {
			t.Errorf("CreateLocalAccount(%q) = %v, want ErrInvalidUsername", bad, err)
		}
	}
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM accounts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("accounts after refused creates = %d (%v), want 0", n, err)
	}
	if _, err := l.CreateLocalAccount(ctx, "alice.b-2", "member"); err != nil {
		t.Fatalf("CreateLocalAccount(alice.b-2) = %v, want nil", err)
	}
}
