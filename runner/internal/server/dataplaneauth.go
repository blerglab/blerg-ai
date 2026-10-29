package server

import (
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Project memory is shared project state. A Cluster runtime may use only the
// exact repository on its current session; it cannot select a different project
// or use a restricted session's runtime credential to acquire extra tools. Native
// desktop helpers use their per-session message credential and additionally
// require the exact current persisted desktop owner and connection generation.
func (a *API) authDataPlane(w http.ResponseWriter, r *http.Request) (*db.SessionRow, bool) {
	if a.isDaemonToken(a.rawBearer(r)) {
		return nil, true
	}
	session, err := a.authenticateRuntimeToken(r)
	if err != nil {
		session, err = a.ownedDesktopSessionToken(r)
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	var restricted bool
	if a.dbPool == nil || a.dbPool.QueryRow(r.Context(), `SELECT restrict_tools FROM sessions WHERE id=$1`, session.ID).Scan(&restricted) != nil || restricted {
		writeError(w, http.StatusForbidden, "project tools unavailable for this session")
		return nil, false
	}
	return session, true
}
func dataPlaneProject(w http.ResponseWriter, session *db.SessionRow, project string) bool {
	if session != nil && (session.WorkspaceKind == "folder" || session.Repo == "" || project != session.Repo) {
		writeError(w, http.StatusForbidden, "project tools unavailable for this session")
		return false
	}
	return true
}
