package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
)

// contractMux is board's own route table: the agent-contract routes plus every
// REST route api.Routes registers. The tests drive the same two functions
// main.go calls, so a route that is documented but never wired shows up here
// rather than in production.
//
// Deliberately WITHOUT main.go's "/" SPA fallback: that catch-all answers every
// path, so on the production mux "is this path routed?" is always yes. The
// question worth asking is whether board's API registered it, and this mux is
// exactly that table.
func contractMux(a *API) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterBoardContractRoutes(mux, a)
	a.Routes(mux)
	return mux
}

// The manifest entry is board's half of the discovery contract (spec §1):
// name, contract version, capabilities and the three derived URLs.
func TestBoardManifestShape(t *testing.T) {
	e := BoardManifest("https://board.example.test/")

	if e.Name != "blerg-board" {
		t.Errorf("name = %q, want blerg-board", e.Name)
	}
	if e.ContractVersion != agentsmanifest.ContractVersion {
		t.Errorf("contract_version = %q, want %q", e.ContractVersion, agentsmanifest.ContractVersion)
	}
	if e.Description == "" {
		t.Error("description is empty: the manifest is documentation")
	}
	// The trailing slash of the base URL must not survive into the entry.
	if e.BaseURL != "https://board.example.test" {
		t.Errorf("base_url = %q, want the base URL without a trailing slash", e.BaseURL)
	}
	for _, c := range []struct{ got, want string }{
		{e.DocsURL, "https://board.example.test/agents"},
		{e.OpenAPIURL, "https://board.example.test/openapi.json"},
		{e.MCPURL, "https://board.example.test/mcp"},
	} {
		if c.got != c.want {
			t.Errorf("derived URL = %q, want %q", c.got, c.want)
		}
	}
	wantCaps := map[string]bool{"board": false, "mcp": false}
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
	if e.Auth.Audience != boardAudience {
		t.Errorf("auth.audience = %q, want %q", e.Auth.Audience, boardAudience)
	}
	if len(e.Auth.Presets) != 1 || e.Auth.Presets[0] != "board" {
		t.Errorf("auth.presets = %v, want [board]", e.Auth.Presets)
	}
	if len(e.Auth.Accepts) != 2 || e.Auth.Accepts[0] != "agent_token" || e.Auth.Accepts[1] != "human_session" {
		t.Errorf("auth.accepts = %v, want [agent_token human_session]", e.Auth.Accepts)
	}
	// Components never state core's token endpoint — only core knows its own
	// public origin, and it overwrites whatever a component sends.
	if e.Auth.TokenEndpoint != "" {
		t.Errorf("auth.token_endpoint = %q, want empty (core fills it in)", e.Auth.TokenEndpoint)
	}
}

// The operation table is the contract an agent programs against: every route
// board's MCP tools wrap, with the capability board's authz actually enforces
// for it (see authz_member_test.go for the enforcement side).
func TestBoardManifestOperations(t *testing.T) {
	type route struct{ route, cap string }
	got := map[string]route{}
	for _, op := range BoardManifest("https://board.example.test").Operations {
		if op.Summary == "" {
			t.Errorf("operation %s has no summary", op.Name)
		}
		if _, dup := got[op.Name]; dup {
			t.Errorf("operation %s is listed twice", op.Name)
		}
		got[op.Name] = route{op.Method + " " + op.Path, op.Cap}
	}
	want := map[string]route{
		"onboard":       {"GET /onboard", ""},
		"list_boards":   {"GET /api/boards", "card.read"},
		"get_board":     {"GET /api/boards/{id}", "card.read"},
		"create_board":  {"POST /api/boards", "board.admin"},
		"update_board":  {"PATCH /api/boards/{id}", "board.admin"},
		"get_schema":    {"GET /api/boards/{id}/schema", "card.read"},
		"list_columns":  {"GET /api/boards/{id}/columns", "card.read"},
		"create_column": {"POST /api/boards/{id}/columns", "column.write"},
		"move_column":   {"POST /api/columns/{id}/move", "column.write"},
		"search_cards":  {"GET /api/boards/{id}/cards/search", "card.read"},
		"create_card":   {"POST /api/boards/{id}/cards", "card.write"},
		"get_card":      {"GET /api/cards/{id}", "card.read"},
		"update_card":   {"PATCH /api/cards/{id}", "card.write"},
		"link_card":     {"PATCH /api/cards/{id}", "card.write"},
		"move_card":     {"POST /api/cards/{id}/move", "card.write"},
		"archive_card":  {"POST /api/cards/{id}/archive", "card.write"},
		"comment_card":  {"POST /api/cards/{id}/comments", "card.write"},
		"get_review":    {"GET /api/reviews/{id}", "card.read"},
	}
	for name, r := range want {
		if got[name] != r {
			t.Errorf("operation %q = %+v, want %+v", name, got[name], r)
		}
	}
	if len(got) != len(want) {
		t.Errorf("operations = %d entries, want exactly %d", len(got), len(want))
	}
	// Reads are safe to repeat, writes say so themselves.
	for _, op := range BoardManifest("x").Operations {
		if op.Method == "GET" && !op.Idempotent {
			t.Errorf("operation %s is a GET but not marked idempotent", op.Name)
		}
	}
}

// GET /agents is public (an agent must be able to read how to get a credential
// before it has one) and serves board's own entry as JSON.
func TestBoardAgentsEndpointJSON(t *testing.T) {
	srv := httptest.NewServer(contractMux(&API{}))
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
	if e.Name != "blerg-board" {
		t.Errorf("name = %q, want blerg-board", e.Name)
	}
	// The base URL is the request's own origin, so the document is correct on
	// every install without configuration.
	if e.BaseURL != srv.URL {
		t.Errorf("base_url = %q, want %q", e.BaseURL, srv.URL)
	}
	if e.DocsURL != srv.URL+"/agents" || e.MCPURL != srv.URL+"/mcp" {
		t.Errorf("derived URLs = %q / %q, want them under %q", e.DocsURL, e.MCPURL, srv.URL)
	}
	if len(e.Operations) == 0 {
		t.Error("operations is empty")
	}
}

// The same document in Markdown, for an LLM reading it directly.
func TestBoardAgentsEndpointMarkdown(t *testing.T) {
	srv := httptest.NewServer(contractMux(&API{}))
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
			if !strings.Contains(md, "# blerg-board") {
				t.Errorf("markdown has no blerg-board heading:\n%s", md)
			}
			if !strings.Contains(md, "/api/boards/{id}/cards") {
				t.Errorf("markdown does not document the create-card operation:\n%s", md)
			}
		})
	}
}

// GET /openapi.json is public and serves the embedded document verbatim.
func TestBoardOpenAPIEndpointIsPublic(t *testing.T) {
	srv := httptest.NewServer(contractMux(&API{}))
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

// The manifest carries no credential, board secret or internal address: it is
// public documentation and is served unauthenticated.
func TestBoardManifestCarriesNoSecrets(t *testing.T) {
	SetComponentVersion("1.2.3")
	raw, err := json.Marshal(BoardManifest("https://board.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "password", "bearer ", "token_endpoint"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Errorf("manifest mentions %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(string(raw), `"version":"1.2.3"`) {
		t.Errorf("manifest does not carry the build version set by main: %s", raw)
	}
}
