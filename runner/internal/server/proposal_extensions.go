package server

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func (h *proposalsHandler) extend(w http.ResponseWriter, r *http.Request) {
	who, ok := h.person(w, r)
	if !ok {
		return
	}
	var req struct {
		Expected time.Time `json:"expected_expires_at"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, proposalBodyLimit))
	if err != nil || filingDecode(raw, &req) != nil || req.Expected.IsZero() {
		writeError(w, 400, "expected_expires_at must be the deadline you reviewed")
		return
	}
	p, err := db.ExtendProposal(r.Context(), h.api.dbPool, who.Account, r.PathValue("id"), req.Expected)
	if errors.Is(err, db.ErrProposalExtensionCap) {
		writeError(w, http.StatusConflict, "Proposals can stay open up to 30 days; this one cannot be extended again and expires at its deadline")
		return
	}
	if err != nil {
		transitionError(w, err, "only a pending, unexpired proposal with the reviewed deadline can be extended; refresh and try again")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, viewOfProposal(p))
}
