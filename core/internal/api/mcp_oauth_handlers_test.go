package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn/mcptest"
)

const (
	oauthPublicURL   = "https://core.example.test"
	oauthCallbackURL = oauthPublicURL + "/auth/mcp/callback"
	// One cookie per flow, named after the first 8 hex digits of SHA-256(state).
	oauthCookiePrefix = "blerg_mcp_oauth_"
)

// oauthCookieName is the name of the state cookie of the flow with this state.
func oauthCookieName(state string) string {
	sum := sha256.Sum256([]byte(state))
	return oauthCookiePrefix + hex.EncodeToString(sum[:])[:8]
}

func stateOfAuthURL(authURL string) string {
	u, _ := url.Parse(authURL)
	return u.Query().Get("state")
}

// stateCookieOf finds the flow's state cookie (any cookie with the prefix) in a response.
func stateCookieOf(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, oauthCookiePrefix) {
			return c
		}
	}
	return nil
}

type oauthAPIEnv struct {
	deps     api.Deps
	pool     *pgxpool.Pool
	srv      *httptest.Server
	as       *mcptest.Server
	acctA    string
	acctB    string
	sidA     string
	sidB     string
	humanA   string // human token of A carrying sid = sidA
	humanANo string // human token of A with no sid
	humanB   string
	agentA   string
	svc      *mcpconn.Service
}

func newOAuthAPIEnv(t *testing.T) *oauthAPIEnv {
	t.Helper()
	return newOAuthAPIEnvWithPublicURL(t, oauthPublicURL)
}

func newOAuthAPIEnvWithPublicURL(t *testing.T, publicURL string) *oauthAPIEnv {
	t.Helper()
	deps, pool := newInternalTestDeps(t)
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	deps.PublicURL = publicURL
	svc := mcpconn.NewService(db.NewPgStore(pool), backend,
		netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}})
	deps.MCPConnections = svc
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	as := mcptest.New()
	t.Cleanup(as.Close)
	idSvc := deps.Identity.(*identity.Service)
	ctx := context.Background()

	e := &oauthAPIEnv{deps: deps, pool: pool, srv: srv, as: as, svc: svc}
	e.acctA = insertCredAccount(t, pool, "oauth-a")
	e.acctB = insertCredAccount(t, pool, "oauth-b")
	e.sidA = insertLiveHumanSession(t, pool, e.acctA)
	e.sidB = insertLiveHumanSession(t, pool, e.acctB)
	if e.humanA, err = idSvc.MintHumanAccessTokenForSession(ctx, e.acctA, "blerg-core", e.sidA); err != nil {
		t.Fatal(err)
	}
	if e.humanANo, err = idSvc.MintHumanAccessToken(ctx, e.acctA, "blerg-core"); err != nil {
		t.Fatal(err)
	}
	if e.humanB, err = idSvc.MintHumanAccessTokenForSession(ctx, e.acctB, "blerg-core", e.sidB); err != nil {
		t.Fatal(err)
	}
	if e.agentA, err = idSvc.MintAgentToken(ctx, identity.AgentTokenInput{
		Sub: "11111111-1111-1111-1111-111111111111", Aud: "blerg-core",
		OnBehalfOf: e.acctA, Lineage: e.acctA, Caps: []string{"card.read"},
	}); err != nil {
		t.Fatal(err)
	}
	return e
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// req sends one request (never following redirects) and returns the response and body.
func (e *oauthAPIEnv) req(t *testing.T, method, path, bearer string, body any, hdr map[string]string, cookies ...*http.Cookie) (*http.Response, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		if k == "Host" {
			r.Host = v
			continue
		}
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	resp, err := noRedirect.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

var sameOrigin = map[string]string{"Origin": oauthPublicURL}

func (e *oauthAPIEnv) start(t *testing.T, bearer, name string) (*http.Response, string) {
	t.Helper()
	return e.req(t, http.MethodPost, "/api/mcp/connections/oauth/start", bearer,
		map[string]string{"name": name, "url": e.as.MCPURL()}, sameOrigin)
}

// begin runs start and returns the authorization URL and the state cookie the browser would keep.
func (e *oauthAPIEnv) begin(t *testing.T, bearer, name string) (authURL string, cookie *http.Cookie) {
	t.Helper()
	resp, body := e.start(t, bearer, name)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start = %d %s", resp.StatusCode, body)
	}
	var out struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.AuthorizationURL == "" {
		t.Fatalf("start body = %s", body)
	}
	if cookie = stateCookieOf(resp); cookie == nil {
		t.Fatal("no state cookie set")
	}
	return out.AuthorizationURL, cookie
}

