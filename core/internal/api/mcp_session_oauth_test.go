package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

const sessionDraftID = "00000000-0000-4000-8000-000000000147"
const sessionFlowState = "smcp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeSessionFlow struct {
	account, sid, redirect string
	completed, started     int
}

func (f *fakeSessionFlow) PreviewSessionOAuth(ctx context.Context, in mcpconn.SessionOAuthPreviewInput) (mcpconn.SessionOAuthPreview, error) {
	if in.AccountID != f.account || in.HumanSessionID != f.sid {
		return mcpconn.SessionOAuthPreview{}, mcpconn.ErrNotFound
	}
	return mcpconn.SessionOAuthPreview{DraftID: sessionDraftID, Name: "session-server", Source: "settings", ProviderHost: "provider.example.com", IssuerHost: "issuer.example.com", ResourceHost: "resource.example.com", Scopes: []string{"read"}}, nil
}
func (f *fakeSessionFlow) StartSessionOAuth(ctx context.Context, account, sid, id, redirect string) (mcpconn.OAuthStartResult, error) {
	if account != f.account || sid != f.sid || id != sessionDraftID || redirect != f.redirect {
		return mcpconn.OAuthStartResult{}, mcpconn.ErrNotFound
	}
	f.started++
	hash := sha256.Sum256([]byte(sessionFlowState))
	return mcpconn.OAuthStartResult{AuthorizationURL: "https://provider.example.com/authorize?state=" + sessionFlowState, AuthorizationServer: "provider.example.com", StateCookie: hex.EncodeToString(hash[:])}, nil
}
func (f *fakeSessionFlow) CompleteSessionOAuth(ctx context.Context, in mcpconn.OAuthCallbackInput) (mcpconn.SessionOAuthCompletion, error) {
	if in.State != sessionFlowState || in.Code == "" || in.RedirectURI != f.redirect {
		return mcpconn.SessionOAuthCompletion{}, mcpconn.ErrNotFound
	}
	live, err := in.SessionLive(ctx, f.account, f.sid)
	if err != nil || !live {
		return mcpconn.SessionOAuthCompletion{}, mcpconn.ErrNotFound
	}
	f.completed++
	return mcpconn.SessionOAuthCompletion{AttemptID: sessionDraftID}, nil
}
func (f *fakeSessionFlow) CancelSessionOAuth(ctx context.Context, account, sid, id string) error {
	return nil
}
func (f *fakeSessionFlow) SessionOAuthStatus(ctx context.Context, account, sid, id string) (mcpconn.SessionOAuthAttemptStatus, error) {
	if account != f.account || sid != f.sid || id != sessionDraftID {
		return mcpconn.SessionOAuthAttemptStatus{}, mcpconn.ErrNotFound
	}
	stage := "pending"
	if f.completed > 0 {
		stage = "completed"
	}
	return mcpconn.SessionOAuthAttemptStatus{Stage: stage}, nil
}
func newSessionOAuthAPI(t *testing.T, wired bool) (*oauthAPIEnv, *fakeSessionFlow, *http.Client, *http.Client) {
	t.Helper()
	e := newOAuthAPIEnv(t)
	e.srv.Close()
	deps := e.deps
	deps.CookieSecure = true
	server := httptest.NewUnstartedServer(nil)
	deps.PublicURL = "https://" + server.Listener.Addr().String()
	fake := &fakeSessionFlow{account: e.acctA, sid: e.sidA, redirect: deps.PublicURL + "/auth/mcp/callback"}
	if wired {
		deps.SessionMCPOAuth = fake
	}
	server.Config.Handler = api.NewRouter(deps)
	server.StartTLS()
	t.Cleanup(server.Close)
	e.srv = server
	e.deps = deps
	a, b := *server.Client(), *server.Client()
	a.Jar, _ = cookiejar.New(nil)
	b.Jar, _ = cookiejar.New(nil)
	a.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	b.CheckRedirect = a.CheckRedirect
	return e, fake, &a, &b
}
func sessionRequest(t *testing.T, e *oauthAPIEnv, client *http.Client, method, path, bearer string, body any, origin string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, e.srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}
func prepareSessionBrowser(t *testing.T, e *oauthAPIEnv, client *http.Client) {
	t.Helper()
	resp, _ := sessionRequest(t, e, client, "POST", "/api/mcp/session-oauth/preview", e.humanA, map[string]any{"runner_session_id": sessionDraftID, "definition_id": sessionDraftID, "scopes": "read"}, e.srv.URL)
	if resp.StatusCode != 200 {
		t.Fatalf("preview status %d", resp.StatusCode)
	}
	resp, _ = sessionRequest(t, e, client, "POST", "/api/mcp/session-oauth/"+sessionDraftID+"/start", e.humanA, map[string]any{}, e.srv.URL)
	if resp.StatusCode != 200 {
		t.Fatalf("start status %d", resp.StatusCode)
	}
	if cookie := stateCookieOf(resp); cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" {
		t.Fatal("state cookie missing browser safeguards")
	}
}
func TestSessionOAuthHumanBrowserBinding(t *testing.T) {
	e, fake, a, b := newSessionOAuthAPI(t, true)
	prepareSessionBrowser(t, e, a)
	callback := "/auth/mcp/callback?state=" + sessionFlowState + "&code=synthetic-code"
	resp, _ := sessionRequest(t, e, b, "GET", callback, "", nil, "")
	if resp.StatusCode != 400 || fake.completed != 0 {
		t.Fatal("another browser consumed callback")
	}
	origin, _ := url.Parse(e.srv.URL)
	b.Jar.SetCookies(origin, []*http.Cookie{{Name: oauthCookieName(sessionFlowState), Value: "synthetic-wrong-browser", Path: "/auth", Secure: true}})
	resp, _ = sessionRequest(t, e, b, "GET", callback, "", nil, "")
	if resp.StatusCode != 400 || fake.completed != 0 {
		t.Fatal("mismatched browser cookie consumed callback")
	}
	resp, _ = sessionRequest(t, e, a, "GET", callback, "", nil, "")
	if resp.StatusCode != 303 || fake.completed != 1 {
		t.Fatal("initiating browser could not complete")
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("mcp_session_result") != sessionDraftID || location.Query().Has("mcp_oauth") || strings.Contains(location.RawQuery, "synthetic-code") {
		t.Fatal("callback did not use a clean, authoritative result reference")
	}
	resp, _ = sessionRequest(t, e, a, "GET", callback, "", nil, "")
	if resp.StatusCode != 400 || fake.completed != 1 {
		t.Fatal("replayed browser callback accepted")
	}
	_, unknown := sessionRequest(t, e, a, "GET", "/api/mcp/session-oauth/00000000-0000-4000-8000-000000000999", e.humanA, nil, "")
	resp, foreign := sessionRequest(t, e, b, "GET", "/api/mcp/session-oauth/"+sessionDraftID, e.humanB, nil, "")
	if resp.StatusCode != 404 || foreign != unknown {
		t.Fatal("foreign result reference distinguishable from unknown")
	}
}
func TestSessionOAuthUnwiredHTTPRefusesStartAndCallback(t *testing.T) {
	e, _, a, _ := newSessionOAuthAPI(t, false)
	for _, path := range []string{"/api/mcp/session-oauth/preview", "/api/mcp/session-oauth/" + sessionDraftID + "/start"} {
		body := map[string]any{}
		if strings.HasSuffix(path, "preview") {
			body = map[string]any{"runner_session_id": sessionDraftID, "definition_id": sessionDraftID}
		}
		resp, _ := sessionRequest(t, e, a, "POST", path, e.humanA, body, e.srv.URL)
		if resp.StatusCode != 503 || stateCookieOf(resp) != nil {
			t.Fatalf("unwired start returned %d", resp.StatusCode)
		}
	}
	hash := sha256.Sum256([]byte(sessionFlowState))
	origin, _ := url.Parse(e.srv.URL)
	a.Jar.SetCookies(origin, []*http.Cookie{{Name: oauthCookieName(sessionFlowState), Value: hex.EncodeToString(hash[:]), Path: "/auth", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}})
	resp, _ := sessionRequest(t, e, a, "GET", "/auth/mcp/callback?state="+sessionFlowState+"&code=synthetic-code", "", nil, "")
	if resp.StatusCode != 503 {
		t.Fatalf("unwired callback returned %d", resp.StatusCode)
	}
}
func TestSessionOAuthHumanAndOriginGuards(t *testing.T) {
	e, fake, a, _ := newSessionOAuthAPI(t, true)
	for _, bearer := range []string{"", e.agentA, e.humanANo} {
		resp, _ := sessionRequest(t, e, a, "POST", "/api/mcp/session-oauth/preview", bearer, map[string]any{"runner_session_id": sessionDraftID, "definition_id": sessionDraftID}, e.srv.URL)
		if resp.StatusCode < 400 {
			t.Fatal("non-human or unbound login started sign-in")
		}
	}
	resp, _ := sessionRequest(t, e, a, "POST", "/api/mcp/session-oauth/"+sessionDraftID+"/start", e.humanA, map[string]any{}, "https://attacker.example.com")
	if resp.StatusCode != 403 || fake.started != 0 {
		t.Fatal("cross-origin start accepted")
	}
}

func TestSessionOAuthCallbackRejectsAmbiguousResponses(t *testing.T) {
	e, fake, a, _ := newSessionOAuthAPI(t, true)
	prepareSessionBrowser(t, e, a)
	for _, query := range []string{
		"state=" + sessionFlowState + "&state=" + sessionFlowState + "&code=a",
		"state=" + sessionFlowState + "&code=a&code=b",
		"state=" + sessionFlowState + "&code=a&error=denied",
		"state=" + sessionFlowState + "&code=a&iss=",
		"state=" + sessionFlowState + "&code=a&iss=https://issuer.example.com&iss=https://issuer.example.com",
		"state=" + sessionFlowState + "&code=a&response_mode=form_post",
		"state=" + sessionFlowState + "&code=%zz",
		"state=smcp_" + strings.Repeat("a", 200) + "&code=a",
	} {
		resp, _ := sessionRequest(t, e, a, "GET", "/auth/mcp/callback?"+query, "", nil, "")
		if resp.StatusCode != 400 || fake.completed != 0 {
			t.Fatal("ambiguous callback reached exchange")
		}
	}
	// Malformed requests must not consume the legitimate browser's attempt.
	resp, _ := sessionRequest(t, e, a, "GET", "/auth/mcp/callback?state="+sessionFlowState+"&code=valid", "", nil, "")
	if resp.StatusCode != 303 || fake.completed != 1 {
		t.Fatal("invalid callback burned another attempt")
	}
}

func TestSessionOAuthPrefixCannotCaptureLegacyAccountState(t *testing.T) {
	e := newOAuthAPIEnv(t)
	auth, _ := e.begin(t, e.humanA, "legacy")
	parsed, _ := url.Parse(auth)
	original := parsed.Query().Get("state")
	// Existing account states are 43 characters. A random prefix collision must
	// not route that otherwise valid account callback into the new session flow.
	legacy := mcpconn.SessionOAuthStatePrefix + strings.Repeat("a", 43-len(mcpconn.SessionOAuthStatePrefix))
	before, after := sha256.Sum256([]byte(original)), sha256.Sum256([]byte(legacy))
	if _, err := e.pool.Exec(context.Background(), `UPDATE mcp_oauth_state SET state_hash=$2 WHERE state_hash=$1`, before[:], after[:]); err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("state", legacy)
	parsed.RawQuery = query.Encode()
	response, err := e.as.Approve(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: oauthCookieName(legacy), Value: hex.EncodeToString(after[:])}
	resp, _ := e.callback(t, response, cookie)
	_, hint := locationQuery(t, resp)
	if resp.StatusCode != 303 || hint.Get("mcp_oauth") != "success" {
		t.Fatal("session namespace captured a valid account callback")
	}
}
