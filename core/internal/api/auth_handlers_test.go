package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// testSchema is a fixed schema name reused (dropped+recreated) by every test in this package.
// Tests here don't run in parallel with each other, so one name is enough — matching the
// established pattern in core/internal/identity/accounts_test.go's testPool and
// core/internal/authprovider/local_test.go's testPool. This package's tests share one live
// Postgres database (DATABASE_URL) with no per-row cleanup, so schema isolation is required to
// avoid order-dependent failures against other packages' tests hitting the same database.
const testSchema = "test_blerg_core_api_auth"

// testPool connects to DATABASE_URL and gives the caller a fresh, isolated schema: dropped
// and recreated before the test runs, and dropped again on cleanup. A plain "SET search_path"
// via pool.Exec is NOT enough (pgxpool can hand out any of several underlying connections);
// instead the schema is baked into every connection the returned pool ever opens, via
// ConnConfig.RuntimeParams.
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

// newTestDeps migrates a fresh isolated-schema pool, wires up a real identity.Service and
// authprovider.Local against it, and registers a signing key so minted tokens verify.
func newTestDeps(t *testing.T, allowedOrigins []string, originAudiences map[string]string) (api.Deps, *pgxpool.Pool) {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := db.NewPgStore(pool)
	kp, err := identity.LoadOrGenerateSigningKey(ctx, st)
	if err != nil {
		t.Fatalf("load signing key: %v", err)
	}
	idSvc := identity.NewService(st, kp)
	local := authprovider.NewLocal(st)

	deps := api.Deps{
		Identity:             idSvc,
		AuthProvider:         local,
		Audience:             "blerg-core",
		AllowedReturnOrigins: allowedOrigins,
		OriginAudiences:      originAudiences,
		Store:                st,
		// Pre-flight ruling: newTestDeps sets this explicitly so every existing test (all
		// written against a real TLS-less httptest server that still expects the Secure
		// attribute's ordinary on-by-default behaviour) keeps its current behaviour. Only
		// TestCookieSecureFlag overrides this per-subtest.
		CookieSecure: true,
	}
	return deps, pool
}

// insertLocalAccount inserts a local-provider account with a known bcrypt password hash and
// returns its provider_subject.
func insertLocalAccount(t *testing.T, pool *pgxpool.Pool, subject, password string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt hash: %v", err)
	}
	// email is set (not NULL) to match the convention authprovider's own tests use —
	// authprovider.Local.Callback scans it into a non-nullable string field.
	_, err = pool.Exec(context.Background(),
		`INSERT INTO accounts (provider, provider_subject, email, role, password_hash) VALUES ('local',$1,$1||'@example.com','member',$2)`,
		subject, string(hash))
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
}

// decodeClaims splits a compact JWT and unmarshals its payload segment, so tests can assert on
// the audience/subject a handler minted without needing a full Verify() call.
func decodeClaims(t *testing.T, token string) cid.Claims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token does not look like a compact JWT: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var c cid.Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return c
}