func (e *oauthAPIEnv) callback(t *testing.T, q url.Values, cookies ...*http.Cookie) (*http.Response, string) {
	t.Helper()
	return e.req(t, http.MethodGet, "/auth/mcp/callback?"+q.Encode(), "", nil, nil, cookies...)
}

func locationQuery(t *testing.T, resp *http.Response) (string, url.Values) {
	t.Helper()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc.Scheme + "://" + loc.Host + loc.Path, loc.Query()
}

func connCount(t *testing.T, e *oauthAPIEnv, acct string) int {
	t.Helper()
	l, err := e.svc.List(context.Background(), acct)
	if err != nil {
		t.Fatal(err)
	}
	return len(l)
}

func TestMCPOAuthStartShapeAndCookie(t *testing.T) {
	e := newOAuthAPIEnv(t)
	resp, body := e.start(t, e.humanA, "notes")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start = %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out) != 2 || !strings.HasPrefix(out["authorization_url"], e.as.Issuer()+"/authorize?") {
		t.Fatalf("body = %s", body)
	}
	// The person is shown which authorization server the flow goes to.
	if asu, _ := url.Parse(e.as.Issuer()); out["authorization_server"] != asu.Host {
		t.Fatalf("authorization_server = %q, want %q", out["authorization_server"], asu.Host)
	}
	u, _ := url.Parse(out["authorization_url"])
	state := u.Query().Get("state")
	if u.Query().Get("redirect_uri") != oauthCallbackURL || u.Query().Get("resource") != e.as.MCPURL() {
		t.Fatalf("authorization url = %s", out["authorization_url"])
	}
	// The registered redirect URI is exactly <public url>/auth/mcp/callback.
	if uris, _ := e.as.Registered[0]["redirect_uris"].([]any); len(uris) != 1 || uris[0] != oauthCallbackURL {
		t.Fatalf("registered redirect uris = %v", e.as.Registered[0]["redirect_uris"])
	}
	c := stateCookieOf(resp)
	sum := sha256.Sum256([]byte(state))
	if c == nil || c.Value != hex.EncodeToString(sum[:]) {
		t.Fatalf("state cookie = %+v, want SHA-256(state)", c)
	}
	if c.Name != oauthCookieName(state) {
		t.Fatalf("cookie name = %q, want %q (per flow)", c.Name, oauthCookieName(state))
	}
	if c.SameSite != http.SameSiteLaxMode || !c.HttpOnly || c.Path != "/auth" || c.MaxAge != 600 {
		t.Fatalf("cookie attributes = %+v, want Lax, HttpOnly, Path=/auth, 10 minutes", c)
	}
	if strings.Contains(c.Value, state) {
		t.Fatal("the cookie holds the state itself")
	}
}

