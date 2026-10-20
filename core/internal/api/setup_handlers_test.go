package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/db"
)

// setupServer is core's router over an empty accounts table, with a stub web app.
func setupServer(t *testing.T, allowLocal bool) (*httptest.Server, *pgxpool.Pool, *authprovider.Local) {
	t.Helper()
	deps, pool := newTestDeps(t, nil, nil)
	deps.CookieSecure = false
	deps.SetupAllowLocal = allowLocal
	deps.App = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "the web app") })
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	return srv, pool, authprovider.NewLocal(db.NewPgStore(pool))
}

func noRedirectClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// get issues a GET with an optional Host override (the browser's idea of where core lives).
func get(t *testing.T, c *http.Client, rawURL, host string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func postSetup(t *testing.T, c *http.Client, srvURL, host string, body map[string]string) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srvURL+"/auth/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if host != "" {
		req.Host = host
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func providerSetup(t *testing.T, srvURL, host string) string {
	t.Helper()
	resp := get(t, http.DefaultClient, srvURL+"/auth/provider", host)
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["id"] != "local" {
		t.Fatalf("provider id %q", body["id"])
	}
	return body["setup"]
}

func TestProviderReportsSetupMode(t *testing.T) {
	srv, pool, _ := setupServer(t, false)
	if got := providerSetup(t, srv.URL, ""); got != "code" {
		t.Fatalf("empty install without local allow: setup %q, want code", got)
	}
	srvLocal, _, _ := setupServer(t, true)
	if got := providerSetup(t, srvLocal.URL, ""); got != "open" {
		t.Fatalf("local allow, loopback host: setup %q, want open", got)
	}
	if got := providerSetup(t, srvLocal.URL, "blerg.example.com"); got != "code" {
		t.Fatalf("local allow, non-loopback host: setup %q, want code", got)
	}
	insertLocalAccount(t, pool, "someone", "a-good-long-password")
	if got := providerSetup(t, srv.URL, ""); got != "" {
		t.Fatalf("with an account: setup %q, want none", got)
	}
}

// The whole flow: open the link, create the account, land signed in on the app — no forced
// password change.
func TestSetupWithCodeSignsInWithoutForcedChange(t *testing.T) {
	srv, pool, local := setupServer(t, false)
	code, err := local.MintSetupCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := noRedirectClient(t)
	page := get(t, c, srv.URL+"/setup", "")
	if page.StatusCode != http.StatusOK || page.Header.Get("Cache-Control") != "no-store" || page.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("GET /setup = %d, Cache-Control %q, Referrer-Policy %q", page.StatusCode, page.Header.Get("Cache-Control"), page.Header.Get("Referrer-Policy"))
	}
	assertNoFraming(t, "GET /setup", page)
	resp := postSetup(t, c, srv.URL, "", map[string]string{"code": code, "provider_subject": "owner", "password": "a-good-long-password"})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /auth/setup = %d: %s", resp.StatusCode, b)
	}
	ref := get(t, c, srv.URL+"/auth/refresh?return_to="+url.QueryEscape(srv.URL+"/app"), "")
	loc := ref.Header.Get("Location")
	if ref.StatusCode != http.StatusFound || !strings.HasPrefix(loc, srv.URL+"/app#access_token=") {
		t.Fatalf("refresh after setup = %d -> %q, want the app with a token", ref.StatusCode, loc)
	}
	var role string
	if err := pool.QueryRow(context.Background(), `SELECT role FROM accounts WHERE provider_subject = 'owner'`).Scan(&role); err != nil || role != "admin" {
		t.Fatalf("role %q (%v), want admin", role, err)
	}
}

