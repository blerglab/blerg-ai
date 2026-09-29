package authprovider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
)

// lastExchangeQuery captures the query string fakeGitHubServer's /access_token handler was
// called with, so tests can assert on parameters (like redirect_uri) the token exchange sends.
var lastExchangeQuery url.Values

// fakeGitHubServer builds an httptest.Server standing in for both
// github.com/login/oauth and api.github.com, driven by a caller-supplied
// membership handler for GET /orgs/{org}/members/{login} — the one part
// that varies test to test. Token exchange and /user always succeed, since
// every test here cares about what happens after those succeed.
func fakeGitHubServer(t *testing.T, membership http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			lastExchangeQuery = r.URL.Query()
			json.NewEncoder(w).Encode(map[string]string{"access_token": "gh-token"})
		case "/user":
			json.NewEncoder(w).Encode(map[string]any{"id": 42, "login": "someone"})
		case "/orgs/theorg/members/someone":
			membership(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newGitHubProvider(t *testing.T, srv *httptest.Server) *authprovider.GitHub {
	t.Helper()
	_, st := freshStore(t)
	gh := authprovider.NewGitHub(st, "cid", "csecret", "theorg", srv.Client())
	gh.BaseURL = srv.URL
	gh.AuthorizeURL = srv.URL + "/login/oauth"
	return gh
}

func callbackRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state=y", nil)
	return req
}

func TestGitHubCallbackAcceptsOrgMember(t *testing.T) {
	srv := fakeGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent) // GitHub's documented "is a member" response
	})
	gh := newGitHubProvider(t, srv)

	acc, err := gh.Callback(context.Background(), callbackRequest())
	if err != nil {
		t.Fatalf("expected org member to be accepted, got err: %v", err)
	}
	if acc.Provider != "github" {
		t.Errorf("expected Provider %q, got %q", "github", acc.Provider)
	}
	if acc.ProviderSubject != "42" {
		t.Errorf("expected ProviderSubject %q, got %q", "42", acc.ProviderSubject)
	}
	if acc.Email != "someone" {
		t.Errorf("expected Email (github login) %q, got %q", "someone", acc.Email)
	}
	if acc.ID == "" {
		t.Error("expected non-empty Account.ID")
	}
}

func TestGitHubCallbackUpsertsOnRepeatLogin(t *testing.T) {
	srv := fakeGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	gh := newGitHubProvider(t, srv)

	acc1, err := gh.Callback(context.Background(), callbackRequest())
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	acc2, err := gh.Callback(context.Background(), callbackRequest())
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if acc1.ID != acc2.ID {
		t.Errorf("expected same account id across repeat logins, got %q then %q", acc1.ID, acc2.ID)
	}
}

func TestGitHubCallbackRejectsNonOrgMember(t *testing.T) {
	srv := fakeGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // not a member
	})
	gh := newGitHubProvider(t, srv)

	if _, err := gh.Callback(context.Background(), callbackRequest()); err == nil {
		t.Error("non-org-member must be rejected (fail closed)")
	}
}

func TestGitHubCallbackFailsClosedOnMembershipAPIError(t *testing.T) {
	srv := fakeGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // GitHub API is down
	})
	gh := newGitHubProvider(t, srv)

	if _, err := gh.Callback(context.Background(), callbackRequest()); err == nil {
		t.Error("an inconclusive membership check must reject login, not admit it")
	}
}

// TestGitHubCallbackFailsClosedWhenMembershipRequestErrors proves the
// fail-closed path holds even when the membership check can't reach GitHub
// at all (not just when GitHub answers with an error status) — e.g. a
// network blip. Simulated here by pointing BaseURL at a server that accepts
// the connection but resets it for this one path.
func TestGitHubCallbackFailsClosedWhenMembershipRequestErrors(t *testing.T) {
	srv := fakeGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		conn.Close() // abrupt close: simulates an unreachable/erroring API call
	})
	gh := newGitHubProvider(t, srv)

	if _, err := gh.Callback(context.Background(), callbackRequest()); err == nil {
		t.Error("a membership request transport error must reject login, not admit it")
	}
}

// TestGitHubLoginURLAndExchangeCarryRedirectURI proves SetRedirectURL's value reaches both the
// authorize URL (so the IdP knows where to send the browser back) and the token exchange (GitHub
// requires the same redirect_uri on both legs, or it rejects the exchange).
func TestGitHubLoginURLAndExchangeCarryRedirectURI(t *testing.T) {
	srv := fakeGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	g := newGitHubProvider(t, srv)
	g.SetRedirectURL("https://core.example.com/auth/callback")
	u, err := url.Parse(g.LoginURL("s"))
	if err != nil || u.Query().Get("redirect_uri") != "https://core.example.com/auth/callback" {
		t.Fatalf("LoginURL redirect_uri = %q", u.Query().Get("redirect_uri"))
	}
	_, _ = g.Callback(context.Background(), callbackRequest())
	if lastExchangeQuery.Get("redirect_uri") != "https://core.example.com/auth/callback" {
		t.Fatalf("token exchange redirect_uri = %q", lastExchangeQuery.Get("redirect_uri"))
	}
}
