package server

import (
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
)

// HandleCardData is owner-scoped raw data for future reports, with the same
// live board permission check as the card panel. It never reads another owner's sessions.
func (a *API) HandleCardData(w http.ResponseWriter, r *http.Request) {
	proof, board, number, caller, ok := a.boardCardRequest(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
		return
	}
	card, apiErr := readLiveBoardCard(r.Context(), caller, proof, board, number, "")
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	data, err := db.CardData(r.Context(), a.dbPool, proof.AccountID, board, number)
	if err != nil {
		writeDatabaseFailure(w, "read card data", "card data", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"card": card, "activity": data})
}
