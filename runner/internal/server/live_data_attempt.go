package server

import (
	"encoding/json"
	"io"
	"net/http"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/runner/internal/livepoll"
)

func (a *API) handleLiveAttempt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !a.liveDefinitionAuthenticated(r) {
		_ = boundFailure("live_attempt_unauthenticated")
		writeError(w, 401, "Unavailable.")
		return
	}
	if !a.liveDataPollingEnabled {
		_ = boundFailure("live_data_disabled")
		writeError(w, 503, "Unavailable.")
		return
	}
	var body ld.AttemptReceipt
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&body) != nil || decoder.Decode(&extra) != io.EOF {
		_ = boundFailure("invalid_subject")
		writeError(w, 400, "Unavailable.")
		return
	}
	if err := (livepoll.Attempts{Pool: a.dbPool}).CredentialReceipt(r.Context(), body); err != nil {
		status := 403
		if livepoll.FailureCause(err) == "credential_receipt_unavailable" {
			status = 503
		}
		writeError(w, status, "Unavailable.")
		return
	}
	writeJSON(w, 200, body)
}