func TestMCPOAuthFullFlowOverHTTP(t *testing.T) {
	e := newOAuthAPIEnv(t)
	authURL, cookie := e.begin(t, e.humanA, "notes")
	q, err := e.as.Approve(authURL)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := e.callback(t, q, cookie)
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		t.Fatalf("callback = %d %s", resp.StatusCode, body)
	}
	base, lq := locationQuery(t, resp)
	if base != oauthPublicURL+"/settings" || lq.Get("mcp_oauth") != "success" {
		t.Fatalf("redirected to %s?%v", base, lq)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("callback Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == oauthCookieName(stateOfAuthURL(authURL)) && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the state cookie was not cleared")
	}

	// Listed, status ok, never with a token.
	code, list := func() (int, string) {
		r, b := e.req(t, http.MethodGet, "/api/mcp/connections", e.humanA, nil, nil)
		return r.StatusCode, b
	}()
	if code != 200 || !strings.Contains(list, `"auth_kind":"oauth"`) || !strings.Contains(list, `"status":"ok"`) || !strings.Contains(list, e.as.Issuer()) {
		t.Fatalf("list = %d %s", code, list)
	}
	for _, leak := range []string{"at-", "rt-", "client-"} {
		if strings.Contains(list, leak+"1") || strings.Contains(list, leak+"2") || strings.Contains(list, leak+"3") {
			t.Fatalf("list leaks %q: %s", leak, list)
		}
	}
	var conns struct {
		Connections []struct {
			ID string `json:"id"`
		} `json:"connections"`
	}
	_ = json.Unmarshal([]byte(list), &conns)

	// The internal route hands the runner a Bearer value with an expiry.
	r, b := e.internalReq(t, map[string]string{"account_id": e.acctA, "connection_id": conns.Connections[0].ID, "session_id": e.sidA})
	if r.StatusCode != 200 {
		t.Fatalf("internal token = %d %s", r.StatusCode, b)
	}
	var tok struct {
		URL        string `json:"url"`
		HeaderName string `json:"header_name"`
		Value      string `json:"value"`
		ExpiresAt  string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(b), &tok); err != nil {
		t.Fatal(err)
	}
	if tok.URL != e.as.MCPURL() || tok.HeaderName != "Authorization" || !strings.HasPrefix(tok.Value, "Bearer at-") || tok.ExpiresAt == "" {
		t.Fatalf("token = %+v", tok)
	}
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("internal Cache-Control = %q", r.Header.Get("Cache-Control"))
	}
	if _, err := time.Parse(time.RFC3339, tok.ExpiresAt); err != nil {
		t.Fatalf("expires_at %q: %v", tok.ExpiresAt, err)
	}
	// The fake MCP server accepts exactly that credential.
	mreq, _ := http.NewRequest(http.MethodPost, tok.URL, strings.NewReader(`{}`))
	mreq.Header.Set("Authorization", tok.Value)
	mresp, err := http.DefaultClient.Do(mreq)
	if err != nil {
		t.Fatal(err)
	}
	_ = mresp.Body.Close()
	if mresp.StatusCode != 200 {
		t.Fatalf("the issued bearer value was rejected by the MCP server: %d", mresp.StatusCode)
	}
}

func (e *oauthAPIEnv) internalReq(t *testing.T, body map[string]string) (*http.Response, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/internal/mcp/connections/token", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+testInternalKey)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestMCPOAuthCallbackStateAndCookieRules(t *testing.T) {
	e := newOAuthAPIEnv(t)
	authURL, cookie := e.begin(t, e.humanA, "notes")
	q, _ := e.as.Approve(authURL)

	t.Run("no cookie", func(t *testing.T) {
		resp, _ := e.callback(t, q)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("= %d, want 400", resp.StatusCode)
		}
		if len(resp.Cookies()) != 0 {
			t.Fatalf("a refused callback set cookies: %v", resp.Cookies())
		}
	})
	t.Run("another flow's cookie does not count", func(t *testing.T) {
		other := &http.Cookie{Name: oauthCookiePrefix + "deadbeef", Value: cookie.Value}
		if resp, _ := e.callback(t, q, other); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("= %d, want 400", resp.StatusCode)
		}
	})
	t.Run("mismatched cookie", func(t *testing.T) {
		bad := &http.Cookie{Name: oauthCookieName(q.Get("state")), Value: strings.Repeat("0", 64)}
		resp, _ := e.callback(t, q, bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("= %d, want 400", resp.StatusCode)
		}
		// MINOR 35 (a): a refused (lured) request must not clear the victim's cookie.
		if len(resp.Cookies()) != 0 {
			t.Fatalf("a refused callback set cookies: %v", resp.Cookies())
		}
	})
	t.Run("the login flow's own state cookie does not count", func(t *testing.T) {
		other := &http.Cookie{Name: "blerg_oauth_state", Value: cookie.Value}
		if resp, _ := e.callback(t, q, other); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("= %d, want 400", resp.StatusCode)
		}
	})
	t.Run("no state", func(t *testing.T) {
		if resp, _ := e.callback(t, url.Values{"code": {q.Get("code")}}, cookie); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("= %d, want 400", resp.StatusCode)
		}
	})
	if connCount(t, e, e.acctA) != 0 {
		t.Fatal("a refused callback created a connection")
	}
	// None of those consumed the state: an attacker replaying the link without the victim's cookie
	// cannot burn the victim's flow. The legitimate browser still completes it.
	resp, _ := e.callback(t, q, cookie)
	if _, lq := locationQuery(t, resp); lq.Get("mcp_oauth") != "success" {
		t.Fatalf("legitimate callback after refused attempts: %s", resp.Header.Get("Location"))
	}
	// Reuse: the state is gone.
	resp, _ = e.callback(t, q, cookie)
	if _, lq := locationQuery(t, resp); lq.Get("mcp_oauth") != "error" || lq.Get("reason") != "state" {
		t.Fatalf("replay redirected to %s", resp.Header.Get("Location"))
	}
	if connCount(t, e, e.acctA) != 1 {
		t.Fatal("replay created a second connection")
	}
}

