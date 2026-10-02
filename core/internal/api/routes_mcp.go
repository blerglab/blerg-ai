package api

import "net/http"

// registerMCPRoutes owns the registration of the MCP connection routes (public and internal).
//
// The public routes are human-only (requireHumanPrincipal with "card.read", like /api/tokens and
// /api/credentials): an agent token must never be able to add or read a connection, because a
// connection is a standing credential to a third-party server. The write handlers are
// same-origin checked themselves. Nothing under /internal/ sets CORS headers.
func (d Deps) registerMCPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mcp/connections", requireHumanPrincipal(d.handleListMCPConnections, d, "card.read"))
	mux.HandleFunc("POST /api/mcp/connections", requireHumanPrincipal(d.handleCreateMCPConnection, d, "card.read"))
	mux.HandleFunc("PATCH /api/mcp/connections/{id}", requireHumanPrincipal(d.handlePatchMCPConnection, d, "card.read"))
	mux.HandleFunc("DELETE /api/mcp/connections/{id}", requireHumanPrincipal(d.handleDeleteMCPConnection, d, "card.read"))
	// OAuth connections (mcp_oauth_handlers.go). The callback is reached by the browser coming
	// back from the authorization server: no bearer token, protected by the single-use state row
	// and the Lax state cookie.
	mux.HandleFunc("POST /api/mcp/connections/oauth/start", requireHumanPrincipal(d.handleStartMCPOAuth, d, "card.read"))
	mux.HandleFunc("POST /api/mcp/connections/{id}/reconnect", requireHumanPrincipal(d.handleReconnectMCPConnection, d, "card.read"))
	mux.HandleFunc("GET "+mcpOAuthCallback, d.handleMCPOAuthCallback)

	mux.HandleFunc("POST /internal/mcp/connections/list", d.handleInternalListMCPConnections)
	mux.HandleFunc("POST /internal/mcp/connections/token", d.handleInternalMCPToken)
	mux.HandleFunc("POST /internal/mcp/connections/defaults", d.handleInternalMCPDefaults)
}
