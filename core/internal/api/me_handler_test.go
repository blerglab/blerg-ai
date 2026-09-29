package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// meResponse mirrors GET /api/me's success body for test assertions.
type meResponse struct {
	AccountID          string `json:"account_id"`
	Provider           string `json:"provider"`
	ProviderSubject    string `json:"provider_subject"`
	Email              string `json:"email"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"must_change_password"`
}

// TestMeReturnsSignedInIdentity: GET /api/me with a minted human access token returns the
// caller's own identity, derived from the accounts row the verified token's Sub claim points
// at — never anything client-supplied, and never the password hash.
func TestMeReturnsSignedInIdentity(t *testing.T) {
	deps, pool := newTestDeps(t, []string{"http://board.example.com"}, nil)
	insertLocalAccount(t, pool, "meuser", "me-password-1")

	var accountID string
	if err := pool.QueryRow(context.Background(),
		`SELECT id::text FROM accounts WHERE provider_subject='meuser'`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	idSvc := deps.Identity.(*identity.Service)
	tok, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/me = %d, want 200", resp.StatusCode)
	}
	var body meResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.AccountID != accountID {
		t.Errorf("account_id = %q, want %q", body.AccountID, accountID)
	}
	if body.Provider != "local" {
		t.Errorf("provider = %q, want %q", body.Provider, "local")
	}
	if body.ProviderSubject != "meuser" {
		t.Errorf("provider_subject = %q, want %q", body.ProviderSubject, "meuser")
	}
	if body.Email != "meuser@example.com" {
		t.Errorf("email = %q, want %q", body.Email, "meuser@example.com")
	}
	if body.Role != "member" {
		t.Errorf("role = %q, want %q", body.Role, "member")
	}
	if body.MustChangePassword != false {
		t.Errorf("must_change_password = %v, want false", body.MustChangePassword)
	}

	// The password hash must never appear anywhere in the response body.
	var rawMap map[string]any
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
	req2.Header.Set("Authorization", "Bearer "+tok)
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if err := json.NewDecoder(resp2.Body).Decode(&rawMap); err != nil {
		t.Fatal(err)
	}
	if _, has := rawMap["password_hash"]; has {
		t.Error("response leaks password_hash")
	}
}

// TestMeRequiresToken: GET /api/me without a bearer token must fail closed (401).
func TestMeRequiresToken(t *testing.T) {
	deps, _ := newTestDeps(t, []string{"http://board.example.com"}, nil)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/me without token = %d, want 401", resp.StatusCode)
	}
}

// TestMeRejectsUnknownAgentToken: GET /api/me accepts a user-minted `platform` agent token
// (see TestMeForPlatformAgentToken), but an agent-kind token that is NOT one — a service-style
// token minted internally, with no agent_tokens row behind it — has no account to report and
// must be refused with 403 rather than treated as somebody's identity.
func TestMeRejectsUnknownAgentToken(t *testing.T) {
	deps, _ := newTestDeps(t, []string{"http://board.example.com"}, nil)
	idSvc := deps.Identity.(*identity.Service)
	tok, err := idSvc.MintAgentToken(context.Background(), identity.AgentTokenInput{
		Sub: "some-agent", Aud: "blerg-core",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /api/me with agent-kind token = %d, want 403", resp.StatusCode)
	}
}

// agentMeBody mirrors GET /api/me's agent-principal success body.
type agentMeBody struct {
	Kind            string   `json:"kind"`
	TokenID         string   `json:"token_id"`
	AccountID       string   `json:"account_id"`
	ProviderSubject string   `json:"provider_subject"`
	Role            string   `json:"role"`
	Caps            []string `json:"caps"`
}

// TestMeForPlatformAgentToken: a `platform` preset token (aud blerg-core) can introspect
// itself — which token it is, whose account it acts for, and what it may do. This is how an
// external tool confirms a token works before doing anything with it.
func TestMeForPlatformAgentToken(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	accountID := insertCredAccount(t, pool, "me-agent")
	idSvc := deps.Identity.(*identity.Service)
	rec, raw, err := idSvc.CreateAgentToken(context.Background(), accountID, "introspect", "platform", 0)
	if err != nil {
		t.Fatalf("CreateAgentToken: %v", err)
	}

	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()

	get := func(bearer string) (*http.Response, agentMeBody) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body agentMeBody
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
		}
		return resp, body
	}

	resp, body := get(raw)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/me with a platform token = %d, want 200", resp.StatusCode)
	}
	if body.Kind != "agent" {
		t.Errorf("kind = %q, want agent", body.Kind)
	}
	if body.TokenID != rec.ID {
		t.Errorf("token_id = %q, want %q", body.TokenID, rec.ID)
	}
	if body.AccountID != accountID {
		t.Errorf("account_id = %q, want %q (the on_behalf_of account)", body.AccountID, accountID)
	}
	if body.ProviderSubject != "me-agent" {
		t.Errorf("provider_subject = %q, want me-agent", body.ProviderSubject)
	}
	if body.Role != "member" {
		t.Errorf("role = %q, want member", body.Role)
	}
	if len(body.Caps) != 1 || body.Caps[0] != "card.read" {
		t.Errorf("caps = %v, want [card.read]", body.Caps)
	}

	// Revoking the token takes /api/me with it (verifyBearer's revocation check runs first,
	// so this is a 401 rather than the handler's own 403).
	if err := idSvc.RevokeAgentToken(context.Background(), accountID, rec.ID); err != nil {
		t.Fatal(err)
	}
	resp2, _ := get(raw)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/me with a revoked token = %d, want 401", resp2.StatusCode)
	}
}
