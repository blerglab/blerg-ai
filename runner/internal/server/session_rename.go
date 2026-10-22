package server

// Renaming a session (0.4.5). Three callers may change a session's title, and each rename is recorded
// in the session's History (a session_renamed event: from, to, by):
//
//   - its owner, or an administrator (account.manage), from the browser: PATCH /api/sessions/{id}.
//     Anyone else the session is shared with can see it but not rename it.
//   - the session that started it (its parent), with the parent's own session token:
//     POST /api/sessions/{id}/children/{child}/title (`blerg-runner session rename <child> "<title>"`).
//     delegateChild admits the parent's own children only: no grandchildren, no other session.
//   - the session's own agent, with its own session token: POST /api/sessions/{id}/title
//     (`blerg-runner title "<title>"`).
//
// Every title is cleaned on write (cleanSessionTitle: one line, no control or invisible format
// characters), since it reaches people (sidebar, pushes, toasts, cards) and other agents (the
// parent's `session list`): the renames here, a child's start (delegation_start.go), the browser's
// start (HandlePostSessions) and every StartSession (REST, MCP, board runs, crons). An agent's title must then be 1 to maxDelegationTitleRunes characters,
// as a session start's is; a person's is cut to that length.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
	"github.com/jackc/pgx/v5"
)

// Who renamed a session, as its History records it.
const (
	renamedByPerson = "person"
	renamedByParent = "parent"
	renamedByAgent  = "agent"
)

const renameBodyLimit = 16 << 10

// sessionRenamed is a session_renamed event's payload.
type sessionRenamed struct {
	From string `json:"from"`
	To   string `json:"to"`
	By   string `json:"by"`
}

// cleanSessionTitle is a title as it is stored: topics.CleanText (any whitespace, newlines included,
// becomes one space; C0/C1 controls, bidi controls and zero-width characters are dropped), uncut.
func cleanSessionTitle(raw string) string {
	return topics.CleanText(raw, len(raw)+1)
}

// agentTitle checks a title an agent sets: cleaned, then a session start's rule.
func agentTitle(raw string) (string, *APIError) {
	title := cleanSessionTitle(raw)
	if title == "" {
		return "", apiErrorf(http.StatusUnprocessableEntity, "title is required")
	}
	if utf8.RuneCountInString(title) > maxDelegationTitleRunes {
		return "", apiErrorf(http.StatusUnprocessableEntity, "title is longer than %d characters", maxDelegationTitleRunes)
	}
	return title, nil
}

// renameSession sets the session's title and records the rename in its History, in one
// transaction; then tells the browsers. A title equal to the current one changes nothing and records
// nothing.
func (a *API) renameSession(ctx context.Context, sessionID, title, by string) *APIError {
	if a.dbPool == nil {
		return apiErrorf(http.StatusServiceUnavailable, "database not available")
	}
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		log.Printf("rename session %s: begin: %v", sessionID, err)
		return apiErrorf(http.StatusInternalServerError, "update failed")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	old, err := db.RenameSessionTx(ctx, tx, sessionID, title)
	if errors.Is(err, pgx.ErrNoRows) {
		return apiErrorf(http.StatusNotFound, "session not found")
	}
	if err != nil {
		log.Printf("rename session %s: %v", sessionID, err)
		return apiErrorf(http.StatusInternalServerError, "update failed")
	}
	from := derefOrEmpty(old)
	if from == title {
		return nil
	}
	raw, _ := json.Marshal(sessionRenamed{From: from, To: title, By: by})
	if _, _, _, err := a.appendWorkEvent(ctx, tx, sessionID, "session_renamed", raw); err != nil {
		log.Printf("rename session %s: record: %v", sessionID, err)
		return apiErrorf(http.StatusInternalServerError, "update failed")
	}
	log.Printf("session %s renamed by %s", sessionID, by)
	a.hub.BroadcastTitleChanged(sessionID, title)
	return nil
}

// HandlePatchSession is PATCH /api/sessions/{id}: the session's owner, or an administrator, renames
// it. A private session is its owner's alone, and to anyone else it answers as an unknown one does.
func (a *API) HandlePatchSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}
	if !a.browserCanSee(r.Context(), principal.Sub, sessionID) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if a.dbPool != nil {
		row, err := db.GetSession(r.Context(), a.dbPool, sessionID)
		if err != nil {
			writeDatabaseFailure(w, "check the session", "patch session GetSession", err)
			return
		}
		if row != nil && !ownsSession(principal.Sub, row) && !principal.Has(insightsAdminCap) {
			writeError(w, http.StatusForbidden, "only the session's owner or an administrator can rename it")
			return
		}
	}

	var req patchSessionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, renameBodyLimit)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	title := topics.CleanText(req.Title, maxDelegationTitleRunes)
	if title == "" {
		writeError(w, http.StatusUnprocessableEntity, "title is required")
		return
	}
	if problem := a.renameSession(r.Context(), sessionID, title, renamedByPerson); problem != nil {
		writeAPIError(w, problem)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleRenameOwnSession is POST /api/sessions/{id}/title: the session's agent renames its own
// session, with its own session token.
func (a *API) HandleRenameOwnSession(w http.ResponseWriter, r *http.Request) {
	sid, body, ok := a.agentSessionWrite(w, r, "title", renameBodyLimit)
	if !ok {
		return
	}
	title, problem := decodeRename(body)
	if problem == nil {
		problem = a.renameSession(r.Context(), sid, title, renamedByAgent)
	}
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"session_id": sid, "title": title})
}

// handleChildRename is POST /api/sessions/{id}/children/{child}/title: a parent renames one of its
// own children (delegateChild: the parent's own session token, its own children only).
func (a *API) handleChildRename(w http.ResponseWriter, r *http.Request) {
	_, _, child, ok := a.delegateChild(w, r)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if !decodeChildBody(w, r, &req) {
		return
	}
	title, problem := agentTitle(req.Title)
	if problem == nil {
		problem = a.renameSession(r.Context(), child, title, renamedByParent)
	}
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"session_id": child, "title": title})
}

func decodeRename(body []byte) (string, *APIError) {
	var req struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", apiErrorf(http.StatusBadRequest, "invalid JSON")
	}
	return agentTitle(req.Title)
}
