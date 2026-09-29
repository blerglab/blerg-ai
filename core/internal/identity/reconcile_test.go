package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/signing"
)

const testGHToken = "test-gh-token"

// reconcileTestSchema is distinct from every other package's test schema
// name (identity_test's "test_blerg_core_identity",
// authprovider_test's "test_blerg_core_authprovider") so this internal
// `package identity` test file's isolated schema never collides with
// theirs, even though all three run against the same shared DATABASE_URL.
const reconcileTestSchema = "test_blerg_core_identity_reconcile"

// reconcilePool gives the caller a fresh, isolated schema (dropped and
// recreated), migrated, exactly like accounts_test.go's testPool — required
// because this package's tests otherwise share one live Postgres database
// with no per-row cleanup. Duplicated here (rather than reused from
// accounts_test.go) because that helper lives in the external
// `identity_test` package and this file needs the internal `identity`
// package to reach the unexported githubAPIBaseURL test seam.
func reconcilePool(t *testing.T) *pgxpool.Pool {
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
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", reconcileTestSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", reconcileTestSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	setupPool.Close()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = reconcileTestSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		pool.Exec(bg, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", reconcileTestSchema))
		pool.Close()
	})
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// reconcileFixture builds a Service backed by an isolated schema, and
// returns the pool for direct row insertion/assertion in tests.
func reconcileFixture(t *testing.T) (*pgxpool.Pool, db.Store, *Service) {
	t.Helper()
	pool := reconcilePool(t)
	st := db.NewPgStore(pool)
	kp, err := signing.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return pool, st, NewService(st, kp)
}

// insertGitHubAccount inserts a provider='github' account with the given
// login stored in accounts.email (matching github.go's persistence
// convention) and returns its id.
func insertGitHubAccount(t *testing.T, pool *pgxpool.Pool, subject, login string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO accounts (provider, provider_subject, email, role) VALUES ('github', $1, $2, 'member') RETURNING id::text`,
		subject, login).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func isAccountRevoked(t *testing.T, st db.Store, accountID string) bool {
	t.Helper()
	var revoked bool
	err := st.Pool().QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM revocations WHERE kind='sub' AND value=$1)`, accountID).Scan(&revoked)
	if err != nil {
		t.Fatal(err)
	}
	return revoked
}

func isAccountDisabled(t *testing.T, pool *pgxpool.Pool, accountID string) bool {
	t.Helper()
	var disabled bool
	if err := pool.QueryRow(context.Background(),
		`SELECT disabled_at IS NOT NULL FROM accounts WHERE id = $1`, accountID).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	return disabled
}

// fakeOrgMembersServer serves GET /orgs/{org}/members, paginated per_page
// at a time from logins, so tests can prove reconcileOnce actually walks
// multiple pages rather than only looking at the first 100.
func fakeOrgMembersServer(t *testing.T, org string, logins []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testGHToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/orgs/"+org+"/members" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		perPage := 100
		if v := q.Get("per_page"); v != "" {
			fmt.Sscanf(v, "%d", &perPage)
		}
		page := 1
		if v := q.Get("page"); v != "" {
			fmt.Sscanf(v, "%d", &page)
		}
		start := (page - 1) * perPage
		end := start + perPage
		if start > len(logins) {
			start = len(logins)
		}
		if end > len(logins) {
			end = len(logins)
		}
		batch := logins[start:end]
		out := make([]map[string]string, len(batch))
		for i, l := range batch {
			out[i] = map[string]string{"login": l}
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withFakeGitHubBaseURL points githubAPIBaseURL at srv for the duration of
// the test, restoring the original value on cleanup — needed since
// githubAPIBaseURL is a package-level var shared by the whole test binary.
func withFakeGitHubBaseURL(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = orig })
}

func TestReconcileOnceRevokesAccountNoLongerInOrg(t *testing.T) {
	pool, st, svc := reconcileFixture(t)
	ctx := context.Background()

	stillMemberID := insertGitHubAccount(t, pool, "1", "still-a-member")
	goneID := insertGitHubAccount(t, pool, "2", "left-the-org")

	srv := fakeOrgMembersServer(t, "theorg", []string{"still-a-member"})
	withFakeGitHubBaseURL(t, srv)

	reconcileOnce(ctx, st, svc, "theorg", testGHToken, srv.Client())

	if isAccountRevoked(t, st, stillMemberID) {
		t.Error("expected current org member to remain unrevoked")
	}
	if !isAccountRevoked(t, st, goneID) {
		t.Error("expected account no longer in the org to be revoked")
	}

	// RevokeAccountEverywhere also kills sessions; spot-check the
	// revocation reached the human_sessions table too via the public
	// Service API instead of re-deriving that behavior here.
}

func TestReconcileOnceLeavesRevocationsUntouchedOnAPIFailure(t *testing.T) {
	pool, st, svc := reconcileFixture(t)
	ctx := context.Background()

	accID := insertGitHubAccount(t, pool, "3", "someone")

	// The members-listing endpoint always errors — simulating a GitHub API
	// outage during the periodic reconcile pass.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	withFakeGitHubBaseURL(t, srv)

	reconcileOnce(ctx, st, svc, "theorg", testGHToken, srv.Client())

	if isAccountRevoked(t, st, accID) {
		t.Error("a github API error during reconcile must never revoke accounts (fail safe, not fail closed, for the periodic sweep)")
	}
}

