package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// HandleClearTask acknowledges a finished task without removing its recorded result.
func (a *API) HandleClearTask(w http.ResponseWriter, r *http.Request) {
	tokenSession, authenticated := a.authMessaging(w, r)
	if !authenticated {
		return
	}
	if tokenSession == "" || tokenSession != r.PathValue("id") {
		writeError(w, http.StatusForbidden, "only the session’s own token can clear its tasks")
		return
	}
	sid, _, ok := a.agentSessionWrite(w, r, "tasks", 1024)
	if !ok {
		return
	}
	task := r.PathValue("task")
	var finished bool
	err := a.dbPool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_events WHERE session_id=$1 AND kind='background_task_done' AND payload->>'task_id'=$2)`, sid, task).Scan(&finished)
	if err != nil {
		writeDatabaseFailure(w, "read the task", "clear task", err)
		return
	}
	if !finished {
		writeError(w, http.StatusNotFound, "finished task not found")
		return
	}
	raw, _ := json.Marshal(map[string]string{"task_id": task})
	id, at := newUUID(), time.Now()
	seq, _, err := db.AppendAgentEventAt(r.Context(), a.dbPool, sid, id, "background_task_cleared", string(raw), at)
	if err != nil {
		writeDatabaseFailure(w, "clear the task", "clear task", err)
		return
	}
	fanOutServerEvent(a.hub, sid, id, "background_task_cleared", raw, seq, at)
	writeJSON(w, http.StatusOK, map[string]string{"task_id": task})
}
