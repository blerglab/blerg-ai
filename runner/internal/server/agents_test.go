package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
	"github.com/blerglab/blerg-ai/contracts/identity"
)

// agentContractMux is the production route table for the agent contract: the
// tests drive the same registration function main.go calls, so a route that is
// documented but never wired shows up here rather than in production.
func agentContractMux(a *API) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterRunnerContractRoutes(mux, a)
	return mux
}

// The manifest entry is the runner's half of the discovery contract (spec §1):
// name, contract version, capabilities and the three derived URLs.
func TestRunnerManifestShape(t *testing.T) {
	e := RunnerManifest("https://runner.example.test/")

	if e.Name != "blerg-runner" {
		t.Errorf("name = %q, want blerg-runner", e.Name)
	}
	if e.ContractVersion != agentsmanifest.ContractVersion {
		t.Errorf("contract_version = %q, want %q", e.ContractVersion, agentsmanifest.ContractVersion)
	}
	if e.Description == "" {
		t.Error("description is empty: the manifest is documentation")
	}
	// The trailing slash of the base URL must not survive into the entry.
	if e.BaseURL != "https://runner.example.test" {
		t.Errorf("base_url = %q, want the base URL without a trailing slash", e.BaseURL)
	}
	for _, c := range []struct{ got, want string }{
		{e.DocsURL, "https://runner.example.test/agents"},
		{e.OpenAPIURL, "https://runner.example.test/openapi.json"},
		{e.MCPURL, "https://runner.example.test/mcp"},
	} {
		if c.got != c.want {
			t.Errorf("derived URL = %q, want %q", c.got, c.want)
		}
	}
	wantCaps := map[string]bool{"runner": false, "sessions": false, "mcp": false}
	for _, c := range e.Capabilities {
		if _, ok := wantCaps[c]; !ok {
			t.Errorf("unexpected capability %q", c)
			continue
		}
		wantCaps[c] = true
	}
	for c, seen := range wantCaps {
		if !seen {
			t.Errorf("capability %q missing", c)
		}
	}
	if e.Auth == nil {
		t.Fatal("auth is nil: an agent cannot discover how to authenticate")
	}
	if e.Auth.Audience != coreAuthAudience {
		t.Errorf("auth.audience = %q, want %q", e.Auth.Audience, coreAuthAudience)
	}
	if len(e.Auth.Presets) != 1 || e.Auth.Presets[0] != "run-sessions" {
		t.Errorf("auth.presets = %v, want [run-sessions]", e.Auth.Presets)
	}
	if len(e.Auth.Accepts) != 2 || e.Auth.Accepts[0] != "agent_token" || e.Auth.Accepts[1] != "runner_key" {
		t.Errorf("auth.accepts = %v, want [agent_token runner_key]", e.Auth.Accepts)
	}
	// Components never state core's token endpoint — only core knows its own
	// public origin, and it overwrites whatever a component sends.
	if e.Auth.TokenEndpoint != "" {
		t.Errorf("auth.token_endpoint = %q, want empty (core fills it in)", e.Auth.TokenEndpoint)
	}
}

// Every operation the runner advertises must name a route the session contract
// actually describes, and the nine of §3 must all be there.
func TestRunnerManifestOperations(t *testing.T) {
	got := map[string]string{}
	for _, op := range RunnerManifest("https://runner.example.test").Operations {
		if op.Summary == "" {
			t.Errorf("operation %s has no summary", op.Name)
		}
		if op.Cap != coreAuthRunnerCap {
			t.Errorf("operation %s cap = %q, want %q", op.Name, op.Cap, coreAuthRunnerCap)
		}
		got[op.Name] = op.Method + " " + op.Path
	}
	want := map[string]string{
		"start":         "POST /api/runner/start",
		"status":        "GET /api/runner/sessions/{id}",
		"send_message":  "POST /api/runner/sessions/{id}/message",
		"interrupt":     "POST /api/runner/sessions/{id}/interrupt",
		"stop":          "POST /api/runner/sessions/{id}/stop",
		"events":        "GET /api/runner/sessions/{id}/events",
		"events_stream": "GET /api/runner/sessions/{id}/events/stream",
		"events_live":   "GET /api/runner/sessions/{id}/events/live",
		"result":        "GET /api/runner/sessions/{id}/result",
		"files_list":    "GET /api/runner/sessions/{id}/artifacts",
		"file_raw":      "GET /api/runner/sessions/{id}/artifacts/{aid}/raw",
		"file_download": "GET /api/runner/sessions/{id}/artifacts/{aid}/download",
		"file_delete":   "DELETE /api/runner/sessions/{id}/artifacts/{aid}",
		"upload":        "POST /api/runner/sessions/{id}/uploads",
		"me":            "GET /api/runner/me",
	}
	for name, route := range want {
		if got[name] != route {
			t.Errorf("operation %q = %q, want %q", name, got[name], route)
		}
	}
	if len(got) != len(want) {
		t.Errorf("operations = %v, want exactly %d entries", got, len(want))
	}
}