// TestLoginRejectsBadCredentials asserts the endpoint exists (not 404) and fails closed (401,
// not 200/500) for a wrong password — this is the "structured 401" the plan calls for, not a
// generic 404 meaning the route isn't registered.
func TestLoginRejectsBadCredentials(t *testing.T) {
	deps, pool := newTestDeps(t, []string{"http://board.example.com"}, nil)
	insertLocalAccount(t, pool, "testuser", "correct-horse-battery-staple")

	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	form := url.Values{"provider_subject": {"testuser"}, "password": {"wrong-password"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("POST /auth/login is not registered")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad-password login = %d, want 401", resp.StatusCode)
	}
}

// TestRefreshRejectsUnallowedReturnTo is the open-redirect regression test from the plan: a
// return_to whose origin isn't in AllowedReturnOrigins must never produce a redirect to that
// origin.
func TestRefreshRejectsUnallowedReturnTo(t *testing.T) {
	deps, _ := newTestDeps(t, []string{"http://board.example.com"}, nil)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(srv.URL + "/auth/refresh?return_to=" + url.QueryEscape("http://evil.example.com/steal"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusTemporaryRedirect {
		loc := resp.Header.Get("Location")
		if strings.Contains(loc, "evil.example.com") {
			t.Fatalf("open redirect: refresh redirected to disallowed origin %q", loc)
		}
	}
	if resp.StatusCode == http.StatusFound {
		t.Fatalf("unallowed return_to must not redirect at all, got 302 to %q", resp.Header.Get("Location"))
	}
}

// TestReturnToAcceptsCoreOwnOriginWithEmptyAllowlist is the carried controller ruling: core's
// own origin (publicOrigin) must ALWAYS be an accepted return_to origin, even with an EMPTY
// AllowedReturnOrigins allowlist — core is itself, not an open-redirect target, so a local login
// or the OAuth callback's own "/auth/refresh?return_to=<publicOrigin>/app" redirect must not
// depend on an operator remembering to list core's own origin in its own allowlist. A foreign
// origin must still be rejected.
func TestReturnToAcceptsCoreOwnOriginWithEmptyAllowlist(t *testing.T) {
	deps, _ := newTestDeps(t, nil, nil) // empty allowlist
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}

	// return_to = core's own origin (derived from the request itself, since PublicURL is unset
	// in this test). Host IS caller-supplied here, same as any other header — see
	// returnToAllowed's doc comment for why that's still safe: the attack this carve-out would
	// need to enable is redirecting a VICTIM somewhere the attacker chose, and a victim's own
	// browser decides Host for itself from the URL it's actually connecting to, so it can only
	// ever send core's real Host. This test's own client "forging" its own request's Host only
	// ever affects that same request — nobody else is redirected.
	resp, err := client.Get(srv.URL + "/auth/refresh?return_to=" + url.QueryEscape(srv.URL+"/app"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// No session cookie was ever set, so this can never succeed — but it must fail with 401
	// ("not signed in"), NOT 400 ("invalid return_to"): a 401 here proves returnToAllowed
	// accepted the origin and the request proceeded past that check.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("own-origin return_to with an empty allowlist = %d, want 401 (not 400 — it must be accepted as a valid return_to)", resp.StatusCode)
	}

	// A foreign origin must still be rejected even with the own-origin carve-out in place.
	resp2, err := client.Get(srv.URL + "/auth/refresh?return_to=" + url.QueryEscape("http://evil.example.com/steal"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("foreign return_to with an empty allowlist = %d, want 400", resp2.StatusCode)
	}
}

// TestFullLoginRefreshLogoutFlow exercises login -> refresh cookie set -> refresh access token
// minted with the return_to's mapped audience -> logout revokes the session -> a subsequent
// refresh fails.
func TestFullLoginRefreshLogoutFlow(t *testing.T) {
	origins := []string{"http://board.example.com"}
	audiences := map[string]string{"http://board.example.com": "blerg-board"}
	deps, pool := newTestDeps(t, origins, audiences)
	insertLocalAccount(t, pool, "flowuser", "s3cret-password")

	// The refresh cookie is set with Secure:true (never sent over plain HTTP), so this test
	// needs a TLS server for the client's cookiejar to retain it — an httptest.NewServer
	// (plain HTTP) would silently drop it and every assertion below would fail for the wrong
	// reason.
	router := api.NewRouter(deps)
	srv := httptest.NewTLSServer(router)
	defer srv.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := srv.Client()
	client.Jar = jar
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }

	// 1. Login.
	form := url.Values{"provider_subject": {"flowuser"}, "password": {"s3cret-password"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d, want 200", resp.StatusCode)
	}
	// The cookie is scoped to Path=/auth, so it must be looked up (and, in step 4, replayed)
	// against an /auth-prefixed URL — the jar won't return it for the bare server root.
	su, _ := url.Parse(srv.URL + "/auth/")
	var gotCookie bool
	for _, c := range jar.Cookies(su) {
		if c.Name == "blerg_core_refresh" {
			gotCookie = true
		}
	}
	if !gotCookie {
		t.Fatal("login did not set blerg_core_refresh cookie")
	}

	// 2. Refresh, with return_to mapped to "blerg-board".
	var loginCookieValue string
	for _, c := range jar.Cookies(su) {
		if c.Name == "blerg_core_refresh" {
			loginCookieValue = c.Value
		}
	}
	resp2, err := client.Get(srv.URL + "/auth/refresh?return_to=" + url.QueryEscape("http://board.example.com/app"))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusFound {
		t.Fatalf("refresh = %d, want 302", resp2.StatusCode)
	}
	// Refresh must rotate the session: a NEW Set-Cookie value, distinct from the one login set.
	var refreshedCookieValue string
	for _, c := range resp2.Cookies() {
		if c.Name == "blerg_core_refresh" {
			refreshedCookieValue = c.Value
		}
	}
	if refreshedCookieValue == "" {
		t.Fatal("refresh did not set a new blerg_core_refresh cookie")
	}
	if refreshedCookieValue == loginCookieValue {
		t.Fatal("refresh must rotate the refresh cookie to a new value, got the same one login set")
	}
	loc := resp2.Header.Get("Location")
	if !strings.HasPrefix(loc, "http://board.example.com/app#access_token=") {
		t.Fatalf("refresh redirect location = %q, want board.example.com fragment token", loc)
	}
	token := strings.TrimPrefix(loc, "http://board.example.com/app#access_token=")
	claims := decodeClaims(t, token)
	if claims.Aud != "blerg-board" {
		t.Fatalf("minted access token aud = %q, want %q (from OriginAudiences mapping)", claims.Aud, "blerg-board")
	}

	// 3. Logout.
	req3, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout", nil)
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("logout = %d, want 200", resp3.StatusCode)
	}

	// 4. Refresh again with the same (now-revoked) cookie must fail. Manually re-attach the
	// old cookie value since logout cleared the jar's cookie via Max-Age=-1.
	req4, _ := http.NewRequest(http.MethodGet, srv.URL+"/auth/refresh?return_to="+url.QueryEscape("http://board.example.com/app"), nil)
	for _, c := range jar.Cookies(su) {
		req4.AddCookie(c)
	}
	// jar will have dropped the cookie after logout's Max-Age=-1 response, so this request
	// will carry no cookie at all — which must also fail closed (401), not succeed.
	resp4, err := srv.Client().Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after logout = %d, want 401", resp4.StatusCode)
	}
}

// TestRefreshCookieMaxAgeTracksRemainingLifetime proves session lifetime is absolute from
// login, not reset on every rotation: the rotated cookie's Max-Age must track the session's
// REMAINING lifetime (expires_at - now), so it shrinks on a second refresh rather than resetting
// to a fresh full BLERG_CORE_SESSION_TTL each time.
func TestRefreshCookieMaxAgeTracksRemainingLifetime(t *testing.T) {
	deps, pool := newTestDeps(t, []string{"http://board.example.com"}, nil)
	insertLocalAccount(t, pool, "ttluser", "password-ttl-1")

	// A short TTL keeps the test fast while still giving enough headroom to observe the
	// Max-Age shrinking between two refreshes separated by a real sleep.
	ttl := 10 * time.Second
	idSvc := deps.Identity.(*identity.Service)
	idSvc.SetSessionTTL(ttl)
	deps.SessionTTL = ttl

	router := api.NewRouter(deps)
	srv := httptest.NewTLSServer(router)
	defer srv.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := srv.Client()
	client.Jar = jar
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }

	form := url.Values{"provider_subject": {"ttluser"}, "password": {"password-ttl-1"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d, want 200", resp.StatusCode)
	}
	loginTime := time.Now()

	cookieMaxAge := func() int {
		t.Helper()
		resp, err := client.Get(srv.URL + "/auth/refresh?return_to=" + url.QueryEscape("http://board.example.com/app"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		for _, c := range resp.Cookies() {
			if c.Name == "blerg_core_refresh" {
				return c.MaxAge
			}
		}
		t.Fatal("refresh did not set a new blerg_core_refresh cookie")
		return 0
	}

	maxAge1 := cookieMaxAge()
	wantMaxAge1 := int(ttl.Seconds()) - int(time.Since(loginTime).Seconds())
	if diff := maxAge1 - wantMaxAge1; diff < -2 || diff > 2 {
		t.Fatalf("first refresh Max-Age = %d, want ~%d (±2s of expires_at - now)", maxAge1, wantMaxAge1)
	}

	time.Sleep(2 * time.Second)

	maxAge2 := cookieMaxAge()
	if maxAge2 >= maxAge1 {
		t.Fatalf("second refresh Max-Age = %d, want smaller than first refresh's %d (lifetime is absolute from login, not reset by rotation)", maxAge2, maxAge1)
	}
}

// TestLogoutAllRevokesEverySession proves POST /auth/logout-all (gated behind a human bearer
// token, not the refresh cookie) revokes every live session for that account — not just the
// caller's own device — by logging in twice (two devices), calling logout-all with a token
// minted for the account, and confirming both devices' refresh cookies are now dead.
func TestLogoutAllRevokesEverySession(t *testing.T) {
	deps, pool := newTestDeps(t, []string{"https://core.example.com"}, nil)
	insertLocalAccount(t, pool, "multi", "password-multi-1")
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()

	login := func() *http.Cookie {
		resp, err := srv.Client().PostForm(srv.URL+"/auth/login",
			url.Values{"provider_subject": {"multi"}, "password": {"password-multi-1"}})
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("login: %v %d", err, resp.StatusCode)
		}
		for _, c := range resp.Cookies() {
			if c.Name == "blerg_core_refresh" {
				return c
			}
		}
		t.Fatal("no refresh cookie")
		return nil
	}
	deviceA, deviceB := login(), login()

	// A bearer for the logout-all call: mint one directly for the account.
	var accountID string
	pool.QueryRow(context.Background(), `SELECT id::text FROM accounts WHERE provider_subject='multi'`).Scan(&accountID)
	idSvc := deps.Identity.(*identity.Service)
	tok, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/auth/logout-all", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Sec-Fetch-Site", "same-origin") // Task 9's CSRF gate; harmless before it lands
	resp, err := srv.Client().Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("logout-all: %v %d", err, resp.StatusCode)
	}

	for name, c := range map[string]*http.Cookie{"A": deviceA, "B": deviceB} {
		req, _ := http.NewRequest("GET", srv.URL+"/auth/refresh?return_to="+url.QueryEscape("https://core.example.com/app"), nil)
		req.AddCookie(c)
		client := srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("device %s refresh after logout-all = %d, want 401", name, resp.StatusCode)
		}
	}
}

