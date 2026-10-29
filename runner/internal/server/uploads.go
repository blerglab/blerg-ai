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
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The limits on what people attach (maxUploadsPerSession, maxUploadBytesPerSession) are in
// filelimits.go, beside the agent's.

func (h *artifactsHandler) userUpload(w http.ResponseWriter, r *http.Request) {
	if sid, account, ok := h.personAccount(w, r); ok {
		h.userUploadSession(w, r, sid, account)
	}
}

// userUploadSession stores a person's attachment to an authenticated session. account is who it
// is attributed to: the signed-in person on the browser route, the account an agent token acts for
// on the runner contract (runner_files.go), where the app vouches for the person.
type uploadOpt struct{ parentID, parentTitle string }

func uploadFromSession(id, title string) uploadOpt {
	return uploadOpt{parentID: id, parentTitle: title}
}

func (h *artifactsHandler) userUploadSession(w http.ResponseWriter, r *http.Request, sid, account string, opts ...uploadOpt) {
	// The session's own attachment total: the install default, or what an administrator raised
	// it to (session_storage.go).
	limit, _, err := h.api.sessionUploadLimit(r.Context(), sid)
	if err != nil {
		writeDatabaseFailure(w, "read the session's storage", "uploads storage", err)
		return
	}
	info := h.store(w, r, sid, artifactIntake{
		origin: db.OriginUser, uploadedBy: account, noun: "uploaded files",
		maxFiles: maxUploadsPerSession, maxTotal: limit,
	})
	if info == nil {
		return
	}
	// A parent upload is informational; user_message would incorrectly end a live turn.
	if len(opts) > 0 && opts[0].parentID != "" {
		note := opts[0]
		recordServerAgentEvent(r.Context(), h.api.hub, h.api.dbPool, sid, protocol.SessionMessageEventID(info.ID, "session-upload"), "session_upload", map[string]any{
			"name": info.Name, "size": info.Size, "artifact_id": info.ID, "from_session_id": note.parentID, "from_title": note.parentTitle,
		})
	}
	writeJSON(w, http.StatusCreated, info)
}

// agentSession authenticates the agent side: its per-session token (or the daemon token) for this very
// session, exactly like the artifact POST.
func (h *artifactsHandler) agentSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	sid, _, ok := h.agentSessionVia(w, r)
	return sid, ok
}

// agentSessionVia is agentSession plus which credential it was, for the deletion audit:
// "session_token" (the session's own token) or "daemon_token".
func (h *artifactsHandler) agentSessionVia(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	a := h.api
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
		return "", "", false
	}
	tokenSession, ok := a.authMessaging(w, r)
	if !ok {
		return "", "", false
	}
	sid := r.PathValue("id")
	if tokenSession != "" && tokenSession != sid {
		writeError(w, http.StatusForbidden, "forbidden")
		return "", "", false
	}
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "not found")
		return "", "", false
	}
	via := "daemon_token"
	if tokenSession != "" {
		via = "session_token"
	}
	return sid, via, true
}

func (h *artifactsHandler) attachments(w http.ResponseWriter, r *http.Request) {
	sid, ok := h.agentSession(w, r)
	if !ok {
		return
	}
	sess, err := db.GetSession(r.Context(), h.api.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "check the session", "attachments GetSession", err)
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	rows, err := db.ListArtifacts(r.Context(), h.api.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "list the attachments", "attachments list", err)
		return
	}
	type entry struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Size        int64  `json:"size"`
		ContentType string `json:"content_type"`
		Version     int    `json:"version"`
		Latest      int    `json:"latest_version"`
		// When the person attached it: the CLI names same-name files by upload time, whatever
		// the list order.
		CreatedAt string `json:"created_at"`
	}
	latest := latestVersions(rows)
	out := []entry{}
	for i := len(rows) - 1; i >= 0; i-- { // oldest first: the order the person attached them
		if rows[i].Origin == db.OriginUser {
			out = append(out, entry{rows[i].ID, rows[i].Name, rows[i].Size, rows[i].ContentType, rows[i].Version,
				latest[latestKey(rows[i])], rows[i].CreatedAt.UTC().Format(time.RFC3339Nano)})
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
	f, err := os.Open(artifactPath(sid, *row))
	if errors.Is(err, os.ErrNotExist) {
		// Listed, but its bytes are gone (a data directory that did not survive a restart).
		writeError(w, http.StatusGone, fileGoneMsg)
		return
	}
	if err != nil {
		writeStorageFailure(w, "read the file", accessRead, "attachments open "+sid+"/"+row.ID, err)
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