// GET /agents is public (an agent must be able to read how to get a credential
// before it has one) and serves the runner's own entry as JSON.
func TestAgentsEndpointJSON(t *testing.T) {
	// No runner key configured: discovery still answers. The runner key gates
	// the session contract, not its documentation.
	srv := httptest.NewServer(agentContractMux(&API{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/agents")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /agents = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// The body is JSON or Markdown depending on Accept, so a shared cache must
	// be told not to serve one for the other.
	if got := resp.Header.Get("Vary"); got != "Accept" {
		t.Errorf("Vary = %q, want Accept", got)
	}
	var e agentsmanifest.ComponentEntry
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Name != "blerg-runner" {
		t.Errorf("name = %q, want blerg-runner", e.Name)
	}
	// The base URL is the request's own origin, so the document is correct on
	// every install without configuration.
	if e.BaseURL != srv.URL {
		t.Errorf("base_url = %q, want %q", e.BaseURL, srv.URL)
	}
	if e.DocsURL != srv.URL+"/agents" {
		t.Errorf("docs_url = %q, want %q", e.DocsURL, srv.URL+"/agents")
	}
	if len(e.Operations) == 0 {
		t.Error("operations is empty")
	}
}

// The same document in Markdown, for an LLM reading it directly.
func TestAgentsEndpointMarkdown(t *testing.T) {
	srv := httptest.NewServer(agentContractMux(&API{}))
	defer srv.Close()

	for _, c := range []struct{ name, url, accept string }{
		{"accept header", srv.URL + "/agents", "text/markdown"},
		{"format query", srv.URL + "/agents?format=md", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, c.url, nil)
			if c.accept != "" {
				req.Header.Set("Accept", c.accept)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
				t.Errorf("Content-Type = %q, want text/markdown", ct)
			}
			body, _ := io.ReadAll(resp.Body)
			md := string(body)
			if !strings.Contains(md, "# blerg-runner") {
				t.Errorf("markdown has no blerg-runner heading:\n%s", md)
			}
			if !strings.Contains(md, "/api/runner/start") {
				t.Errorf("markdown does not document the start operation:\n%s", md)
			}
		})
	}
}

// GET /openapi.json is public and serves the embedded document verbatim.
func TestRunnerOpenAPIEndpointIsPublic(t *testing.T) {
	srv := httptest.NewServer(agentContractMux(&API{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /openapi.json = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("served body is not valid JSON: %v", err)
	}
}

// authRunner reports WHO the caller is, not just that they are allowed: the
// static key is its own principal kind with no subject to attribute work to.
func TestAuthRunnerPrincipalForStaticKey(t *testing.T) {
	a := &API{runnerKey: "static-runner-key"}
	req := httptest.NewRequest(http.MethodGet, "/api/runner/me", nil)
	req.Header.Set("Authorization", "Bearer static-runner-key")
	w := httptest.NewRecorder()

	p, ok := a.authRunner(w, req)
	if !ok {
		t.Fatal("authRunner() = false, want true for the static runner key")
	}
	if p.Kind != runnerKeyPrincipalKind {
		t.Errorf("kind = %q, want %q", p.Kind, runnerKeyPrincipalKind)
	}
	if p.Sub != "" || p.OnBehalfOf != "" || len(p.Caps) != 0 || p.ExpiresAt != 0 {
		t.Errorf("static key principal carries claims it cannot have: %+v", p)
	}
}

// A core-minted token's principal carries the claims a session is attributed
// to — sub, on_behalf_of, caps — so later tasks can run the session as its owner.
func TestAuthRunnerPrincipalForCoreToken(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &API{runnerKey: "static-runner-key", coreAuth: newTestCoreAuthClient(t, pub, "core-1")}

	exp := time.Now().Unix() + 600
	tok := mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "token-1", Aud: coreAuthAudience, Kind: "agent", OnBehalfOf: "account-1",
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: exp,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/runner/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()

	p, ok := a.authRunner(w, req)
	if !ok {
		t.Fatalf("authRunner() = false, want true (status %d)", w.Code)
	}
	if p.Kind != "agent" || p.Sub != "token-1" || p.OnBehalfOf != "account-1" || p.Aud != coreAuthAudience {
		t.Errorf("principal = %+v, want the token's own claims", p)
	}
	if len(p.Caps) != 1 || p.Caps[0] != coreAuthRunnerCap {
		t.Errorf("caps = %v, want [%s]", p.Caps, coreAuthRunnerCap)
	}
	if p.ExpiresAt != exp {
		t.Errorf("expires_at = %d, want %d", p.ExpiresAt, exp)
	}
}

// GET /api/runner/me is the introspection endpoint of spec §2: it tells a
// caller exactly what its credential is, which is how an agent debugs a 401
// without a token dump.
func TestRunnerMe(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &API{runnerKey: "static-runner-key", coreAuth: newTestCoreAuthClient(t, pub, "core-1")}
	srv := httptest.NewServer(agentContractMux(a))
	defer srv.Close()

	exp := time.Now().Unix() + 600
	tok := mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "token-1", Aud: coreAuthAudience, Kind: "agent", OnBehalfOf: "account-1",
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: exp,
	})

	get := func(t *testing.T, bearer string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/runner/me", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	t.Run("static key", func(t *testing.T) {
		code, body := get(t, "static-runner-key")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if body["kind"] != runnerKeyPrincipalKind {
			t.Errorf("kind = %v, want %q", body["kind"], runnerKeyPrincipalKind)
		}
		// The key has no subject, account or expiry — and must not invent one.
		if len(body) != 1 {
			t.Errorf("body = %v, want only {kind}", body)
		}
	})

	t.Run("agent token", func(t *testing.T) {
		code, body := get(t, tok)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if body["kind"] != "agent" || body["sub"] != "token-1" || body["on_behalf_of"] != "account-1" {
			t.Errorf("body = %v, want the token's claims", body)
		}
		if body["aud"] != coreAuthAudience {
			t.Errorf("aud = %v, want %q", body["aud"], coreAuthAudience)
		}
		caps, _ := body["caps"].([]any)
		if len(caps) != 1 || caps[0] != coreAuthRunnerCap {
			t.Errorf("caps = %v, want [%s]", body["caps"], coreAuthRunnerCap)
		}
		if got, want := body["expires_at"], float64(exp); got != want {
			t.Errorf("expires_at = %v, want %v", got, want)
		}
		// Never the credential itself.
		raw, _ := json.Marshal(body)
		if strings.Contains(string(raw), tok) {
			t.Error("/api/runner/me echoed the bearer token back")
		}
	})

	t.Run("unauthenticated", func(t *testing.T) {
		if code, _ := get(t, "wrong"); code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", code)
		}
	})
}