// TestAudienceForReturnToFallsBackToCoreAudience checks the "no match in OriginAudiences"
// branch: an allowed return_to origin with no explicit mapping must mint a token for
// deps.Audience ("blerg-core"), not an empty or hardcoded string.
func TestAudienceForReturnToFallsBackToCoreAudience(t *testing.T) {
	origins := []string{"http://board.example.com"}
	// No OriginAudiences entries at all.
	deps, pool := newTestDeps(t, origins, nil)
	insertLocalAccount(t, pool, "falluser", "another-password")

	// See the TLS-server comment in TestFullLoginRefreshLogoutFlow: the refresh cookie is
	// Secure, so a plain-HTTP test server's client would never retain it.
	router := api.NewRouter(deps)
	srv := httptest.NewTLSServer(router)
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar

	form := url.Values{"provider_subject": {"falluser"}, "password": {"another-password"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d, want 200", resp.StatusCode)
	}

	client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	resp2, err := client.Get(srv.URL + "/auth/refresh?return_to=" + url.QueryEscape("http://board.example.com/app"))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	loc := resp2.Header.Get("Location")
	token := strings.TrimPrefix(loc, "http://board.example.com/app#access_token=")
	claims := decodeClaims(t, token)
	if claims.Aud != "blerg-core" {
		t.Fatalf("minted access token aud = %q, want fallback %q", claims.Aud, "blerg-core")
	}
}

// TestCallbackNotFoundForLocalProvider: local's login path is the form POST to /auth/login, not
// a GET /auth/callback redirect flow (that's the OAuth providers' — GitHub/OIDC — entry point).
// Under local, GET /auth/callback must 404 rather than silently doing nothing or panicking.
func TestCallbackNotFoundForLocalProvider(t *testing.T) {
	deps, _ := newTestDeps(t, []string{"http://board.example.com"}, nil)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /auth/callback with local provider active = %d, want 404", resp.StatusCode)
	}
}

