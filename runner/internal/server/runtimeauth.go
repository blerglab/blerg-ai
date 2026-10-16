package server

import (
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// authenticateRuntimeToken grants only the immutable current cluster launch.
// Callers must apply their own operation and repository/session constraints.
func (a *API) authenticateRuntimeToken(r *http.Request) (*db.SessionRow, error) {
	token, err := db.ValidateRuntimeToken(r.Context(), a.dbPool, a.rawBearer(r))
	if err != nil {
		return nil, err
	}
	session, err := db.GetSession(r.Context(), a.dbPool, token.SessionID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.EndedAt != nil || session.Status == "stopped" || session.Status == "ended" {
		return nil, db.ErrTokenInvalid
	}
	session.Repo, session.ProjectPath, err = db.RuntimeScope(r.Context(), a.dbPool, token.SessionID, token.ID)
	if err != nil {
		return nil, err
	}
	return session, nil
}
