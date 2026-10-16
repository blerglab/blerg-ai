package server

import "net/http"

// RegisterToolChangeRoutes registers the human-only tool-change review routes
// (docs/design/mcp-tool-changes.md §5) and the pin-event audit routes on mux.
func (a *API) RegisterToolChangeRoutes(mux *http.ServeMux) {
	h := &toolChangesHandler{p: &proposalsHandler{api: a}}
	mux.HandleFunc("GET /api/tool-changes", h.list)
	mux.HandleFunc("GET /api/tool-changes/{id}", h.get)
	mux.HandleFunc("POST /api/tool-changes/{id}/accept", h.accept)
	mux.HandleFunc("POST /api/tool-changes/{id}/decline", h.decline)
	mux.HandleFunc("GET /api/sessions/{id}/pin-events", h.sessionEvents)
	mux.HandleFunc("GET /api/mcp/connections/{id}/pin-events", h.connectionEvents)
}
