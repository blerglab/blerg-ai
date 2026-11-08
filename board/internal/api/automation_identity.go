package api

import (
	"context"
	"net/http"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

type automationIdentityReader interface {
	Identity(context.Context, string) (string, error)
}

func (a *API) identityName(ctx context.Context, proof string) string {
	reader, ok := a.Minter.(automationIdentityReader)
	if !ok || proof == "" {
		return ""
	}
	name, err := reader.Identity(ctx, proof)
	if err != nil {
		return ""
	}
	return name
}

func (a *API) handleAutomationIdentity(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	automation, err := db.GetBoardAutomation(r.Context(), a.Pool, boardID)
	if err != nil {
		writeError(w, http.StatusNotFound, "Board automation is unavailable.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"username": a.identityName(r.Context(), automation.Token)})
}
