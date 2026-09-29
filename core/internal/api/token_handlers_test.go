package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// createdTokenBody mirrors POST /api/tokens's 201 response.
type createdTokenBody struct {
	ID        string   `json:"id"`
	Token     string   `json:"token"`
	Name      string   `json:"name"`
	Aud       string   `json:"aud"`
	Caps      []string `json:"caps"`
	ExpiresAt string   `json:"expires_at"`
	CreatedAt string   `json:"created_at"`
}

// tokenSummaryBody mirrors one entry of GET /api/tokens.
type tokenSummaryBody struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Aud        string   `json:"aud"`
	Caps       []string `json:"caps"`
	CreatedAt  string   `json:"created_at"`
	ExpiresAt  string   `json:"expires_at"`
	LastUsedAt *string  `json:"last_used_at"`
	RevokedAt  *string  `json:"revoked_at"`
}

// newTokensTestDeps wires a TLS test server (so the same-origin CSRF check has a meaningful
// origin to compare against, matching TestLoginCSRFMatrix's convention) plus one member
// account with a freshly minted human access token.
func newTokensTestDeps(t *testing.T, subject string) (api.Deps, *pgxpool.Pool, *httptest.Server, string, string) {
	t.Helper()
	deps, pool := newTestDeps(t, nil, nil)
	accountID := insertCredAccount(t, pool, subject)
	idSvc := deps.Identity.(*identity.Service)
	tok, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
	if err != nil {
		t.Fatalf("mint human token: %v", err)
	}
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	return deps, pool, srv, accountID, tok
}

// postToken sends a same-origin POST /api/tokens with the given body.
func postToken(t *testing.T, srv *httptest.Server, bearer string, body any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/tokens", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.URL)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func listTokens(t *testing.T, srv *httptest.Server, bearer string) ([]tokenSummaryBody, int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode
	}
	var out []tokenSummaryBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return out, resp.StatusCode
}

func deleteToken(t *testing.T, srv *httptest.Server, bearer, id string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/tokens/"+id, nil)
	req.Header.Set("Origin", srv.URL)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestAgentTokenEndpointsRoundTrip is the create → list → revoke path through HTTP: the 201
// carries a usable token with the preset's audience and claims, the list carries metadata only
// (never the token or its hash), and DELETE marks it revoked.
func TestAgentTokenEndpointsRoundTrip(t *testing.T) {
	_, _, srv, accountID, human := newTokensTestDeps(t, "tokens-roundtrip")

	resp := postToken(t, srv, human, map[string]any{
		"name": "laptop cli", "preset": "run-sessions", "expires_in_days": 30,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/tokens = %d, want 201", resp.StatusCode)
	}
	var created createdTokenBody
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Token == "" {
		t.Fatalf("created = %+v, want an id and a token", created)
	}
	if created.Name != "laptop cli" || created.Aud != "blerg-runner" {
		t.Errorf("name/aud = %q/%q", created.Name, created.Aud)
	}
	if len(created.Caps) != 1 || created.Caps[0] != "session.start" {
		t.Errorf("caps = %v, want [session.start]", created.Caps)
	}
	exp, err := time.Parse(time.RFC3339, created.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at %q is not RFC3339: %v", created.ExpiresAt, err)
	}
	if d := time.Until(exp); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("expires_at = %v (in %v), want ~30 days out", exp, d)
	}

	claims := decodeClaims(t, created.Token)
	if claims.Kind != string(cid.Agent) {
		t.Errorf("kind = %q, want agent", claims.Kind)
	}
	if claims.Sub != created.ID {
		t.Errorf("sub = %q, want the token id %q", claims.Sub, created.ID)
	}
	if claims.OnBehalfOf != accountID || claims.Lineage != accountID {
		t.Errorf("on_behalf_of/lineage = %q/%q, want %q", claims.OnBehalfOf, claims.Lineage, accountID)
	}
	if claims.Aud != "blerg-runner" {
		t.Errorf("aud = %q, want blerg-runner", claims.Aud)
	}
	if claims.ExpiresAt != exp.Unix() {
		t.Errorf("exp = %d, want %d", claims.ExpiresAt, exp.Unix())
	}

	list, code := listTokens(t, srv, human)
	if code != http.StatusOK {
		t.Fatalf("GET /api/tokens = %d, want 200", code)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v, want the one token", list)
	}
	if list[0].RevokedAt != nil || list[0].LastUsedAt != nil {
		t.Errorf("fresh token listed as used/revoked: %+v", list[0])
	}

	// The list must never carry the token value or its hash, under any key.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+human)
	raw, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Body.Close()
	var asMaps []map[string]any
	if err := json.NewDecoder(raw.Body).Decode(&asMaps); err != nil {
		t.Fatal(err)
	}
	for _, m := range asMaps {
		for _, forbidden := range []string{"token", "token_hash"} {
			if _, has := m[forbidden]; has {
				t.Errorf("GET /api/tokens leaks %q", forbidden)
			}
		}
		for k, v := range m {
			if s, ok := v.(string); ok && s == created.Token {
				t.Errorf("GET /api/tokens leaks the token value under %q", k)
			}
		}
	}

	del := deleteToken(t, srv, human, created.ID)
	del.Body.Close()
	if del.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/tokens/{id} = %d, want 200", del.StatusCode)
	}
	list, _ = listTokens(t, srv, human)
	if len(list) != 1 || list[0].RevokedAt == nil {
		t.Fatalf("after DELETE list = %+v, want revoked_at set", list)
	}
	// Idempotent: deleting again is still 200.
	del2 := deleteToken(t, srv, human, created.ID)
	del2.Body.Close()
	if del2.StatusCode != http.StatusOK {
		t.Fatalf("second DELETE = %d, want 200", del2.StatusCode)
	}
}

