package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
)

// HandleReturnRecoveryInput claims the original chat input once across all
// devices and server restarts. The daemon's error text is never the draft source.
func (a *API) HandleReturnRecoveryInput(w http.ResponseWriter, r *http.Request) {
	who, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	row, ok := a.ownedSession(r.Context(), w, who.Account, r.PathValue("id"))
	if !ok {
		return
	}
	clientID := r.PathValue("client")
	if clientID == "" || len(clientID) > db.SessionInputMaxClientIDBytes {
		writeError(w, 400, "invalid message id")
		return
	}
	var text string
	err := a.dbPool.QueryRow(r.Context(), `SELECT i.frame->>'text' FROM session_inputs i
 WHERE i.session_id=$1 AND i.client_id=$2 AND i.settled_at IS NOT NULL
 AND COALESCE(i.frame->>'source','chat') IN ('','chat')
 AND EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=i.session_id AND e.kind='error'
 AND e.payload->>'code'='input_recovery_failed' AND e.payload->>'client_id'=i.client_id)`, row.ID, clientID).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, 200, map[string]any{"text": nil})
		return
	}
	if err != nil {
		writeDatabaseFailure(w, "return the message", "recovery input return", err)
		return
	}
	payload, _ := json.Marshal(map[string]string{"client_id": clientID})
	_, fresh, err := db.AppendAgentEvent(r.Context(), a.dbPool, row.ID, messageEventID(clientID, "input-return-claimed"), "input_return_claimed", string(payload))
	if err != nil {
		writeDatabaseFailure(w, "return the message", "recovery input return", err)
		return
	}
	if !fresh {
		writeJSON(w, 200, map[string]any{"text": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"text": text})
}