func TestSetupRefusesMissingWrongAndUsedCodes(t *testing.T) {
	srv, pool, local := setupServer(t, false)
	c := noRedirectClient(t)
	code, _ := local.MintSetupCode(context.Background())
	for name, body := range map[string]map[string]string{
		"missing": {"provider_subject": "owner", "password": "a-good-long-password"},
		"wrong":   {"code": "not-the-code", "provider_subject": "owner", "password": "a-good-long-password"},
	} {
		resp := postSetup(t, c, srv.URL, "", body)
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get("X-Blerg-Error") != "code" {
			t.Errorf("%s code: %d %q, want 403 code", name, resp.StatusCode, resp.Header.Get("X-Blerg-Error"))
		}
	}
	if n := countRows(t, pool, "accounts"); n != 0 {
		t.Fatalf("refused codes created %d accounts", n)
	}
	resp := postSetup(t, c, srv.URL, "", map[string]string{"code": code, "provider_subject": "Bad Name", "password": "a-good-long-password"})
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("X-Blerg-Error") != "username" {
		t.Fatalf("bad username: %d %q", resp.StatusCode, resp.Header.Get("X-Blerg-Error"))
	}
	resp = postSetup(t, c, srv.URL, "", map[string]string{"code": code, "provider_subject": "owner", "password": "short"})
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("X-Blerg-Error") != "password" {
		t.Fatalf("short password: %d %q", resp.StatusCode, resp.Header.Get("X-Blerg-Error"))
	}
	if resp := postSetup(t, c, srv.URL, "", map[string]string{"code": code, "provider_subject": "owner", "password": "a-good-long-password"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("valid code: %d", resp.StatusCode)
	}
	// Used: setup is now closed, for the same code and for any other.
	resp = postSetup(t, noRedirectClient(t), srv.URL, "", map[string]string{"code": code, "provider_subject": "second", "password": "a-good-long-password"})
	if resp.StatusCode != http.StatusConflict || resp.Header.Get("X-Blerg-Error") != "complete" {
		t.Fatalf("used code: %d %q, want 409 complete", resp.StatusCode, resp.Header.Get("X-Blerg-Error"))
	}
	if n := countRows(t, pool, "accounts"); n != 1 {
		t.Fatalf("%d accounts, want 1", n)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The desktop flag lets a loopback-addressed browser set up without a code — and only that.
func TestSetupWithoutCodeOnlyWhenLocalAllowed(t *testing.T) {
	srv, pool, _ := setupServer(t, false)
	body := map[string]string{"provider_subject": "owner", "password": "a-good-long-password"}
	if resp := postSetup(t, noRedirectClient(t), srv.URL, "", body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no flag, loopback host: %d, want 403", resp.StatusCode)
	}
	srvLocal, poolLocal, _ := setupServer(t, true)
	if resp := postSetup(t, noRedirectClient(t), srvLocal.URL, "blerg.example.com", body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("flag, non-loopback host (DNS rebinding shape): %d, want 403", resp.StatusCode)
	}
	if resp := postSetup(t, noRedirectClient(t), srvLocal.URL, "", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("flag, loopback host: %d, want 200", resp.StatusCode)
	}
	if countRows(t, pool, "accounts") != 0 || countRows(t, poolLocal, "accounts") != 1 {
		t.Fatal("accounts created on the wrong server")
	}
}

func TestSetupClosedAfterFirstAccount(t *testing.T) {
	srv, pool, local := setupServer(t, true)
	code, _ := local.MintSetupCode(context.Background())
	insertLocalAccount(t, pool, "someone", "a-good-long-password")
	c := noRedirectClient(t)
	resp := get(t, c, srv.URL+"/setup", "")
	assertNoFraming(t, "GET /setup when set up", resp)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/login" {
		t.Fatalf("GET /setup when set up = %d -> %q, want /login", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, body := range []map[string]string{
		{"code": code, "provider_subject": "owner", "password": "a-good-long-password"},
		{"provider_subject": "owner", "password": "a-good-long-password"},
	} {
		if resp := postSetup(t, c, srv.URL, "", body); resp.StatusCode != http.StatusConflict {
			t.Fatalf("POST /auth/setup when set up = %d, want 409", resp.StatusCode)
		}
	}
	if n := countRows(t, pool, "accounts"); n != 1 {
		t.Fatalf("%d accounts, want 1", n)
	}
	// /login is just the login page again.
	if resp := get(t, c, srv.URL+"/login", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login when set up = %d", resp.StatusCode)
	}
}

func TestLoginPageDuringSetup(t *testing.T) {
	srvLocal, _, _ := setupServer(t, true)
	c := noRedirectClient(t)
	resp := get(t, c, srvLocal.URL+"/login", "")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/setup" {
		t.Fatalf("desktop /login in setup mode = %d -> %q, want /setup", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Needs a code: the login page itself, which says to open the setup link.
	srv, _, _ := setupServer(t, false)
	resp = get(t, c, srv.URL+"/login", "")
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(b) != "the web app" {
		t.Fatalf("cluster /login in setup mode = %d %q, want the app", resp.StatusCode, b)
	}
}

func TestSetupRejectsCrossSiteAndIsRateLimited(t *testing.T) {
	deps, _ := newTestDeps(t, nil, nil)
	deps.CookieSecure = false
	limiter := api.NewLoginLimiter(2, time.Minute)
	deps.LoginLimiter = limiter
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	local := deps.AuthProvider.(*authprovider.Local)
	code, _ := local.MintSetupCode(context.Background())

	b, _ := json.Marshal(map[string]string{"code": code, "provider_subject": "owner", "password": "a-good-long-password"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/setup", bytes.NewReader(b))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site setup = %d, want 403", resp.StatusCode)
	}

	c := noRedirectClient(t)
	for i := 0; i < 2; i++ {
		postSetup(t, c, srv.URL, "", map[string]string{"code": "wrong", "provider_subject": "owner", "password": "a-good-long-password"})
	}
	resp = postSetup(t, c, srv.URL, "", map[string]string{"code": code, "provider_subject": "owner", "password": "a-good-long-password"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after 2 wrong codes = %d, want 429", resp.StatusCode)
	}
	// An oversized body is refused before anything is parsed.
	big := `{"code":"` + strings.Repeat("x", 20<<10) + `"}`
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/auth/setup", strings.NewReader(big))
	limiter.Reset("127.0.0.1", "setup:127.0.0.1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized body = %d, want 400", resp.StatusCode)
	}
}

func assertNoFraming(t *testing.T, what string, resp *http.Response) {
	t.Helper()
	if resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("Content-Security-Policy") != "frame-ancestors 'none'" {
		t.Errorf("%s: X-Frame-Options %q, CSP %q; want DENY and frame-ancestors 'none'", what,
			resp.Header.Get("X-Frame-Options"), resp.Header.Get("Content-Security-Policy"))
	}
}

func TestSetupResponsesCannotBeFramed(t *testing.T) {
	srv, _, _ := setupServer(t, false)
	resp := postSetup(t, noRedirectClient(t), srv.URL, "", map[string]string{"code": "x", "provider_subject": "owner", "password": "a-good-long-password"})
	assertNoFraming(t, "POST /auth/setup", resp)
}

// The code-less desktop path trusts only a direct connection from this machine: a loopback peer
// or the container network's gateway (where the host's own loopback arrives through a published
// port), and never a request that came through a proxy.
func TestSetupLocalTrustNeedsADirectLocalPeer(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.CookieSecure = false
	deps.SetupAllowLocal = true
	deps.SetupLocalGateway = net.ParseIP("198.51.100.1")
	router := api.NewRouter(deps)
	try := func(remote string, header http.Header) int {
		b, _ := json.Marshal(map[string]string{"provider_subject": "owner", "password": "a-good-long-password"})
		req := httptest.NewRequest(http.MethodPost, "http://localhost:8081/auth/setup", bytes.NewReader(b))
		req.RemoteAddr = remote
		for k, v := range header {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-Ip"} {
		if code := try("127.0.0.1:5000", http.Header{h: {"for=192.0.2.7"}}); code != http.StatusForbidden {
			t.Errorf("loopback peer with %s: %d, want 403 (a setup code is required)", h, code)
		}
	}
	if code := try("192.0.2.7:5000", nil); code != http.StatusForbidden {
		t.Errorf("LAN peer with Host: localhost: %d, want 403", code)
	}
	if code := try("198.51.100.2:5000", nil); code != http.StatusForbidden {
		t.Errorf("another container on the network: %d, want 403", code)
	}
	if n := countRows(t, pool, "accounts"); n != 0 {
		t.Fatalf("refused peers created %d accounts", n)
	}
	if code := try("198.51.100.1:5000", nil); code != http.StatusOK {
		t.Fatalf("the network gateway (the host's loopback through a published port): %d, want 200", code)
	}
}

// A set-up install keeps answering setup attempts cheaply, and they count against the per-IP limit.
func TestSetupAttemptsAfterSetupAreRateLimited(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.CookieSecure = false
	deps.LoginLimiter = api.NewLoginLimiter(3, time.Minute)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	insertLocalAccount(t, pool, "someone", "a-good-long-password")
	c := noRedirectClient(t)
	body := map[string]string{"code": "bogus", "provider_subject": "owner", "password": "a-good-long-password"}
	for i := 0; i < 3; i++ {
		if resp := postSetup(t, c, srv.URL, "", body); resp.StatusCode != http.StatusConflict {
			t.Fatalf("attempt %d on a set-up install: %d, want 409", i, resp.StatusCode)
		}
	}
	if resp := postSetup(t, c, srv.URL, "", body); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("4th attempt: %d, want 429", resp.StatusCode)
	}
}

// /setup/ (and below) must not serve the setup page without its headers: it redirects to /setup.
func TestSetupTrailingSlashRedirectsWithHeaders(t *testing.T) {
	srv, _, _ := setupServer(t, true)
	for _, path := range []string{"/setup/", "/setup/x"} {
		resp := get(t, noRedirectClient(t), srv.URL+path, "")
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/setup" {
			t.Fatalf("GET %s = %d -> %q, want a redirect to /setup", path, resp.StatusCode, resp.Header.Get("Location"))
		}
		assertNoFraming(t, "GET "+path, resp)
		if resp.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("GET %s: Referrer-Policy %q", path, resp.Header.Get("Referrer-Policy"))
		}
	}
}
