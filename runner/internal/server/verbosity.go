package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

const verbosityEnvVar = "BLERG_RUNNER_VERBOSITY"
const verbosityMessage = `verbosity must be "narrated", "standard", or "terse"`

func (a *API) resolveVerbosity(ctx context.Context, asked, account string, child bool) (string, *APIError) {
	if asked != "" {
		if !protocol.ValidVerbosity(asked) {
			return "", apiErrorf(400, "%s", verbosityMessage)
		}
		return asked, nil
	}
	if child {
		return protocol.VerbosityTerse, nil
	}
	if account != "" && a.dbPool != nil {
		raw, err := db.GetAccountPreference(ctx, a.dbPool, account, prefVerbosity)
		if err != nil {
			return "", apiErrorf(503, "could not read verbosity default")
		}
		if raw != nil {
			var value string
			if json.Unmarshal(raw, &value) != nil || !protocol.ValidVerbosity(value) {
				return "", apiErrorf(503, "invalid verbosity default")
			}
			return value, nil
		}
	}
	return protocol.VerbosityStandard, nil
}

func effectiveVerbosity(row *db.SessionRow) string {
	if row != nil && protocol.ValidVerbosity(row.Verbosity) {
		return row.Verbosity
	}
	return protocol.VerbosityStandard
}

func recordVerbosity(ctx context.Context, pool *pgxpool.Pool, id, value string) error {
	if pool == nil {
		return nil
	}
	if !protocol.ValidVerbosity(value) {
		value = protocol.VerbosityStandard
	}
	_, err := pool.Exec(ctx, `UPDATE sessions SET verbosity=$2 WHERE id=$1`, id, value)
	return err
}

// Only the person who owns a session (or an administrator) may change its style.
// Live changes apply on the next input; the host must be connected to accept them.
//
//nolint:contextcheck // Browser broadcast delivery uses independent bounded contexts after the setting is saved.
func (a *API) HandleSessionVerbosity(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	account, ok := a.personAccount(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	row, err := db.GetSession(r.Context(), a.dbPool, id)
	if err != nil {
		writeError(w, 503, "could not read session")
		return
	}
	if !a.browserCanSee(r.Context(), account, id) || row == nil || (derefOrEmpty(row.SpawningAccountID) != account && !p.Has("account.manage")) {
		writeError(w, 404, "session not found")
		return
	}
	var request struct {
		Verbosity string `json:"verbosity"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || !protocol.ValidVerbosity(request.Verbosity) {
		writeError(w, 400, verbosityMessage)
		return
	}
	if row.Status == "starting" {
		writeError(w, 409, "the session is starting; try again when it is ready")
		return
	}
	if row.Kind != "agent" || terminalSessionStatus(row.Status) || row.EndedAt != nil {
		writeError(w, 409, "verbosity requires an active agent session")
		return
	}
	dc := a.hub.FindDaemonForSession(id)
	if dc == nil {
		writeError(w, 503, "the session host is not connected; try again shortly")
		return
	}
	if err := recordVerbosity(r.Context(), a.dbPool, id, request.Verbosity); err != nil {
		writeError(w, 503, "could not save verbosity")
		return
	}
	raw, _ := json.Marshal(protocol.SetSessionVerbosity{Type: "set_session_verbosity", SessionID: id, Verbosity: request.Verbosity})
	if !a.hub.trySendToDaemon(dc, raw) {
		writeError(w, 503, "Verbosity saved. The host is busy; try again to apply it.")
		return
	}
	a.hub.BroadcastJSON(map[string]string{"type": "session_verbosity_changed", "session_id": id, "verbosity": request.Verbosity})
	writeJSON(w, 200, map[string]string{"verbosity": request.Verbosity, "applies_to": "next_turn"})
}
