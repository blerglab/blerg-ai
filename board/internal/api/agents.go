package api

// Board's half of the agent contract (spec §1 discovery): a public GET /agents
// manifest and a public GET /openapi.json description of the board REST surface
// an agent drives — the same operations board's MCP tools wrap, so a caller can
// choose either transport from one document.

import (
	_ "embed"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
)

// boardOpenAPIDoc is board's hand-maintained OpenAPI 3.1 document, served
// verbatim at GET /openapi.json. It is embedded rather than generated: the
// public agent-facing surface is a small subset of board's routes, and
// openapi_test.go asserts it agrees with both the manifest and the route table,
// so a hand-written document cannot silently drift from the server.
//
//go:embed openapi.json
var boardOpenAPIDoc []byte

// componentVersion is the build version board reports in its manifest. main.go
// sets it from the same ldflags-stamped string it gives core; it is an atomic
// package value because /agents may be served concurrently with (and before)
// the registration goroutine that also publishes it.
var componentVersion atomic.Value

// SetComponentVersion records board's build version for its manifest entry.
func SetComponentVersion(v string) {
	if v != "" {
		componentVersion.Store(v)
	}
}

func componentVersionString() string {
	if v, ok := componentVersion.Load().(string); ok {
		return v
	}
	return "dev"
}

// boardAudience is the token "aud" board verifies — the audience an agent token
// must be minted for to be usable here.
const boardAudience = "blerg-board"

// The capabilities board's authz enforces on the documented routes. They are
// named here as the same strings RequireBoard/RequireAdmin check (see
// authz_member_test.go), so the manifest documents the real requirement rather
// than a guess.
const (
	capCardRead    = "card.read"
	capCardWrite   = "card.write"
	capColumnWrite = "column.write"
	capBoardAdmin  = "board.admin"
)

// boardOperations is the documented operation table for board's manifest entry:
// the public REST routes board's MCP tools (internal/mcp/tools.go) wrap, so an
// agent that prefers plain HTTP has the same surface as one that speaks MCP.
// It is the single source of truth openapi_test.go checks openapi.json against,
// so adding a documented route means adding it here and in openapi.json
// together, or the test fails.
//
// Deliberately a subset of api.Routes: the browser transport (WebSocket, diffs,
// metrics, icons), the operator surface (tokens, deployments, board deletion)
// and the runner/session-driving routes are board's own UI and control plane,
// not the card-writing contract an agent is pointed at.
var boardOperations = []agentsmanifest.Operation{
	// /onboard is board's conventions document (what a good card looks like,
	// how the admission gate answers). It is listed because an agent is
	// expected to read it BEFORE its first write, and it needs no credential.
	{Name: "onboard", Method: "GET", Path: "/onboard", Idempotent: true,
		Summary: "Board conventions for agents: how to file, claim and move cards. Read it before your first write."},

	{Name: "list_boards", Method: "GET", Path: "/api/boards", Cap: capCardRead, Idempotent: true,
		Summary: "List the boards this credential can see; a board-scoped token sees only its own."},
	{Name: "get_board", Method: "GET", Path: "/api/boards/{id}", Cap: capCardRead, Idempotent: true,
		Summary: "One board: name, repos, gate policy, columns and field schema."},
	{Name: "create_board", Method: "POST", Path: "/api/boards", Cap: capBoardAdmin,
		Summary: "Create a board, optionally with repos, initial columns and a field schema."},
	{Name: "update_board", Method: "PATCH", Path: "/api/boards/{id}", Cap: capBoardAdmin,
		Summary: "Update board settings, repos, models or field schema."},
	{Name: "get_schema", Method: "GET", Path: "/api/boards/{id}/schema", Cap: capCardRead, Idempotent: true,
		Summary: "The board's declared custom fields; read it before writing any `fields`."},

	{Name: "list_columns", Method: "GET", Path: "/api/boards/{id}/columns", Cap: capCardRead, Idempotent: true,
		Summary: "The board's columns in order."},
	{Name: "create_column", Method: "POST", Path: "/api/boards/{id}/columns", Cap: capColumnWrite,
		Summary: "Append a column to the board."},
	{Name: "move_column", Method: "POST", Path: "/api/columns/{id}/move", Cap: capColumnWrite,
		Summary: "Reposition a column among its board's siblings."},

	{Name: "search_cards", Method: "GET", Path: "/api/boards/{id}/cards/search", Cap: capCardRead, Idempotent: true,
		Summary: "Search a board's cards by text and filters. Always search before filing: the admission gate denies duplicates."},
	{Name: "create_card", Method: "POST", Path: "/api/boards/{id}/cards", Cap: capCardWrite,
		Summary: "File a card. Agent writes pass the admission gate, which may deny (409) or hold (202) them."},
	{Name: "get_card", Method: "GET", Path: "/api/cards/{id}", Cap: capCardRead, Idempotent: true,
		Summary: "One card with repos, tags, links, dependencies and its version."},
	{Name: "update_card", Method: "PATCH", Path: "/api/cards/{id}", Cap: capCardWrite,
		Summary: "Update a card's content, fields, tags or repos; send If-Match with the version you read."},
	// link_card shares update_card's route on purpose: attaching a link is a
	// PATCH of the card's `links` array (blerg_card_link does exactly that), and
	// an agent looking for "how do I attach a PR" should find it in the table
	// rather than having to infer it from the update schema.
	{Name: "link_card", Method: "PATCH", Path: "/api/cards/{id}", Cap: capCardWrite,
		Summary: "Attach a link (session, pr, rcca, doc, url, artifact) by PATCHing {\"add_links\":[{kind,url,label}]}: appended atomically, existing links untouched."},
	{Name: "move_card", Method: "POST", Path: "/api/cards/{id}/move", Cap: capCardWrite,
		Summary: "Move a card to another column, optionally before a given card."},
	{Name: "archive_card", Method: "POST", Path: "/api/cards/{id}/archive", Cap: capCardWrite,
		Summary: "Archive a card: it leaves the board but stays queryable."},
	{Name: "comment_card", Method: "POST", Path: "/api/cards/{id}/comments", Cap: capCardWrite,
		Summary: "Post a progress comment on a card — the engineer's log, short and frequent."},

	{Name: "get_review", Method: "GET", Path: "/api/reviews/{id}", Cap: capCardRead, Idempotent: true,
		Summary: "An admission review by id: the outcome of a held or denied write."},
}

