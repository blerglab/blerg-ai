package mcpconn_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

// MINOR 35 (d): a protected resource document that does not name its resource is refused.
func TestOAuthRefusesResourceMetadataWithoutResource(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.PRMOmitResource = true
	_, err := e.svc.StartOAuth(context.Background(), e.start("notes"))
	wantValidation(t, err, "resource")
}

// MINOR 35 (d): RFC 8414 says an absent token_endpoint_auth_methods_supported means
// client_secret_basic. A client_id alone (a public client) cannot work against such a server and
// the person is told so; dynamic registration asks for client_secret_basic.
func TestOAuthAuthMethodDefaultsToClientSecretBasic(t *testing.T) {
	ctx := context.Background()

	e := newOAuthEnv(t)
	e.as.OmitAuthMethods = true
	e.as.AddClient("my-client")
	in := e.start("notes")
	in.ClientID = "my-client"
	_, err := e.svc.StartOAuth(ctx, in)
	wantValidation(t, err, "client authentication")

	e2 := newOAuthEnv(t)
	e2.as.AuthMethods = []string{"client_secret_basic", "client_secret_post"}
	e2.as.AddClient("my-client")
	in = e2.start("notes")
	in.ClientID = "my-client"
	_, err = e2.svc.StartOAuth(ctx, in)
	wantValidation(t, err, "client authentication")

	// Dynamic registration against the same omitted list asks for client_secret_basic.
	e3 := newOAuthEnv(t)
	e3.as.OmitAuthMethods = true
	if _, err := e3.svc.StartOAuth(ctx, e3.start("notes")); err != nil {
		t.Fatal(err)
	}
	if got := e3.as.Registered[0]["token_endpoint_auth_method"]; got != "client_secret_basic" {
		t.Fatalf("registered with auth method %v, want client_secret_basic", got)
	}

	// A server that lists none keeps working with a user supplied client_id.
	e4 := newOAuthEnv(t)
	e4.as.AddClient("my-client")
	in = e4.start("notes")
	in.ClientID = "my-client"
	if _, err := e4.svc.StartOAuth(ctx, in); err != nil {
		t.Fatalf("a public client against a server listing none: %v", err)
	}
}

// MINOR 35 (h): the start result names the authorization server (its host) so the person can see
// where they are being sent.
func TestOAuthStartNamesTheAuthorizationServer(t *testing.T) {
	e := newOAuthEnv(t)
	res, err := e.svc.StartOAuth(context.Background(), e.start("notes"))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(e.as.Issuer())
	if res.AuthorizationServer != u.Host {
		t.Fatalf("AuthorizationServer = %q, want %q", res.AuthorizationServer, u.Host)
	}
}

func reconnect(t *testing.T, e *oauthEnv, id, redirect string) mcpconn.Connection {
	t.Helper()
	ctx := context.Background()
	in := mcpconn.OAuthStartInput{AccountID: e.acct, SessionID: liveSid, ReconnectID: id, RedirectURI: redirect}
	res, err := e.svc.StartOAuth(ctx, in)
	if err != nil {
		t.Fatalf("reconnect start: %v", err)
	}
	q, err := e.as.Approve(res.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	cb := callback(q, alwaysLive)
	cb.RedirectURI = redirect
	c, err := e.svc.CompleteOAuth(ctx, cb)
	if err != nil {
		t.Fatalf("reconnect complete: %v", err)
	}
	return c
}

// MINOR 35 (e): a reconnect re-registers when the redirect URI changed, but reuses the client
// otherwise (TestOAuthReconnectReplacesTokens covers the unchanged case).
func TestOAuthReconnectReRegistersWhenTheRedirectURIChanged(t *testing.T) {
	e := newOAuthEnv(t)
	c := e.connect(t, "notes")
	reconnect(t, e, c.ID, "https://other.example.com/auth/mcp/callback")
	if len(e.as.Registered) != 2 {
		t.Fatalf("%d registrations, want 2 (the redirect URI changed)", len(e.as.Registered))
	}
	if uris, _ := e.as.Registered[1]["redirect_uris"].([]any); len(uris) != 1 || uris[0] != "https://other.example.com/auth/mcp/callback" {
		t.Fatalf("second registration redirect uris = %v", e.as.Registered[1]["redirect_uris"])
	}
	// Reconnecting again with the same URI now reuses the second client.
	reconnect(t, e, c.ID, "https://other.example.com/auth/mcp/callback")
	if len(e.as.Registered) != 2 {
		t.Fatalf("%d registrations after an unchanged reconnect, want still 2", len(e.as.Registered))
	}
}

// MINOR 35 (e): a client secret that has expired (client_secret_expires_at) is not reused.
func TestOAuthReconnectReRegistersWhenTheClientSecretExpired(t *testing.T) {
	e := newOAuthEnv(t)
	e.as.Confidential = true
	e.as.ClientSecretExpiresAt = time.Now().Add(-time.Hour).Unix()
	c := e.connect(t, "notes")
	reconnect(t, e, c.ID, redirectURI)
	if len(e.as.Registered) != 2 {
		t.Fatalf("%d registrations, want 2 (the client secret had expired)", len(e.as.Registered))
	}

	// A secret that has not expired is reused.
	e2 := newOAuthEnv(t)
	e2.as.Confidential = true
	e2.as.ClientSecretExpiresAt = time.Now().Add(24 * time.Hour).Unix()
	c2 := e2.connect(t, "notes")
	reconnect(t, e2, c2.ID, redirectURI)
	if len(e2.as.Registered) != 1 {
		t.Fatalf("%d registrations, want 1 (the client secret is still valid)", len(e2.as.Registered))
	}
}

// MINOR 35 (g): after the reconnect's new grant is stored, the OLD grant is revoked upstream, and
// the new one is untouched.
func TestOAuthReconnectRevokesTheOldGrant(t *testing.T) {
	e := newOAuthEnv(t)
	c := e.connect(t, "notes")
	if _, _, revokes, _ := e.as.Stats(); revokes != 0 {
		t.Fatalf("%d revocations before the reconnect", revokes)
	}
	e.as.SetAccessTTL(30) // the new token is inside the refresh window: fetching it refreshes the NEW refresh token
	reconnect(t, e, c.ID, redirectURI)

	e.as.Lock()
	revoked := append([]url.Values(nil), e.as.Revoked...)
	e.as.Unlock()
	if len(revoked) != 2 || revoked[0].Get("token_type_hint") != "refresh_token" || revoked[1].Get("token_type_hint") != "access_token" {
		t.Fatalf("revocations = %v, want the old refresh token then the old access token", revoked)
	}
	// The new grant still works: its refresh token was not among the revoked.
	res, err := e.fetch(t, c.ID)
	if err != nil || !strings.HasPrefix(res.Value, "Bearer at-") {
		t.Fatalf("fetch on the new grant = %+v %v", res, err)
	}
	for _, r := range revoked {
		if "Bearer "+r.Get("token") == res.Value {
			t.Fatal("the new access token was revoked")
		}
	}
	if spent, reuse, _, _ := e.as.Stats(); spent != 1 || reuse != 0 {
		t.Fatalf("spent=%d reuse=%d: the new refresh token did not refresh cleanly", spent, reuse)
	}
}