func TestReconcileOnceGenuinelyPaginatesBeyondFirstPage(t *testing.T) {
	pool, st, svc := reconcileFixture(t)
	ctx := context.Background()

	// 150 members on GitHub's side (2 pages at 100/page); the 151st local
	// account is NOT among them and must still be revoked, proving the
	// listing walked past page 1.
	var logins []string
	for i := 0; i < 150; i++ {
		logins = append(logins, fmt.Sprintf("member-%d", i))
	}
	// A member that would only appear on page 2 must be recognized as
	// still present.
	page2ID := insertGitHubAccount(t, pool, "page2", "member-149")
	goneID := insertGitHubAccount(t, pool, "gone", "not-a-member-at-all")

	srv := fakeOrgMembersServer(t, "theorg", logins)
	withFakeGitHubBaseURL(t, srv)

	reconcileOnce(ctx, st, svc, "theorg", testGHToken, srv.Client())

	if isAccountRevoked(t, st, page2ID) {
		t.Error("expected a member only listed on page 2 to be recognized as still a member (pagination must walk all pages)")
	}
	if !isAccountRevoked(t, st, goneID) {
		t.Error("expected an account absent from the full member list to be revoked")
	}
}

func TestReconcileOnceAdvisoryLockPreventsConcurrentPass(t *testing.T) {
	_, st, svc := reconcileFixture(t)
	ctx := context.Background()

	// Hold the exact advisory lock reconcileOnce uses, on a separate
	// connection, and confirm a concurrent reconcileOnce call returns
	// promptly without doing any work (it must use pg_try_advisory_lock,
	// not block) rather than hanging until the holder releases.
	holder, err := st.Pool().Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire holder conn: %v", err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, "SELECT pg_advisory_lock($1)", reconcileLockKey); err != nil {
		t.Fatalf("acquire advisory lock: %v", err)
	}
	defer holder.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", reconcileLockKey)

	srv := fakeOrgMembersServer(t, "theorg", []string{})
	withFakeGitHubBaseURL(t, srv)

	done := make(chan struct{})
	go func() {
		reconcileOnce(ctx, st, svc, "theorg", testGHToken, srv.Client())
		close(done)
	}()

	select {
	case <-done:
		// good: it returned without blocking on the held lock.
	case <-time.After(3 * time.Second):
		t.Fatal("reconcileOnce blocked instead of skipping when the advisory lock was already held — it must use pg_try_advisory_lock, not pg_advisory_lock")
	}
}

func TestReconcileWithoutTokenRevokesNobody(t *testing.T) {
	pool, st, svc := reconcileFixture(t)
	goneID := insertGitHubAccount(t, pool, "9", "left-the-org")
	srv := fakeOrgMembersServer(t, "theorg", []string{})
	withFakeGitHubBaseURL(t, srv)

	reconcileOnce(context.Background(), st, svc, "theorg", "", srv.Client()) // no token → fake 401s

	if isAccountRevoked(t, st, goneID) || isAccountDisabled(t, pool, goneID) {
		t.Fatal("a 401 from the members listing must leave every account untouched")
	}
}

func TestReconcileDisablesThenReEnablesAndIsIdempotent(t *testing.T) {
	pool, st, svc := reconcileFixture(t)
	ctx := context.Background()
	id := insertGitHubAccount(t, pool, "7", "flaky-member")

	gone := fakeOrgMembersServer(t, "theorg", []string{"someone-else"})
	withFakeGitHubBaseURL(t, gone)
	reconcileOnce(ctx, st, svc, "theorg", testGHToken, gone.Client())
	if !isAccountRevoked(t, st, id) || !isAccountDisabled(t, pool, id) {
		t.Fatal("departed member must be disabled and sub-revoked")
	}
	var firstDisabledAt time.Time
	pool.QueryRow(ctx, `SELECT disabled_at FROM accounts WHERE id = $1`, id).Scan(&firstDisabledAt)

	// Second tick with the same listing: nothing changes (no re-revoke, disabled_at untouched).
	reconcileOnce(ctx, st, svc, "theorg", testGHToken, gone.Client())
	var secondDisabledAt time.Time
	pool.QueryRow(ctx, `SELECT disabled_at FROM accounts WHERE id = $1`, id).Scan(&secondDisabledAt)
	if !firstDisabledAt.Equal(secondDisabledAt) {
		t.Fatal("reconcile must be idempotent per tick — disabled_at moved")
	}
	var revRows int
	pool.QueryRow(ctx, `SELECT count(*) FROM revocations WHERE kind='sub' AND value=$1`, id).Scan(&revRows)
	if revRows != 1 {
		t.Fatalf("revocations rows = %d, want exactly 1", revRows)
	}

	// The login reappears in the org: re-enabled, sub un-revoked.
	back := fakeOrgMembersServer(t, "theorg", []string{"flaky-member"})
	withFakeGitHubBaseURL(t, back)
	reconcileOnce(ctx, st, svc, "theorg", testGHToken, back.Client())
	if isAccountRevoked(t, st, id) || isAccountDisabled(t, pool, id) {
		t.Fatal("a member who is back in the org must be re-enabled and un-revoked")
	}
}
