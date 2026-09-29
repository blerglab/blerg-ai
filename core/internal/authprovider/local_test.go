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
