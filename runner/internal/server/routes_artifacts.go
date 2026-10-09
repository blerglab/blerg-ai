package server

import (
	"context"
	"net/http"
)

// RegisterArtifactRoutes registers the session-artifact routes (artifacts.go) on mux and starts
// the prune of orphaned artifact directories (once now, then hourly, for the life of the process).
func (a *API) RegisterArtifactRoutes(mux *http.ServeMux) {
	a.registerArtifactRoutes(mux)
	go a.runArtifactPrune(context.Background())
}

func (a *API) registerArtifactRoutes(mux *http.ServeMux) {
	h := &artifactsHandler{api: a}
	mux.HandleFunc("POST /api/sessions/{id}/artifacts", h.upload)
	mux.HandleFunc("GET /api/sessions/{id}/artifacts", h.list)
	mux.HandleFunc("GET /api/sessions/{id}/artifacts/{aid}/download", h.download)
	mux.HandleFunc("GET /api/sessions/{id}/artifacts/{aid}/raw", h.raw)
	mux.HandleFunc("DELETE /api/sessions/{id}/artifacts/{aid}", h.remove)
	mux.HandleFunc("POST /api/sessions/{id}/uploads", h.userUpload)
	mux.HandleFunc("GET /api/sessions/{id}/attachments", h.attachments)
	mux.HandleFunc("GET /api/sessions/{id}/attachments/{aid}/file", h.attachmentFile)
	mux.HandleFunc("GET /api/sessions/{id}/files", h.files)
	mux.HandleFunc("DELETE /api/sessions/{id}/files/{aid}", h.unpublish)
}