// BoardManifest builds board's ComponentEntry for the given public base URL:
// the document served at GET /agents and the body sent to core's
// POST /components.
//
// LastSeen, Stale and Auth.TokenEndpoint are deliberately left unset — they are
// core's to fill in. Nothing here is a secret: public URLs, capability names and
// the documentation of endpoints that each enforce their own auth.
func BoardManifest(baseURL string) agentsmanifest.ComponentEntry {
	base := strings.TrimRight(baseURL, "/")
	return agentsmanifest.ComponentEntry{
		Name:            "blerg-board",
		BaseURL:         base,
		Version:         componentVersionString(),
		ContractVersion: agentsmanifest.ContractVersion,
		Capabilities:    []string{"board", "mcp"},
		Description:     "Kanban board where agents draft cards and humans curate the work",
		DocsURL:         base + "/agents",
		OpenAPIURL:      base + "/openapi.json",
		MCPURL:          base + "/mcp",
		Auth: &agentsmanifest.AuthInfo{
			Audience: boardAudience,
			Presets:  []string{"board"},
			// Both: a core-minted agent token, and a signed-in human's session
			// (the same routes back board's own web UI).
			Accepts: []string{"agent_token", "human_session"},
		},
		Operations: boardOperations,
	}
}

// boardOrigin is the public origin of this request — how a caller reached
// board, so the URLs in the manifest are the URLs that work for them. Behind
// the ingress the scheme arrives in X-Forwarded-Proto, as elsewhere in this
// package (see checkWSOrigin).
func boardOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// HandleAgents is GET /agents: board's manifest entry, as JSON or (with
// Accept: text/markdown, or ?format=md) as the Markdown agent guide.
//
// Deliberately unauthenticated (spec §1): an agent must be able to read how to
// get a credential BEFORE it has one, so gating discovery behind a credential
// would make the contract unbootstrappable. The document holds no secrets.
func HandleAgents(w http.ResponseWriter, r *http.Request) {
	// The body depends on Accept (JSON or Markdown): a shared cache must not
	// serve one to a client that asked for the other.
	w.Header().Set("Vary", "Accept")
	entry := BoardManifest(boardOrigin(r))
	if agentsmanifest.WantsMarkdown(r) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		// The origin in the document echoes the Host header; nosniff keeps a
		// browser from ever treating the Markdown as HTML.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(agentsmanifest.RenderComponentMarkdown(entry))) //nolint:gosec // G705: served as text/markdown with nosniff, so the echoed Host header is never rendered as HTML
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// handleOpenAPI is GET /openapi.json — the embedded document, unauthenticated
// for the same reason /agents is: it is documentation of a contract, not access
// to it.
//
// Unexported, like core's and the runner's: every component's contract routes
// are wired by its own register function (below), so nothing outside the
// package has any business naming the handler.
func handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(boardOpenAPIDoc)
}

// RegisterBoardContractRoutes wires the discovery half of the contract. The
// operations themselves are registered by Routes; main.go calls both, and
// openapi_test.go drives the same two functions — a documented route that is
// never registered fails the build's tests rather than an agent's call.
//
// The API receiver is unused today (both handlers are pure documentation), but
// it keeps the signature the same shape as the other route registrars so a
// future contract route that needs board state does not change every caller.
func RegisterBoardContractRoutes(mux *http.ServeMux, _ *API) {
	mux.HandleFunc("GET /agents", HandleAgents)
	mux.HandleFunc("GET /openapi.json", handleOpenAPI)
}
