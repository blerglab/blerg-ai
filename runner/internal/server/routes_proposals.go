package server

import "net/http"

// RegisterProposalRoutes registers the human-only proposals routes (spec 9) on mux: list, read,
// approve, reject, and resolve for a proposal whose outcome was unknown. Every handler
// authenticates the caller itself (a signed-in person, owner only).
func (a *API) RegisterProposalRoutes(mux *http.ServeMux) {
	h := &proposalsHandler{api: a}
	mux.HandleFunc("GET /api/proposals", h.list)
	mux.HandleFunc("GET /api/proposals/count", h.count)
	mux.HandleFunc("GET /api/proposals/{id}", h.get)
	mux.HandleFunc("POST /api/proposals/{id}/approve", h.approve)
	mux.HandleFunc("POST /api/proposals/{id}/reject", h.reject)
	mux.HandleFunc("POST /api/proposals/{id}/resolve", h.resolve)
}