// TestCreateAgentTokenValidation: name and expiry bounds and the closed preset list are all
// enforced server-side with a 400, never silently coerced.
func TestCreateAgentTokenValidation(t *testing.T) {
	_, _, srv, _, human := newTokensTestDeps(t, "tokens-validation")

	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty name", map[string]any{"name": "", "preset": "platform"}},
		{"blank name", map[string]any{"name": "   ", "preset": "platform"}},
		{"name too long", map[string]any{"name": strings.Repeat("x", 65), "preset": "platform"}},
		{"missing preset", map[string]any{"name": "n"}},
		{"unknown preset", map[string]any{"name": "n", "preset": "root"}},
		{"zero days is not 'default'", map[string]any{"name": "n", "preset": "platform", "expires_in_days": -1}},
		{"too many days", map[string]any{"name": "n", "preset": "platform", "expires_in_days": 366}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postToken(t, srv, human, tc.body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("POST %v = %d, want 400", tc.body, resp.StatusCode)
			}
		})
	}

	t.Run("omitted expires_in_days defaults to 90 days", func(t *testing.T) {
		resp := postToken(t, srv, human, map[string]any{"name": "default expiry", "preset": "platform"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST = %d, want 201", resp.StatusCode)
		}
		var created createdTokenBody
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatal(err)
		}
		exp, err := time.Parse(time.RFC3339, created.ExpiresAt)
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Until(exp); d < 89*24*time.Hour || d > 91*24*time.Hour {
			t.Errorf("default expiry = %v, want ~90 days", d)
		}
	})

	t.Run("name is trimmed, and 64 chars is accepted", func(t *testing.T) {
		resp := postToken(t, srv, human, map[string]any{
			"name": "  " + strings.Repeat("y", 64) + "  ", "preset": "platform",
		})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST = %d, want 201", resp.StatusCode)
		}
		var created createdTokenBody
		_ = json.NewDecoder(resp.Body).Decode(&created)
		if created.Name != strings.Repeat("y", 64) {
			t.Errorf("name = %q, want it trimmed to 64 y's", created.Name)
		}
	})
}