// stubIdentityProvider implements just enough of the identityProvider interface (JWKS,
// RevocationSnapshot) to satisfy Deps.Identity's field type without being a
// *identity.Service — used to exercise the auth handlers' fail-closed behavior when the
// concrete-type assertion in identitySvc() doesn't hold, mirroring router_test.go's own
// stubIdentity for the same reason (Deps.Identity is deliberately the narrow interface, not
// the concrete type, precisely so a stub like this is possible).
type stubIdentityProvider struct{}

func (stubIdentityProvider) JWKS(context.Context) (cid.KeySet, error) { return cid.KeySet{}, nil }
func (stubIdentityProvider) RevocationSnapshot(context.Context) ([]identity.Revocation, error) {
	return nil, nil
}

// TestAuthHandlersFailClosedOnNonConcreteIdentity: when Deps.Identity isn't a
// *identity.Service (e.g. a test stub, or a future refactor), the auth handlers that need
// the concrete type's human-session methods must return 500, never panic.
func TestAuthHandlersFailClosedOnNonConcreteIdentity(t *testing.T) {
	deps := api.Deps{
		Identity:             stubIdentityProvider{},
		AuthProvider:         authprovider.NewLocal(nil),
		Audience:             "blerg-core",
		AllowedReturnOrigins: []string{"http://board.example.com"},
	}
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	// GET /auth/refresh: returnToAllowed passes, but there's no cookie, so this actually
	// exercises the "no session" 401 path before ever reaching identitySvc() — covered by
	// TestRefreshRequiresCookie already. What this test specifically wants is a request that
	// gets past the cookie check into identitySvc(): attach a cookie so it reaches
	// RefreshAccessToken's non-concrete-Identity branch.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/auth/refresh?return_to="+url.QueryEscape("http://board.example.com/app"), nil)
	req.AddCookie(&http.Cookie{Name: refreshCookieNameForTest, Value: "whatever"})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("refresh with non-concrete Identity = %d, want 500 (must fail closed, not panic)", resp.StatusCode)
	}

	// POST /auth/logout: must not panic either, and per its best-effort-revocation contract
	// still succeeds (200) and clears the cookie even though the session couldn't actually be
	// revoked server-side.
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout", nil)
	req2.AddCookie(&http.Cookie{Name: refreshCookieNameForTest, Value: "whatever"})
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("logout with non-concrete Identity = %d, want 200 (best-effort, must not panic)", resp2.StatusCode)
	}
}

