package server

// Session files on the runner contract (A in the chat-package design): the credential that may
// drive a session may also handle its files, so an app built on the contract can show the same
// Files panel the runner's own browser shows.
//
//	GET    /api/runner/sessions/{id}/artifacts                 the session's files, newest first
//	GET    /api/runner/sessions/{id}/artifacts/{aid}/raw       the bytes for an in-app viewer
//	GET    /api/runner/sessions/{id}/artifacts/{aid}/download  the bytes as an attachment
//	DELETE /api/runner/sessions/{id}/artifacts/{aid}           remove one (any origin)
//	POST   /api/runner/sessions/{id}/uploads                   attach a person's file (raw body)
//
// Every route is authRunner + requireSessionAccess, exactly like send_message: an agent token
// reaches only the sessions its owner started, a private session only its owner, and anything
// else is one uniform 404 — never a listing. The bodies are the browser routes' own
// (artifacts.go, uploads.go), so the caps, the server-chosen content type and the hardening
// headers cannot differ between the two surfaces.

import (
	"net/http"
)

// runnerSessionFiles authenticates a runner-contract files request and resolves the session it
// names. On failure it has written the response (404 when the contract is off or the session is
// unknown, malformed or not this principal's; 401 otherwise) and returns false.
func (a *API) runnerSessionFiles(w http.ResponseWriter, r *http.Request) (runnerPrincipal, string, bool) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return runnerPrincipal{}, "", false
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return runnerPrincipal{}, "", false
	}
	// The id becomes a directory name below, so only a canonical uuid gets near a path; the
	// answer is the same 404 requireSessionAccess gives an unknown one.
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "session not found")
		return runnerPrincipal{}, "", false
	}
	if _, apiErr := a.requireSessionAccess(r.Context(), principal, sid); apiErr != nil {
		writeAPIError(w, apiErr)
		return runnerPrincipal{}, "", false
	}
	return principal, sid, true
}

// HandleRunnerFilesList is GET /api/runner/sessions/{id}/artifacts.
func (a *API) HandleRunnerFilesList(w http.ResponseWriter, r *http.Request) {
	if _, sid, ok := a.runnerSessionFiles(w, r); ok {
		(&artifactsHandler{api: a}).listSession(w, r, sid)
	}
}

// HandleRunnerFileRaw is GET /api/runner/sessions/{id}/artifacts/{aid}/raw.
func (a *API) HandleRunnerFileRaw(w http.ResponseWriter, r *http.Request) {
	if _, sid, ok := a.runnerSessionFiles(w, r); ok {
		(&artifactsHandler{api: a}).serveSession(w, r, sid, true)
	}
}

// HandleRunnerFileDownload is GET /api/runner/sessions/{id}/artifacts/{aid}/download.
func (a *API) HandleRunnerFileDownload(w http.ResponseWriter, r *http.Request) {
	if _, sid, ok := a.runnerSessionFiles(w, r); ok {
		(&artifactsHandler{api: a}).serveSession(w, r, sid, false)
	}
}

// HandleRunnerFileDelete is DELETE /api/runner/sessions/{id}/artifacts/{aid}: a file is deleted
// by whoever may see the session, whichever origin it came from.
func (a *API) HandleRunnerFileDelete(w http.ResponseWriter, r *http.Request) {
	if _, sid, ok := a.runnerSessionFiles(w, r); ok {
		(&artifactsHandler{api: a}).removeSession(w, r, sid)
	}
}

// HandleRunnerUpload is POST /api/runner/sessions/{id}/uploads: a person's attachment, origin
// user, under the browser route's caps and name header, attributed to the account the credential
// acts for (none for the operator key).
func (a *API) HandleRunnerUpload(w http.ResponseWriter, r *http.Request) {
	if principal, sid, ok := a.runnerSessionFiles(w, r); ok {
		(&artifactsHandler{api: a}).userUploadSession(w, r, sid, principal.spawningAccountID())
	}
}
