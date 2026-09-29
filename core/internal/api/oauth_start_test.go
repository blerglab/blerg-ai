package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/authprovider"
)

// stubProvider stands in for github/oidc: it records the state it was handed and accepts
// exactly one code. Its account must already exist (real providers upsert it in Callback).
type stubProvider struct {
	lastState string
	accountID string
}

func (s *stubProvider) ID() string { return "github" }
func (s *stubProvider) LoginURL(state string) string {
	s.lastState = state
	return "https://idp.example/authorize?state=" + url.QueryEscape(state)
}
func (s *stubProvider) Callback(_ context.Context, r *http.Request) (authprovider.Account, error) {
	if r.URL.Query().Get("code") != "ok" {
		return authprovider.Account{}, errors.New("bad code")
	}
	return authprovider.Account{ID: s.accountID, Provider: "github", ProviderSubject: "42"}, nil
}

func insertProviderAccount(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO accounts (provider, provider_subject, role) VALUES ('github','42','member') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func noRedirects(c *http.Client) *http.Client {
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestOAuthStartAndCallbackBindState(t *testing.T) {
	deps, pool := newTestDeps(t, []string{"https://core.example.com"}, nil)
	stub := &stubProvider{accountID: insertProviderAccount(t, pool)}
	deps.AuthProvider = stub
	deps.PublicURL = "https://core.example.com"
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()
	client := noRedirects(srv.Client())

	resp, err := client.Get(srv.URL + "/auth/start")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("/auth/start = %d, want 302", resp.StatusCode)
	}
	if stub.lastState == "" || !containsQuery(resp.Header.Get("Location"), "state", stub.lastState) {
		t.Fatalf("Location %q does not carry the generated state %q", resp.Header.Get("Location"), stub.lastState)
	}
	stateCookie := cookieNamed(resp, "blerg_oauth_state")
	if stateCookie == nil || !stateCookie.HttpOnly || !stateCookie.Secure || stateCookie.Path != "/auth" {
		t.Fatalf("state cookie = %+v, want HttpOnly+Secure, Path=/auth", stateCookie)
	}
	sum := sha256.Sum256([]byte(stub.lastState))
	if stateCookie.Value != hex.EncodeToString(sum[:]) {
		t.Fatal("state cookie must hold SHA-256(state), never the state itself")
	}

	// Wrong state → 400, no session.
	req, _ := http.NewRequest("GET", srv.URL+"/auth/callback?code=ok&state=wrong", nil)
	req.AddCookie(stateCookie)
	resp, _ = client.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback with wrong state = %d, want 400", resp.StatusCode)
	}
	// Missing cookie → 400.
	req, _ = http.NewRequest("GET", srv.URL+"/auth/callback?code=ok&state="+url.QueryEscape(stub.lastState), nil)
	resp, _ = client.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback without state cookie = %d, want 400", resp.StatusCode)
	}
	// Matching state → session cookie set, state cookie cleared, redirect into /auth/refresh.
	req, _ = http.NewRequest("GET", srv.URL+"/auth/callback?code=ok&state="+url.QueryEscape(stub.lastState), nil)
	req.AddCookie(stateCookie)
	resp, _ = client.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback = %d, want 302", resp.StatusCode)
	}
	wantLoc := "https://core.example.com/auth/refresh?return_to=" + url.QueryEscape("https://core.example.com/app")
	if resp.Header.Get("Location") != wantLoc {
		t.Fatalf("Location = %q, want %q", resp.Header.Get("Location"), wantLoc)
	}
	if c := cookieNamed(resp, "blerg_core_refresh"); c == nil || c.Value == "" {
		t.Fatal("callback must set the refresh cookie")
	}
	if c := cookieNamed(resp, "blerg_oauth_state"); c == nil || c.MaxAge >= 0 {
		t.Fatal("callback must clear the state cookie")
	}
}

func containsQuery(rawURL, key, want string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Query().Get(key) == want
}

func TestOAuthStartIs404ForLocalProvider(t *testing.T) {
	deps, _ := newTestDeps(t, nil, nil) // AuthProvider is authprovider.Local
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()
	resp, err := noRedirects(srv.Client()).Get(srv.URL + "/auth/start")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/auth/start under local = %d, want 404", resp.StatusCode)
	}
}

func TestAuthProviderEndpoint(t *testing.T) {
	deps, _ := newTestDeps(t, nil, nil)
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/auth/provider")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.ID != "local" {
		t.Fatalf("/auth/provider = %+v (%v), want {id: local}", body, err)
	}
}