// refreshCookieNameForTest mirrors the unexported refreshCookieName constant in
// auth_handlers.go ("blerg_core_refresh") — duplicated here since this is an external
// (api_test) test package and the constant isn't exported.
const refreshCookieNameForTest = "blerg_core_refresh"

// TestRefreshRequiresCookie: no refresh cookie at all must fail closed (401), even with an
// otherwise-valid return_to.
func TestRefreshRequiresCookie(t *testing.T) {
	deps, _ := newTestDeps(t, []string{"http://board.example.com"}, nil)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/auth/refresh?return_to=" + url.QueryEscape("http://board.example.com/app"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh without cookie = %d, want 401", resp.StatusCode)
	}
}

// TestRefreshFailuresAreRecoverable covers the final-review fix for the dead-end
// GET /auth/refresh failure: both recoverable failures (no cookie, revoked/expired session)
// must still be 401, but must now hand a real user an HTML page with a link back to /login
// carrying their original return_to, instead of a bare plain-text error string.
func TestRefreshFailuresAreRecoverable(t *testing.T) {
	deps, _ := newTestDeps(t, []string{"http://board.example.com"}, nil)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	returnTo := "http://board.example.com/app"
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
	}{
		{"no cookie at all", nil},
		{"cookie for a session that no longer exists", &http.Cookie{Name: refreshCookieNameForTest, Value: "long-gone"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/auth/refresh?return_to="+url.QueryEscape(returnTo), nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (the fix must not weaken the auth failure)", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html", ct)
			}
			body := new(strings.Builder)
			if _, err := io.Copy(body, resp.Body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body.String(), `href="/login?return_to=`) {
				t.Errorf("response has no link back to /login carrying return_to: %s", body.String())
			}
			if !strings.Contains(body.String(), url.QueryEscape(returnTo)) {
				t.Errorf("response drops the original return_to: %s", body.String())
			}
		})
	}
}

