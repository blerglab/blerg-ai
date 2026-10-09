package server

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// The session's own files, for the agent side (`blerg-runner files` / `unpublish`):
//
//	GET    /api/sessions/{id}/files         the files this session published, newest first,
//	                                        and how many of the session's file slots are used
//	DELETE /api/sessions/{id}/files/{aid}   remove one file this session published
//
// Both take the per-session messaging token (agentSession), like publish and fetch. A session
// may list and delete what it published (origin agent) — never a file a person attached: that
// is theirs to remove, from the Files panel. The person's own routes (artifacts.go) are unchanged.

type sessionFileEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	Version     int    `json:"version"`
	Latest      int    `json:"latest_version"`
	CreatedAt   string `json:"created_at"`
}

func (h *artifactsHandler) files(w http.ResponseWriter, r *http.Request) {
	sid, ok := h.agentSession(w, r)
	if !ok {
		return
	}
	sess, err := db.GetSession(r.Context(), h.api.dbPool, sid)
	if err != nil {
		log.Printf("files GetSession: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	rows, err := db.ListArtifacts(r.Context(), h.api.dbPool, sid)
	if err != nil {
		log.Printf("files list: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	latest := latestVersions(rows)
	out := []sessionFileEntry{}
	for _, a := range rows { // newest first, as ListArtifacts orders them
		if a.Origin != db.OriginAgent {
			continue
		}
		out = append(out, sessionFileEntry{a.ID, a.Name, a.Size, a.ContentType, a.Version, latest[latestKey(a)], a.CreatedAt.UTC().Format(time.RFC3339)})
	}
	w.Header().Set("Cache-Control", "private, no-store")
	// used counts every file of the session, the person's attachments included: that is what
	// the cap is measured against.
	writeJSON(w, http.StatusOK, map[string]any{"files": out, "used": len(rows), "limit": maxArtifactsPerSession})
}

func (h *artifactsHandler) unpublish(w http.ResponseWriter, r *http.Request) {
	sid, ok := h.agentSession(w, r)
	if !ok {
		return
	}
	row := h.artifact(w, r, sid)
	if row == nil {
		return
	}
	if row.Origin != db.OriginAgent {
		writeError(w, http.StatusForbidden, "a person attached this file; only they can remove it, from the Files panel")
		return
	}
	if _, err := db.DeleteArtifact(r.Context(), h.api.dbPool, sid, row.ID); err != nil {
		log.Printf("files delete: %v", err)
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if err := os.RemoveAll(artifactDir(sid, row.ID)); err != nil {
		log.Printf("files delete files %s/%s: %v", sid, row.ID, err)
	}
	w.WriteHeader(http.StatusNoContent)
}
