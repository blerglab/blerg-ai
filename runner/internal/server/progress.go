package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"io"
	"net/http"
	"time"
)

// HandlePostProgress accepts only a session's own credential. Origin fields identify an engine
// shell call, never a display child ID; the live owning daemon resolves them before storage.
func (a *API) HandlePostProgress(w http.ResponseWriter, r *http.Request) {
	sid, body, ok := a.agentSessionWrite(w, r, "progress", protocol.MaxProgressUpdatedBytes)
	if !ok {
		return
	}
	var req struct {
		Completed      *int   `json:"completed"`
		Total          *int   `json:"total"`
		Label          string `json:"label"`
		Done           bool   `json:"done"`
		CallID         string `json:"call_id"`
		NativeThreadID string `json:"native_thread_id"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeError(w, 400, "Progress must be a complete bounded snapshot.")
		return
	}
	if (req.CallID != "" && !protocol.ValidBackgroundTaskID(req.CallID)) || (req.NativeThreadID != "" && !protocol.ValidBackgroundTaskID(req.NativeThreadID)) || (req.CallID != "" && req.NativeThreadID != "") {
		writeError(w, 400, "Progress origin is invalid.")
		return
	}
	if req.Done && (req.Completed != nil || req.Total != nil || req.Label != "") {
		writeError(w, 400, "Done completes the previous progress snapshot.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	payload := protocol.ProgressUpdatedPayload{}
	var originDaemon *DaemonConn
	var unlock func()
	if req.CallID != "" || req.NativeThreadID != "" {
		originDaemon = a.hub.FindDaemonForSession(sid)
		if originDaemon == nil {
			writeError(w, 409, "The session cannot attribute this progress right now.")
			return
		}
		res, err := a.hub.requestProgressOrigin(ctx, originDaemon, sid, req.CallID, req.NativeThreadID)
		if err != nil {
			writeError(w, 409, "The progress call is unknown, ended, or unavailable.")
			return
		}
		payload.ParentCallID, payload.ChildID = res.ParentCallID, res.ChildID
		unlock = a.hub.lockDaemonWork(originDaemon.ID)
		defer func() {
			if unlock != nil {
				unlock()
			}
		}()
		if a.hub.GetDaemon(originDaemon.ID) != originDaemon || a.hub.FindDaemonForSession(sid) != originDaemon {
			writeError(w, 409, "The progress call belongs to an old daemon.")
			return
		}
		if originDaemon.RuntimeTokenID != "" {
			if err := db.ValidateRuntimeBinding(ctx, a.dbPool, sid, originDaemon.RuntimeTokenID, false); err != nil {
				writeError(w, 409, "The session runtime changed.")
				return
			}
		}
	}
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		writeDatabaseFailure(w, "record progress", "progress begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := db.LockSessionWork(ctx, tx, "progress", sid); err != nil {
		writeDatabaseFailure(w, "record progress", "progress lock", err)
		return
	}
	var status, daemonID string
	if err := tx.QueryRow(ctx, `SELECT status,daemon_id FROM sessions WHERE id=$1 FOR UPDATE`, sid).Scan(&status, &daemonID); err != nil {
		writeDatabaseFailure(w, "record progress", "progress session", err)
		return
	}
	if originDaemon != nil && originDaemon.RuntimeTokenID != "" {
		if err := db.ValidateRuntimeBindingTx(ctx, tx, sid, originDaemon.RuntimeTokenID); err != nil {
			writeError(w, 409, "The session runtime changed.")
			return
		}
	}

	if terminalSessionStatus(status) {
		writeError(w, 409, "This session has ended.")
		return
	}
	scope, _ := protocol.ProgressScope(payload.ParentCallID, payload.ChildID)
	if originDaemon != nil && daemonID != originDaemon.ID {
		writeError(w, 409, "The session runtime changed.")
		return
	}
	if scope != "" {
		if err := db.ValidateChildProgressTx(ctx, tx, sid, scope, originDaemon.ID, time.Now()); err != nil {
			writeError(w, 409, "The subagent has ended or changed.")
			return
		}
	}
	if req.Done {
		since := int64(0)
		if scope != "" {
			if err := tx.QueryRow(ctx, `SELECT max(seq) FROM agent_events WHERE session_id=$1 AND kind='subagent_started' AND payload->>'child_id'=$2`, sid, scope).Scan(&since); err != nil {
				writeDatabaseFailure(w, "read progress", "progress incarnation", err)
				return
			}
		}
		rows, err := db.ListScopeProgressEvents(ctx, tx, sid, 1<<62, scope, since)
		if err != nil {
			writeDatabaseFailure(w, "read progress", "progress prior", err)
			return
		}
		var latest int64
		for _, row := range rows {
			if row.Kind != protocol.ProgressUpdatedKind || row.Seq <= latest {
				continue
			}
			prior, valid := protocol.DecodeProgressUpdated([]byte(row.Payload))
			if !valid {
				continue
			}
			payload.Completed, payload.Total, payload.Label = prior.Total, prior.Total, prior.Label
			latest = row.Seq
		}
		if latest == 0 {
			writeError(w, 409, "There is no explicit progress to complete.")
			return
		}
	} else {
		if req.Completed == nil || req.Total == nil {
			writeError(w, 400, "Progress needs completed, total and label.")
			return
		}
		payload.Completed, payload.Total, payload.Label = *req.Completed, *req.Total, req.Label
	}
	clean, valid := protocol.SanitizeProgressUpdated(payload)
	if !valid {
		writeError(w, 400, "Progress needs 0 <= completed <= total <= 1000000 and a label.")
		return
	}
	raw, _ := json.Marshal(clean)
	_, _, _, err = a.appendWorkEvent(ctx, tx, sid, protocol.ProgressUpdatedKind, raw)
	if unlock != nil {
		unlock()
		unlock = nil
	}
	if err != nil {
		writeDatabaseFailure(w, "record progress", "progress append", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, clean)
}