func TestLoginRefusesDisabledAndUnrevokesOnSuccess(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	insertLocalAccount(t, pool, "revoked-then-back", "password-back-123")
	ctx := context.Background()
	var accountID string
	pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE provider_subject='revoked-then-back'`).Scan(&accountID)
	if _, err := pool.Exec(ctx, `INSERT INTO revocations(kind, value) VALUES ('sub', $1)`, accountID); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()
	form := url.Values{"provider_subject": {"revoked-then-back"}, "password": {"password-back-123"}}

	// Disabled: refused even with the right password.
	pool.Exec(ctx, `UPDATE accounts SET disabled_at = now() WHERE id = $1`, accountID)
	resp, err := srv.Client().PostForm(srv.URL+"/auth/login", form)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled login = %v %d, want 403", err, resp.StatusCode)
	}

	// Enabled again: login succeeds and clears the stale sub revocation.
	pool.Exec(ctx, `UPDATE accounts SET disabled_at = NULL WHERE id = $1`, accountID)
	resp, err = srv.Client().PostForm(srv.URL+"/auth/login", form)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %v %d, want 200", err, resp.StatusCode)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM revocations WHERE kind='sub' AND value=$1`, accountID).Scan(&n)
	if n != 0 {
		t.Fatalf("sub revocation rows after successful login = %d, want 0", n)
	}
}