// TestCreateAgentTokenRejectsCrossSite: minting a long-lived credential off the caller's cookie
// session is exactly the sort of state change CSRF targets, so POST and DELETE both carry the
// same same-origin guard as /auth/login. GET (read-only) does not need it.
func TestCreateAgentTokenRejectsCrossSite(t *testing.T) {
	_, _, srv, _, human := newTokensTestDeps(t, "tokens-crosssite")

	body, _ := json.Marshal(map[string]any{"name": "evil", "preset": "platform"})
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/tokens"},
		{http.MethodDelete, "/api/tokens/00000000-0000-0000-0000-000000000000"},
	} {
		req, _ := http.NewRequest(route.method, srv.URL+route.path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+human)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("cross-site %s %s = %d, want 403", route.method, route.path, resp.StatusCode)
		}
	}

	// A foreign Origin is rejected too.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+human)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign-Origin POST = %d, want 403", resp.StatusCode)
	}

	// Nothing was minted.
	if list, _ := listTokens(t, srv, human); len(list) != 0 {
		t.Errorf("a rejected cross-site request still minted %d token(s)", len(list))
	}
}

// TestDeleteAgentTokenIsOwnerScoped: another account's token id is a 404 (never a 200, never a
// 403 that would confirm the id exists), and so is a syntactically invalid or unknown id.
func TestDeleteAgentTokenIsOwnerScoped(t *testing.T) {
	deps, pool, srv, _, human := newTokensTestDeps(t, "tokens-owner")

	otherID := insertCredAccount(t, pool, "tokens-owner-other")
	idSvc := deps.Identity.(*identity.Service)
	otherHuman, err := idSvc.MintHumanAccessToken(context.Background(), otherID, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}

	resp := postToken(t, srv, human, map[string]any{"name": "mine", "preset": "platform"})
	var created createdTokenBody
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	for _, id := range []string{created.ID, "00000000-0000-0000-0000-000000000000", "not-a-uuid"} {
		del := deleteToken(t, srv, otherHuman, id)
		del.Body.Close()
		if del.StatusCode != http.StatusNotFound {
			t.Errorf("DELETE %s as another account = %d, want 404", id, del.StatusCode)
		}
	}

	// The owner's token is untouched, and the other account sees an empty list (never the
	// owner's rows).
	list, _ := listTokens(t, srv, human)
	if len(list) != 1 || list[0].RevokedAt != nil {
		t.Errorf("owner's token = %+v, want untouched", list)
	}
	if otherList, _ := listTokens(t, srv, otherHuman); len(otherList) != 0 {
		t.Errorf("other account's list = %+v, want empty", otherList)
	}
}

// TestAgentTokenEndpointsRequireAHumanSession: no token is 401 and an AGENT token is 403 on all
// three routes — an agent token must never be able to mint another, longer-lived agent token
// (that would make revocation unenforceable: kill one and its offspring live on).
func TestAgentTokenEndpointsRequireAHumanSession(t *testing.T) {
	deps, _, srv, accountID, _ := newTokensTestDeps(t, "tokens-humanonly")
	idSvc := deps.Identity.(*identity.Service)
	agentTok, err := idSvc.MintAgentToken(context.Background(), identity.AgentTokenInput{
		Sub: "11111111-1111-1111-1111-111111111111", Aud: "blerg-core",
		OnBehalfOf: accountID, Lineage: accountID, Caps: []string{"card.read"},
	})
	if err != nil {
		t.Fatal(err)
	}

	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/tokens"},
		{http.MethodGet, "/api/tokens"},
		{http.MethodDelete, "/api/tokens/00000000-0000-0000-0000-000000000000"},
	}
	for _, route := range routes {
		for _, tc := range []struct {
			name, bearer string
			want         int
		}{
			{"no token", "", http.StatusUnauthorized},
			{"agent token", agentTok, http.StatusForbidden},
		} {
			body, _ := json.Marshal(map[string]any{"name": "n", "preset": "platform"})
			req, _ := http.NewRequest(route.method, srv.URL+route.path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", srv.URL)
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("%s %s with %s = %d, want %d", route.method, route.path, tc.name, resp.StatusCode, tc.want)
			}
		}
	}
}

