package api

import (
	_ "embed"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
	"github.com/blerglab/blerg-ai/core/internal/landing"
)

// openAPIDoc is core's hand-maintained OpenAPI 3.1 document, served verbatim at
// GET /openapi.json. It is embedded rather than generated: the operations core exposes are
// few and stable, and openapi_test.go asserts it agrees with both the manifest and the mux,
// so a hand-written document cannot silently drift from the server.
//
//go:embed openapi.json
var openAPIDoc []byte

// quickstartTemplate is spec §1's seven-step quickstart, rendered into the aggregate manifest
// (and, through it, into the Markdown agent guide). "{{core}}" is replaced with the request's
// public origin so a reader can paste the URLs as-is; the other angle-bracket placeholders
// (<runner>, <token>, <session_id>) are genuinely for the reader to fill in.
const quickstartTemplate = "1. Read this document (`GET /agents`, `Accept: text/markdown`).\n" +
	"2. Get a token: sign in at `{{core}}/settings` → Agent tokens → create with preset " +
	"`run-sessions` (or `POST /api/tokens` with a human session). Send it as " +
	"`Authorization: Bearer <token>` to the component whose audience it was minted for.\n" +
	"3. Find the runner: `components[].name == \"blerg-runner\"` → `base_url`.\n" +
	"4. Start a session: `POST <runner>/api/runner/start` with `Idempotency-Key` and a `prompt` " +
	"(and optionally `callback_url`; pass `auto_stop: true` for a one-shot task). You get a `session_id`.\n" +
	"5. Follow it: `GET …/events?after_seq=` (poll), `GET …/events/stream` (SSE), or wait for the webhook.\n" +
	"6. Collect: `GET …/result` → `terminal`, `branch` (`wip/<session_id>` on the repo's remote), " +
	"`last_assistant_message`.\n" +
	"7. Prefer MCP? `POST <runner>/mcp` with the same bearer token exposes the same operations as tools.\n"

// coreOperations is the documented operation table for core's own manifest entry. It is the
// single source of truth openapi_test.go checks openapi.json against, so adding a public core
// route means adding it here and in openapi.json together or the test fails.
var coreOperations = []agentsmanifest.Operation{
	{Name: "create_token", Method: "POST", Path: "/api/tokens", Summary: "Mint an agent token; the token value is returned exactly once.", Cap: "card.read"},
	{Name: "list_tokens", Method: "GET", Path: "/api/tokens", Summary: "List the caller's agent tokens (never the token value).", Cap: "card.read", Idempotent: true},
	{Name: "revoke_token", Method: "DELETE", Path: "/api/tokens/{id}", Summary: "Revoke one of the caller's agent tokens.", Cap: "card.read", Idempotent: true},
	{Name: "me", Method: "GET", Path: "/api/me", Summary: "Who the caller is: account, provider, role.", Idempotent: true},
	{Name: "list_credentials", Method: "GET", Path: "/api/credentials", Summary: "List the caller's stored engine credentials (metadata only).", Cap: "card.read", Idempotent: true},
	{Name: "agents", Method: "GET", Path: "/agents", Summary: "This manifest: every component on the install, with auth and operations.", Idempotent: true},
	{Name: "openapi", Method: "GET", Path: "/openapi.json", Summary: "OpenAPI 3.1 description of core's public contract.", Idempotent: true},
}

// coreEntry is the synthetic manifest entry for core itself. Core does not register with its
// own discovery registry (registration is the bootstrap path for OTHER components), so its
// entry is built per-request from the caller's public origin and listed first: an agent reading
// the guide top-to-bottom meets the thing that issues its credentials before anything that
// consumes them.
func coreEntry(origin string) agentsmanifest.ComponentEntry {
	return agentsmanifest.ComponentEntry{
		Name:            "blerg-core",
		BaseURL:         origin,
		Version:         landing.Version(),
		ContractVersion: agentsmanifest.ContractVersion,
		Capabilities:    []string{},
		LastSeen:        time.Now().Unix(),
		Description:     "Identity, credentials and discovery for this install",
		DocsURL:         origin + "/agents",
		OpenAPIURL:      origin + "/openapi.json",
		Auth: &agentsmanifest.AuthInfo{
			Audience:      "blerg-core",
			TokenEndpoint: origin + "/api/tokens",
			Presets:       []string{"platform"},
			Accepts:       []string{"agent_token", "human_session"},
		},
		Operations: coreOperations,
	}
}

// aggregate builds the public /agents document: core's own entry first, then every registered
// component with the core-filled fields (the absolute token endpoint) applied.
func (d Deps) aggregate(r *http.Request) (agentsmanifest.AggregateManifest, error) {
	origin := d.publicOrigin(r)
	out := agentsmanifest.AggregateManifest{
		ContractVersion: agentsmanifest.ContractVersion,
		CoreBaseURL:     origin,
		Quickstart:      strings.ReplaceAll(quickstartTemplate, "{{core}}", origin),
		Components:      []agentsmanifest.ComponentEntry{coreEntry(origin)},
	}
	if d.Registry == nil {
		return out, nil
	}
	agg, err := d.Registry.Aggregate(r.Context())
	if err != nil {
		return agentsmanifest.AggregateManifest{}, err
	}
	for _, c := range agg.Components {
		// Components never state the token endpoint themselves — only core knows its own
		// public origin — so it is filled in here, overwriting anything a component sent.
		if c.Auth != nil {
			auth := *c.Auth
			auth.TokenEndpoint = origin + "/api/tokens"
			c.Auth = &auth
		}
		out.Components = append(out.Components, c)
	}
	return out, nil
}

// handleAgents is GET /agents: the whole install's agent manifest, as JSON or (with
// Accept: text/markdown, or ?format=md) as the Markdown "getting started" guide.
//
// Deliberately unauthenticated (spec §1). The manifest holds no secrets: public base URLs,
// version strings, capability names and the documentation of endpoints that each enforce their
// own auth. An agent must be able to read how to get a credential BEFORE it has one, so gating
// discovery behind a credential would make the contract unbootstrappable.
func (d Deps) handleAgents(w http.ResponseWriter, r *http.Request) {
	// The body depends on Accept (JSON or Markdown), so a shared cache must not
	// serve one to a client that asked for the other. And it must not serve a
	// stale one at all: the manifest is the live component list — base URLs,
	// versions, staleness — which changes as components come and go.
	w.Header().Set("Vary", "Accept")
	w.Header().Set("Cache-Control", "no-store")
	agg, err := d.aggregate(r)
	if err != nil {
		// The registry error quotes the query and the database's own message;
		// /agents is unauthenticated, so the caller gets the fixed string and
		// the operator gets the detail.
		log.Printf("GET /agents: aggregate: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if agentsmanifest.WantsMarkdown(r) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(agentsmanifest.RenderAggregateMarkdown(agg))) //nolint:gosec // G705: served as text/markdown with nosniff, so a browser never interprets the registry text as HTML
		return
	}
	writeJSON(w, agg)
}

// handleOpenAPI is GET /openapi.json — the embedded document, unauthenticated for the same
// reason /agents is: it is documentation of a contract, not access to it.
func handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPIDoc)
}