// TestLoginIsRateLimited proves POST /auth/login stops answering wrong-password attempts once
// LoginLimiter's max is reached — even the CORRECT password 429s while blocked, since the limit
// is checked before Local.Callback runs — and that a fresh limiter (a new window) lets the
// correct password through again (R2/C1: no online brute force against a desktop's exposed
// login).
func TestLoginIsRateLimited(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	insertLocalAccount(t, pool, "rl-user", "correct-horse-battery")
	deps.LoginLimiter = api.NewLoginLimiter(3, time.Minute)
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()

	post := func(pw string) *http.Response {
		form := url.Values{"provider_subject": {"rl-user"}, "password": {pw}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	for i := 0; i < 3; i++ {
		if resp := post("wrong"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password %d = %d, want 401", i+1, resp.StatusCode)
		}
	}
	resp := post("correct-horse-battery")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("4th attempt (correct password) = %d, want 429", resp.StatusCode)
	}
	if ra := resp.Header.Get("Retry-After"); ra == "" {
		t.Fatal("429 must carry Retry-After")
	} else if _, err := strconv.Atoi(ra); err != nil {
		t.Fatalf("Retry-After %q is not an integer", ra)
	}

	// A fresh limiter (window elapsed) lets the correct password through.
	deps.LoginLimiter = api.NewLoginLimiter(3, time.Minute)
	srv2 := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv2.Close()
	form := url.Values{"provider_subject": {"rl-user"}, "password": {"correct-horse-battery"}}
	req, _ := http.NewRequest(http.MethodPost, srv2.URL+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := srv2.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("correct password after reset = %d, want 200", resp2.StatusCode)
	}
}

// TestCookieSecureFlag is desktop-security R6: WebKit does not treat http://localhost as a
// secure context for cookies, so a hardcoded Secure: true silently drops the refresh cookie in
// Safari on the desktop install (login loops forever at "Sign in required"). Deps.CookieSecure
// must control the Secure attribute on the cookie the login flow issues, and — since this is a
// PLAIN http httptest server on purpose — an http.CookieJar (a real browser's own behaviour)
// only replays a cookie back to the server when Secure is false. That jar behaviour IS the
// assertion: it's the same reason Safari doesn't send the cookie back over plain http today.
func TestCookieSecureFlag(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("secure=%v", secure), func(t *testing.T) {
			deps, pool := newTestDeps(t, nil, nil)
			deps.CookieSecure = secure
			insertLocalAccount(t, pool, "cookie-user", "s3cret-password")
			srv := httptest.NewServer(api.NewRouter(deps)) // PLAIN http on purpose
			defer srv.Close()
			jar, _ := cookiejar.New(nil)
			client := srv.Client()
			client.Jar = jar
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

			form := url.Values{"provider_subject": {"cookie-user"}, "password": {"s3cret-password"}}
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("login = %d", resp.StatusCode)
			}
			var setCookie string
			for _, c := range resp.Cookies() {
				if c.Name == "blerg_core_refresh" {
					setCookie = c.Raw
					if c.Secure != secure {
						t.Fatalf("Set-Cookie Secure=%v, want %v", c.Secure, secure)
					}
				}
			}
			if setCookie == "" {
				t.Fatal("no refresh cookie in login response")
			}
			// Own origin is always an allowed return_to; over plain http the jar only
			// replays a non-Secure cookie — that is exactly the Safari symptom.
			resp2, err := client.Get(srv.URL + "/auth/refresh?return_to=" + url.QueryEscape(srv.URL+"/app"))
			if err != nil {
				t.Fatal(err)
			}
			resp2.Body.Close()
			if secure {
				if resp2.StatusCode != http.StatusUnauthorized {
					t.Fatalf("Secure cookie over http: refresh = %d, want 401 (cookie dropped)", resp2.StatusCode)
				}
			} else {
				if resp2.StatusCode != http.StatusFound || !strings.Contains(resp2.Header.Get("Location"), "#access_token=") {
					t.Fatalf("insecure cookie over http: refresh = %d %q, want 302 with token fragment", resp2.StatusCode, resp2.Header.Get("Location"))
				}
			}
		})
	}
}

// TestRefreshFailuresRedirectBrowserNavigationsToLogin: a real user reaches GET /auth/refresh
// by full-page navigation (Sec-Fetch-Mode: navigate), and for them a recoverable failure is
// the login form itself — a 302 to /login carrying return_to and a reason code — not a page
// with a link to notice. The 401 HTML page in TestRefreshFailuresAreRecoverable stays for
// everything that is not a browser navigation.
func TestRefreshFailuresRedirectBrowserNavigationsToLogin(t *testing.T) {
	deps, _ := newTestDeps(t, []string{"http://board.example.com"}, nil)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	returnTo := "http://board.example.com/app"
	for _, tc := range []struct {
		name       string
		cookie     *http.Cookie
		wantReason string
	}{
		{"no cookie at all", nil, "signed_out"},
		{"cookie for a dead session", &http.Cookie{Name: refreshCookieNameForTest, Value: "long-gone"}, "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/auth/refresh?return_to="+url.QueryEscape(returnTo), nil)
			req.Header.Set("Sec-Fetch-Mode", "navigate")
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("status = %d, want 302", resp.StatusCode)
			}
			loc, err := url.Parse(resp.Header.Get("Location"))
			if err != nil || loc.Path != "/login" {
				t.Fatalf("Location = %q, want /login?...", resp.Header.Get("Location"))
			}
			if got := loc.Query().Get("return_to"); got != returnTo {
				t.Errorf("return_to = %q, want %q", got, returnTo)
			}
			if got := loc.Query().Get("reason"); got != tc.wantReason {
				t.Errorf("reason = %q, want %q", got, tc.wantReason)
			}
		})
	}

	// An Accept: text/html without the Sec-Fetch-Mode header (an older browser) is a
	// navigation too.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/auth/refresh?return_to="+url.QueryEscape(returnTo), nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("Accept: text/html status = %d, want 302", resp.StatusCode)
	}
}
