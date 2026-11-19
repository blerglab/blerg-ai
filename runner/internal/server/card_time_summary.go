package server

import (
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/cardtime"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// RegisterCardTimeSummaryRoute registers the card-time read for the pinned-card glyph and the
// checklist-item glyph (BLG-444). It never returns worker titles, session ids or mark prose —
// only the numbers the glyph's tooltip already shows.
func (a *API) RegisterCardTimeSummaryRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/board-cards/{board}/{number}/card-time", a.HandleCardTimeSummary)
}

// HandleCardTimeSummary is owner-scoped. H1: boardCardRequest alone only checks project scope and
// a project-limited sign-in's own board — it does NOT confirm the card is still visible on the
// live board right now, so an account whose board access was since revoked would otherwise still
// read totals. This calls the board the same way the card panel does (readLiveBoardCard, the same
// live visibility check HandleCardData makes) before answering anything; a denied or revoked
// board answers the caller's own error, never a total. It answers {} (no fields) whenever the
// total itself isn't known yet — no linked work, a still-running or not-yet-backfilled session,
// or a disqualified (unclassified-time) total — never a guessed or zero number. Codex round 3: the
// glyph shows whenever the card HAS linked work, not only once a budget threshold has fired — a
// card under budget still answers its real (small) total and round count, it just never crosses
// into the plan-drift notice path.
func (a *API) HandleCardTimeSummary(w http.ResponseWriter, r *http.Request) {
	proof, board, number, caller, ok := a.boardCardRequest(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
		return
	}
	if _, apiErr := readLiveBoardCard(r.Context(), caller, proof, board, number, ""); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	seconds, ok, _, err := db.CardTimeTotal(r.Context(), a.dbPool, proof.AccountID, board, number)
	if err != nil {
		writeDatabaseFailure(w, "read card time", "card time total", err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	marks, err := db.CardReviewMarks(r.Context(), a.dbPool, proof.AccountID, board, number)
	if err != nil {
		writeDatabaseFailure(w, "read card time", "card time marks", err)
		return
	}
	rounds := cardtime.Rounds(marks)
	verdict, high, medium, hasDetail := cardtime.LatestDetail(marks)
	resp := map[string]any{"seconds": seconds, "rounds": rounds}
	if hasDetail {
		resp["verdict"] = verdict
		resp["open_high"] = high
		resp["open_medium"] = medium
	}
	writeJSON(w, http.StatusOK, resp)
}