// Registration is how core learns the runner exists: it must carry the whole
// manifest entry (operations and auth included), not just a name and a base URL,
// or core's aggregate /agents cannot document the runner at all.
func TestCoreRegistrationSendsFullManifestEntry(t *testing.T) {
	var got agentsmanifest.ComponentEntry
	var authz string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/components" {
			t.Errorf("registered at %q, want /components", r.URL.Path)
		}
		authz = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode registration body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer core.Close()

	body := RunnerManifest("http://runner.internal:8080")
	body.Version = "1.2.3"
	if err := registerOnce(t.Context(), core.Client(), core.URL, "register-key", body); err != nil {
		t.Fatalf("registerOnce: %v", err)
	}

	if authz != "Bearer register-key" {
		t.Errorf("Authorization = %q, want the register key", authz)
	}
	if got.Name != "blerg-runner" || got.BaseURL != "http://runner.internal:8080" {
		t.Errorf("name/base_url = %q/%q", got.Name, got.BaseURL)
	}
	if got.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", got.Version)
	}
	if got.ContractVersion != agentsmanifest.ContractVersion {
		t.Errorf("contract_version = %q, want %q", got.ContractVersion, agentsmanifest.ContractVersion)
	}
	if len(got.Operations) != len(RunnerManifest("x").Operations) {
		t.Errorf("operations = %d, want the full table", len(got.Operations))
	}
	if got.Auth == nil || got.Auth.Audience != coreAuthAudience {
		t.Errorf("auth = %+v, want audience %q", got.Auth, coreAuthAudience)
	}
	if got.OpenAPIURL == "" || got.MCPURL == "" || got.DocsURL == "" {
		t.Errorf("derived URLs missing: %+v", got)
	}
}