// TestCreateAgentTokenIsRateLimited: the endpoint is guarded by the same LoginLimiter as
// /auth/login, keyed on the authenticated account, so a stolen access token does not buy an
// unbounded run at minting long-lived credentials.
func TestCreateAgentTokenIsRateLimited(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.LoginLimiter = api.NewLoginLimiter(2, time.Minute)
	accountID := insertCredAccount(t, pool, "tokens-ratelimited")
	idSvc := deps.Identity.(*identity.Service)
	human, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()

	// Two rejected attempts fill the window...
	for i := 0; i < 2; i++ {
		resp := postToken(t, srv, human, map[string]any{"name": "n", "preset": "root"})
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("attempt %d = %d, want 400", i, resp.StatusCode)
		}
	}
	// ...and the next attempt is refused before it is even parsed, even though it is valid.
	resp := postToken(t, srv, human, map[string]any{"name": "n", "preset": "platform"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("POST after the limit = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("429 is missing a Retry-After header")
	}
}

// TestCreateAgentTokenRefusesDisabledAccount: a disabled account's still-valid access token
// must not be able to mint a credential that would outlive the disable by up to a year.
func TestCreateAgentTokenRefusesDisabledAccount(t *testing.T) {
	_, pool, srv, accountID, human := newTokensTestDeps(t, "tokens-disabled")
	if _, err := pool.Exec(context.Background(),
		`UPDATE accounts SET disabled_at = now() WHERE id = $1`, accountID); err != nil {
		t.Fatal(err)
	}
	resp := postToken(t, srv, human, map[string]any{"name": "n", "preset": "platform"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST as a disabled account = %d, want 403", resp.StatusCode)
	}
}

// TestCreateAgentTokenRejectsTooManyLiveTokens: at the per-account cap the endpoint answers
// 409 with a JSON error naming the limit and the way out, rather than an opaque 500 or an
// unbounded pile of live credentials.
func TestCreateAgentTokenRejectsTooManyLiveTokens(t *testing.T) {
	_, pool, srv, accountID, human := newTokensTestDeps(t, "tokens-cap")

	// Fill the account to exactly the cap by inserting rows directly — this test is about the
	// handler's answer at the boundary, not about minting 50 real tokens over HTTP.
	for i := 0; i < 50; i++ {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO agent_tokens (account_id, name, aud, caps, token_hash, expires_at)
			 VALUES ($1, 'filler', 'blerg-core', ARRAY['card.read'], $2, now() + interval '30 days')`,
			accountID, "hash-"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}

	resp := postToken(t, srv, human, map[string]any{"name": "one too many", "preset": "platform"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST at the cap = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("409 body is not JSON: %v", err)
	}
	if !strings.Contains(body.Error, "too many active agent tokens") {
		t.Errorf("error = %q, want it to name the limit", body.Error)
	}

	// Revoking one frees a slot and the next mint succeeds — the cap counts LIVE tokens.
	if _, err := pool.Exec(context.Background(),
		`UPDATE agent_tokens SET revoked_at = now() WHERE token_hash = 'hash-0'`); err != nil {
		t.Fatal(err)
	}
	ok := postToken(t, srv, human, map[string]any{"name": "now there is room", "preset": "platform"})
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusCreated {
		t.Fatalf("POST after freeing a slot = %d, want 201", ok.StatusCode)
	}
}

// TestCreateAgentTokenRejectsOversizeBody: the body is capped BEFORE decoding, like every other
// authenticated POST in this package (audit M-4).
func TestCreateAgentTokenRejectsOversizeBody(t *testing.T) {
	_, _, srv, _, human := newTokensTestDeps(t, "tokens-oversize")

	huge := `{"name":"` + strings.Repeat("x", 32<<10) + `","preset":"platform"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/tokens", strings.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("Authorization", "Bearer "+human)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize POST = %d, want 413", resp.StatusCode)
	}
}
