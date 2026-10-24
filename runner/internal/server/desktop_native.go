package server

import (
	"net/http"
	"slices"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Native desktop helpers use the existing per-session message credential,
// never the desktop bearer. They may act only for a live session on its exact
// person's current authenticated desktop. An old/unknown owner fails closed.
func (a *API) ownedDesktopSessionToken(r *http.Request) (*db.SessionRow, error) {
	if a.dbPool == nil || a.hub == nil {
		return nil, db.ErrTokenInvalid
	}
	token, err := db.ValidateBoardToken(r.Context(), a.dbPool, a.rawBearer(r))
	if err != nil || token.BoardID != "" || !slices.Contains(token.Capabilities, "message") {
		return nil, db.ErrTokenInvalid
	}
	row, err := db.GetSession(r.Context(), a.dbPool, token.SessionID)
	if err != nil || row == nil || row.SpawningAccountID == nil || row.EndedAt != nil || terminalSessionStatus(row.Status) || row.ArchivedAt != nil || row.DiscardRequestedAt != nil {
		return nil, db.ErrTokenInvalid
	}
	desktop, err := db.GetDesktop(r.Context(), a.dbPool, row.DaemonID)
	if err != nil || desktop == nil || desktop.OwnerID != *row.SpawningAccountID || desktop.OwnerID == "" {
		return nil, db.ErrTokenInvalid
	}
	dc := a.hub.GetHostDaemon(row.DaemonID)
	if dc == nil || dc.DesktopBinding == nil || dc.DesktopBinding.OwnerID != desktop.OwnerID || !dc.DesktopBinding.ExpiresAt.After(time.Now()) || db.ValidateDesktopConnection(r.Context(), a.dbPool, *dc.DesktopBinding, dc.DesktopGeneration) != nil {
		return nil, db.ErrTokenInvalid
	}
	a.slideTokenExpiry(r.Context(), token)
	return row, nil
}

func (a *API) authNativePublish(w http.ResponseWriter, r *http.Request) bool {
	if a.isDaemonToken(a.rawBearer(r)) {
		return true
	}
	if _, err := a.ownedDesktopSessionToken(r); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}
