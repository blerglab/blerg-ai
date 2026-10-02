package server

// Files a person attaches to a session from the chat box (the reverse of artifacts.go). They are
// stored exactly like artifacts (same directory, sanitising, size cap and server-chosen type) but
// with origin "user", and the session's agent fetches them with `blerg-runner fetch`:
//
//	POST /api/sessions/{id}/uploads                       a person who can see the session: raw body
//	GET  /api/sessions/{id}/attachments                   the agent, its per-session token: the list
//	GET  /api/sessions/{id}/attachments/{aid}/file        the agent: the bytes (octet-stream)
//
// People list, view and delete them through the artifact routes (their list carries `origin`).
// The bytes are untrusted: they are only ever served as application/octet-stream + nosniff.

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Limits on what people attach, separate from (and in addition to) the agent's artifacts.
var (
	maxUploadsPerSession           = 20
	maxUploadBytesPerSession int64 = 200 << 20
)

func (h *artifactsHandler) userUpload(w http.ResponseWriter, r *http.Request) {
	sid, account, ok := h.personAccount(w, r)
	if !ok {
		return
	}
	info := h.store(w, r, sid, artifactIntake{
		origin: db.OriginUser, uploadedBy: account, noun: "uploaded files",
		maxFiles: maxUploadsPerSession, maxTotal: maxUploadBytesPerSession,
	})
	if info == nil {
		return
	}
	// No agent event: the chat message the person sends carries the information.
	writeJSON(w, http.StatusCreated, info)
}

// agentSession authenticates the agent side: its per-session token (or the daemon token) for this very
// session, exactly like the artifact POST.
func (h *artifactsHandler) agentSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	a := h.api
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return "", false
	}
	tokenSession, ok := a.authMessaging(w, r)
	if !ok {
		return "", false
	}
	sid := r.PathValue("id")
	if tokenSession != "" && tokenSession != sid {
		writeError(w, http.StatusForbidden, "forbidden")
		return "", false
	}
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "not found")
		return "", false
	}
	return sid, true
}

func (h *artifactsHandler) attachments(w http.ResponseWriter, r *http.Request) {
	sid, ok := h.agentSession(w, r)
	if !ok {
		return
	}
	sess, err := db.GetSession(r.Context(), h.api.dbPool, sid)
	if err != nil {
		log.Printf("attachments GetSession: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	rows, err := db.ListArtifacts(r.Context(), h.api.dbPool, sid)
	if err != nil {
		log.Printf("attachments list: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	type entry struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Size        int64  `json:"size"`
		ContentType string `json:"content_type"`
		Version     int    `json:"version"`
		Latest      int    `json:"latest_version"`
	}
	latest := latestVersions(rows)
	out := []entry{}
	for i := len(rows) - 1; i >= 0; i-- { // oldest first: the order the person attached them
		if rows[i].Origin == db.OriginUser {
			out = append(out, entry{rows[i].ID, rows[i].Name, rows[i].Size, rows[i].ContentType, rows[i].Version, latest[latestKey(rows[i])]})
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"attachments": out})
}

func (h *artifactsHandler) attachmentFile(w http.ResponseWriter, r *http.Request) {
	sid, ok := h.agentSession(w, r)
	if !ok {
		return
	}
	row := h.artifact(w, r, sid)
	if row == nil {
		return
	}
	if row.Origin != db.OriginUser {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	f, err := os.Open(filepath.Join(artifactDir(sid, row.ID), row.Name))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer func() { _ = f.Close() }()
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "sandbox")
	hd.Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", time.Time{}, f)
}