func TestMCPOAuthCallbackRefusals(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		mod    func(e *oauthAPIEnv, q url.Values)
	}{
		{"dead login session", "session", func(e *oauthAPIEnv, _ url.Values) {
			if _, err := e.pool.Exec(context.Background(), `UPDATE human_sessions SET revoked_at = now() WHERE id = $1`, e.sidA); err != nil {
				panic(err)
			}
		}},
		{"mismatched iss", "issuer", func(_ *oauthAPIEnv, q url.Values) { q.Set("iss", "https://evil.example/as") }},
		{"provider error is not reflected", "denied", func(_ *oauthAPIEnv, q url.Values) {
			q.Del("code")
			q.Set("error", "access_denied")
			q.Set("error_description", "<script>alert(1)</script>")
		}},
		{"expired state", "state", func(e *oauthAPIEnv, _ url.Values) {
			if _, err := e.pool.Exec(context.Background(), `UPDATE mcp_oauth_state SET expires_at = now() - interval '1 second'`); err != nil {
				panic(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newOAuthAPIEnv(t)
			authURL, cookie := e.begin(t, e.humanA, "notes")
			q, _ := e.as.Approve(authURL)
			tc.mod(e, q)
			resp, _ := e.callback(t, q, cookie)
			base, lq := locationQuery(t, resp)
			if base != oauthPublicURL+"/settings" || lq.Get("mcp_oauth") != "error" || lq.Get("reason") != tc.reason {
				t.Fatalf("redirected to %s", resp.Header.Get("Location"))
			}
			if strings.Contains(resp.Header.Get("Location"), "script") || strings.Contains(resp.Header.Get("Location"), "access_denied") {
				t.Fatalf("provider text reflected: %s", resp.Header.Get("Location"))
			}
			if connCount(t, e, e.acctA) != 0 {
				t.Fatal("a refused callback created a connection")
			}
		})
	}
}

func TestMCPOAuthCallbackMissingIssWhenAdvertised(t *testing.T) {
	e := newOAuthAPIEnv(t)
	e.as.AdvertiseIss = true
	authURL, cookie := e.begin(t, e.humanA, "notes")
	q, _ := e.as.Approve(authURL)
	q.Del("iss")
	resp, _ := e.callback(t, q, cookie)
	if _, lq := locationQuery(t, resp); lq.Get("reason") != "issuer" {
		t.Fatalf("redirected to %s", resp.Header.Get("Location"))
	}
}

func TestMCPOAuthStartRequiresLiveSession(t *testing.T) {
	e := newOAuthAPIEnv(t)
	if resp, body := e.start(t, e.humanANo, "notes"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("token without sid = %d %s, want 400", resp.StatusCode, body)
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE human_sessions SET revoked_at = now() WHERE id = $1`, e.sidA); err != nil {
		t.Fatal(err)
	}
	if resp, body := e.start(t, e.humanA, "notes"); resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("dead session = %d %s, want 401/403", resp.StatusCode, body)
	}
}

func TestMCPOAuthStartErrors(t *testing.T) {
	e := newOAuthAPIEnv(t)
	post := func(body any) (int, string) {
		r, b := e.req(t, http.MethodPost, "/api/mcp/connections/oauth/start", e.humanA, body, sameOrigin)
		return r.StatusCode, b
	}
	if code, _ := post(map[string]string{"name": "Bad Name", "url": e.as.MCPURL()}); code != 400 {
		t.Errorf("bad name = %d", code)
	}
	if code, _ := post(map[string]string{"name": "board", "url": e.as.MCPURL()}); code != 400 {
		t.Errorf("reserved = %d", code)
	}
	if code, _ := post(map[string]any{"name": "x", "url": e.as.MCPURL(), "extra": 1}); code != 400 {
		t.Errorf("unknown field = %d", code)
	}
	if code, body := post(map[string]string{"name": "x", "url": "https://10.0.0.5/mcp"}); code != 400 || !strings.Contains(body, "not permitted") { // scrub:allow (a range the guard must refuse)
		t.Errorf("private address = %d %s", code, body)
	}
	if code, _ := post(map[string]string{"name": "x", "url": e.as.MCPURL(), "scopes": strings.Repeat("a ", 600)}); code != 400 {
		t.Errorf("long scopes = %d", code)
	}
	if code, _ := post(map[string]string{"name": "x", "url": e.as.MCPURL(), "pad": strings.Repeat("a", 20<<10)}); code != 413 {
		t.Errorf("oversized = %d", code)
	}
	// scopes as an array is accepted too.
	if code, body := post(map[string]any{"name": "arr", "url": e.as.MCPURL(), "scopes": []string{"a", "b"}}); code != 200 {
		t.Errorf("scopes array = %d %s", code, body)
	}
	e.as.NoRegistration = true
	if code, body := post(map[string]string{"name": "noreg", "url": e.as.MCPURL()}); code != 400 || !strings.Contains(body, "client_id") {
		t.Errorf("no registration = %d %s", code, body)
	}
	if code, body := post(map[string]string{"name": "preset", "url": e.as.MCPURL(), "client_id": "x"}); code != 200 {
		// the fake has no such client; start does not need it, only the authorize step would
		t.Errorf("client_id supplied = %d %s", code, body)
	}
}

func TestMCPOAuthStartRateLimited(t *testing.T) {
	e := newOAuthAPIEnv(t)
	e.svc.SetOAuthStartLimit(2, time.Minute)
	for i := range 2 {
		if resp, body := e.start(t, e.humanA, "notes"); resp.StatusCode != 200 {
			t.Fatalf("start %d = %d %s", i, resp.StatusCode, body)
		}
	}
	resp, _ := e.start(t, e.humanA, "notes")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third start = %d, want 429", resp.StatusCode)
	}
	if resp, _ := e.start(t, e.humanB, "notes"); resp.StatusCode != 200 {
		t.Fatalf("another account was limited: %d", resp.StatusCode)
	}
}

// connect completes a whole flow over HTTP for humanA and returns the connection id.
func (e *oauthAPIEnv) connect(t *testing.T, name string) string {
	t.Helper()
	authURL, cookie := e.begin(t, e.humanA, name)
	q, err := e.as.Approve(authURL)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := e.callback(t, q, cookie)
	if _, lq := locationQuery(t, resp); lq.Get("mcp_oauth") != "success" {
		t.Fatalf("connect: %s", resp.Header.Get("Location"))
	}
	l, _ := e.svc.List(context.Background(), e.acctA)
	for _, c := range l {
		if c.Name == name {
			return c.ID
		}
	}
	t.Fatal("connection missing")
	return ""
}

func TestMCPOAuthRefreshFailureIs404OnInternalRoute(t *testing.T) {
	e := newOAuthAPIEnv(t)
	e.as.SetAccessTTL(30)
	id := e.connect(t, "notes")
	e.as.Lock()
	e.as.RefreshStatus = 400
	e.as.Unlock()
	req := map[string]string{"account_id": e.acctA, "connection_id": id, "session_id": e.sidA}
	resp, body := e.internalReq(t, req)
	if resp.StatusCode != 404 || strings.Contains(body, "at-") {
		t.Fatalf("= %d %s, want the uniform 404", resp.StatusCode, body)
	}
	// Same body as any other 404 of this route.
	_, missing := e.internalReq(t, map[string]string{"account_id": e.acctA, "connection_id": noSuchSession, "session_id": e.sidA})
	if body != missing {
		t.Fatalf("needs_auth 404 body %q differs from the unknown-connection 404 %q", body, missing)
	}
	r, list := e.req(t, http.MethodGet, "/api/mcp/connections", e.humanA, nil, nil)
	if r.StatusCode != 200 || !strings.Contains(list, `"status":"needs_auth"`) {
		t.Fatalf("list = %s", list)
	}
	// Still 404 afterwards, without asking the provider again.
	before := len(e.as.TokenForms)
	if resp, _ := e.internalReq(t, req); resp.StatusCode != 404 {
		t.Fatalf("second = %d", resp.StatusCode)
	}
	if len(e.as.TokenForms) != before {
		t.Fatal("a needs_auth connection asked the provider again")
	}
}

func TestMCPOAuthTransientRefreshFailureIs503(t *testing.T) {
	e := newOAuthAPIEnv(t)
	e.as.SetAccessTTL(1)
	id := e.connect(t, "notes")
	time.Sleep(1100 * time.Millisecond)
	e.as.Lock()
	e.as.RefreshStatus = 503
	e.as.Unlock()
	resp, body := e.internalReq(t, map[string]string{"account_id": e.acctA, "connection_id": id, "session_id": e.sidA})
	if resp.StatusCode != http.StatusServiceUnavailable || strings.Contains(body, "at-") {
		t.Fatalf("= %d %s, want 503", resp.StatusCode, body)
	}
}

func TestMCPOAuthDeleteAndReconnectOverHTTP(t *testing.T) {
	e := newOAuthAPIEnv(t)
	id := e.connect(t, "notes")

	// Reconnect: another account 404s, a non-oauth connection 400s, the owner gets a new flow.
	path := "/api/mcp/connections/" + id + "/reconnect"
	if resp, _ := e.req(t, http.MethodPost, path, e.humanB, nil, sameOrigin); resp.StatusCode != 404 {
		t.Errorf("other account's reconnect = %d, want 404", resp.StatusCode)
	}
	static, err := e.svc.Create(context.Background(), e.acctA, mcpconn.CreateInput{Name: "plain", URL: "https://p.example.com", AuthKind: "static", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := e.req(t, http.MethodPost, "/api/mcp/connections/"+static.ID+"/reconnect", e.humanA, nil, sameOrigin); resp.StatusCode != 400 {
		t.Errorf("static reconnect = %d, want 400", resp.StatusCode)
	}
	old, _ := e.svc.FetchSecret(context.Background(), e.acctA, id, mcpconn.Proof{SessionID: e.sidA})
	resp, body := e.req(t, http.MethodPost, path, e.humanA, nil, sameOrigin)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("reconnect = %d %s", resp.StatusCode, body)
	}
	var out struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	cookie := stateCookieOf(resp)
	q, err := e.as.Approve(out.AuthorizationURL)
	if err != nil || cookie == nil {
		t.Fatalf("approve: %v cookie=%v", err, cookie)
	}
	cb, _ := e.callback(t, q, cookie)
	if _, lq := locationQuery(t, cb); lq.Get("mcp_oauth") != "success" {
		t.Fatalf("reconnect callback: %s", cb.Header.Get("Location"))
	}
	fresh, err := e.svc.FetchSecret(context.Background(), e.acctA, id, mcpconn.Proof{SessionID: e.sidA})
	if err != nil || fresh.Value == old.Value {
		t.Fatalf("tokens not replaced: %v", err)
	}

	// The reconnect revoked the OLD grant upstream (two calls: refresh token, access token).
	_, _, afterReconnect, _ := e.as.Stats()
	if afterReconnect != 2 {
		t.Fatalf("reconnect revoked %d tokens upstream, want the old pair (2)", afterReconnect)
	}
	// Delete revokes upstream.
	if resp, _ := e.req(t, http.MethodDelete, "/api/mcp/connections/"+id, e.humanB, nil, sameOrigin); resp.StatusCode != 404 {
		t.Errorf("other account's delete = %d", resp.StatusCode)
	}
	if _, _, revokes, _ := e.as.Stats(); revokes != afterReconnect {
		t.Fatal("another account's delete revoked upstream")
	}
	if resp, _ := e.req(t, http.MethodDelete, "/api/mcp/connections/"+id, e.humanA, nil, sameOrigin); resp.StatusCode != 204 {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if _, _, revokes, _ := e.as.Stats(); revokes == afterReconnect {
		t.Fatal("delete did not revoke upstream")
	}
}

// The route-group table for the OAuth additions.
func TestMCPOAuthRoutesAuthMatrix(t *testing.T) {
	e := newOAuthAPIEnv(t)
	id := e.connect(t, "notes")
	routes := []struct {
		name, method, path string
		body               any
	}{
		{"start", http.MethodPost, "/api/mcp/connections/oauth/start", map[string]string{"name": "x", "url": e.as.MCPURL()}},
		{"reconnect", http.MethodPost, "/api/mcp/connections/" + id + "/reconnect", nil},
		{"delete", http.MethodDelete, "/api/mcp/connections/" + id, nil},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			if r, _ := e.req(t, rt.method, rt.path, "", rt.body, sameOrigin); r.StatusCode != http.StatusUnauthorized {
				t.Errorf("no token = %d, want 401", r.StatusCode)
			}
			if r, _ := e.req(t, rt.method, rt.path, "garbage", rt.body, sameOrigin); r.StatusCode != http.StatusUnauthorized {
				t.Errorf("garbage token = %d, want 401", r.StatusCode)
			}
			if r, b := e.req(t, rt.method, rt.path, e.agentA, rt.body, sameOrigin); r.StatusCode != http.StatusForbidden {
				t.Errorf("agent token = %d, want 403 (%s)", r.StatusCode, b)
			}
			for _, hdr := range []map[string]string{{"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}, {"Sec-Fetch-Site": "same-site"}} {
				if r, _ := e.req(t, rt.method, rt.path, e.humanA, rt.body, hdr); r.StatusCode != http.StatusForbidden {
					t.Errorf("%v = %d, want 403", hdr, r.StatusCode)
				}
			}
		})
	}
	if _, err := e.svc.Get(context.Background(), e.acctA, id); err != nil {
		t.Fatalf("rejected calls touched the connection: %v", err)
	}
	if _, _, revokes, regs := e.as.Stats(); revokes != 0 || regs != 1 {
		t.Fatalf("rejected calls reached the provider: revokes=%d registrations=%d", revokes, regs)
	}
	// The callback is unauthenticated by design (no bearer, so never 401/403) but useless without
	// state and cookie.
	for _, q := range []url.Values{{}, {"state": {"x"}, "code": {"y"}}} {
		if r, _ := e.callback(t, q); r.StatusCode != http.StatusBadRequest {
			t.Errorf("callback %v = %d, want 400", q, r.StatusCode)
		}
	}
	// And it accepts no other method.
	if r, _ := e.req(t, http.MethodPost, "/auth/mcp/callback", "", nil, nil); r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST callback = %d, want 405", r.StatusCode)
	}
}

func TestMCPOAuthSecretsNeverInResponsesOrLogs(t *testing.T) {
	e := newOAuthAPIEnv(t)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	e.as.Confidential = true
	e.as.SetAccessTTL(30) // inside the refresh window, so the internal call below refreshes (and is rejected)

	var seen []string
	authURL, cookie := e.begin(t, e.humanA, "notes")
	state := func() string { u, _ := url.Parse(authURL); return u.Query().Get("state") }()
	q, _ := e.as.Approve(authURL)
	resp, body := e.callback(t, q, cookie)
	seen = append(seen, resp.Header.Get("Location"), body)
	_, list := e.req(t, http.MethodGet, "/api/mcp/connections", e.humanA, nil, nil)
	seen = append(seen, list)
	l, _ := e.svc.List(context.Background(), e.acctA)
	e.as.Lock()
	e.as.RefreshStatus = 400
	e.as.Unlock()
	_, b := e.internalReq(t, map[string]string{"account_id": e.acctA, "connection_id": l[0].ID, "session_id": e.sidA})
	seen = append(seen, b)
	_, b = e.req(t, http.MethodDelete, "/api/mcp/connections/"+l[0].ID, e.humanA, nil, sameOrigin)
	seen = append(seen, b)
	for _, s := range seen {
		for _, secret := range []string{"at-", "rt-", "client-secret", q.Get("code")} {
			if strings.Contains(s, secret) {
				t.Errorf("response leaks %q: %s", secret, s)
			}
		}
	}
	for _, secret := range []string{"at-", "rt-", "client-secret", q.Get("code"), state, "code_verifier"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log leaks %q: %s", secret, logs.String())
		}
	}
}

// MINOR 35 (b): two flows in flight at once (two tabs) each keep their own cookie, and finishing
// one does not touch the other's.
func TestMCPOAuthTwoFlowsInFlightUseSeparateCookies(t *testing.T) {
	e := newOAuthAPIEnv(t)
	authA, cookieA := e.begin(t, e.humanA, "notes")
	authB, cookieB := e.begin(t, e.humanA, "other")
	if cookieA.Name == cookieB.Name {
		t.Fatalf("both flows use the cookie %q: the second tab overwrites the first", cookieA.Name)
	}
	qA, _ := e.as.Approve(authA)
	qB, _ := e.as.Approve(authB)
	respA, _ := e.callback(t, qA, cookieA) // B's cookie is not even sent
	if _, lq := locationQuery(t, respA); lq.Get("mcp_oauth") != "success" {
		t.Fatalf("flow A: %s", respA.Header.Get("Location"))
	}
	for _, c := range respA.Cookies() {
		if c.Name == cookieB.Name {
			t.Fatalf("finishing flow A touched flow B's cookie %q", c.Name)
		}
	}
	respB, _ := e.callback(t, qB, cookieB)
	if _, lq := locationQuery(t, respB); lq.Get("mcp_oauth") != "success" {
		t.Fatalf("flow B: %s", respB.Header.Get("Location"))
	}
	if n := connCount(t, e, e.acctA); n != 2 {
		t.Fatalf("%d connections, want 2", n)
	}
}

// MINOR 35 (f): without BLERG_CORE_PUBLIC_URL the redirect URI is never derived from the Host or
// X-Forwarded-Proto of a non-loopback request; a loopback (desktop) request still works.
func TestMCPOAuthWithoutPublicURLRefusesNonLoopbackHosts(t *testing.T) {
	e := newOAuthAPIEnvWithPublicURL(t, "")
	start := func(host string, hdr map[string]string) (*http.Response, string) {
		h := map[string]string{"Host": host}
		for k, v := range hdr {
			h[k] = v
		}
		return e.req(t, http.MethodPost, "/api/mcp/connections/oauth/start", e.humanA,
			map[string]string{"name": "notes", "url": e.as.MCPURL()}, h)
	}
	for _, host := range []string{"core.example.org", "core.example.org:8443", "evil.example"} {
		resp, body := start(host, map[string]string{"X-Forwarded-Proto": "https", "Origin": "https://" + host})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "BLERG_CORE_PUBLIC_URL") {
			t.Errorf("start on host %q = %d %s, want 400 naming BLERG_CORE_PUBLIC_URL", host, resp.StatusCode, body)
		}
	}
	if len(e.as.Registered) != 0 {
		t.Fatal("a refused start reached the provider")
	}
	// The callback is refused the same way.
	if resp, _ := e.req(t, http.MethodGet, "/auth/mcp/callback?state=x&code=y", "", nil, map[string]string{"Host": "core.example.org"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback on a non-loopback host = %d, want 400", resp.StatusCode)
	}
	// Loopback hosts (a desktop install) keep working and use the request's own origin.
	for _, host := range []string{"localhost:7701", "127.0.0.1:7701", "[::1]:7701"} {
		resp, body := start(host, map[string]string{"Origin": "http://" + host})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("start on loopback host %q = %d %s, want 200", host, resp.StatusCode, body)
		}
	}
	last := e.as.Registered[len(e.as.Registered)-1]
	if uris, _ := last["redirect_uris"].([]any); len(uris) != 1 || uris[0] != "http://[::1]:7701/auth/mcp/callback" {
		t.Errorf("registered redirect uris = %v", last["redirect_uris"])
	}
}
