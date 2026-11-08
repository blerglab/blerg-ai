package api

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

// Account clock preferences live with runner's per-account preferences; core owns the Settings UI.
func (d Deps) handleAccountTime(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := principalFromCtx(r.Context())
	if !ok || p.Sid == "" || p.OnBehalfOf != "" {
		writeJSONStatus(w, 403, map[string]string{"error": "Sign in as yourself to change your time zone."})
		return
	}
	var body []byte
	if r.Method == http.MethodPut {
		var err error
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			writeJSONStatus(w, 413, map[string]string{"error": "Request too large."})
			return
		}
	}
	status, raw, err := d.runnerDesktopCall(r.Context(), p, r.Method, "/internal/account-time", body)
	if err != nil {
		failurelog.SetCause(w, err)
		failurelog.Default.Event("account_time_proxy", slog.LevelWarn, "component", "core", "stage", "account_time_proxy", "cause", failurelog.Cause(err))
		writeJSONStatus(w, 503, map[string]string{"error": "Time settings are unavailable."})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw) //nolint:gosec // G705: trusted runner JSON, application/json content type.
}
