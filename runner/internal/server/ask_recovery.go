package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// authQuestionRecovery admits only the session's host: the daemon token, the bearer of the
// desktop the session runs on, or a pod's runtime token, which binds it to its own session. Every
// agent can read its session token, and a restart is not something an agent may declare.
func (a *API) authQuestionRecovery(w http.ResponseWriter, r *http.Request, sessionID *string) bool {
	raw := a.rawBearer(r)
	ctx := r.Context()
	switch {
	case raw == "":
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	case a.isDaemonToken(raw):
		return true
	case strings.HasPrefix(raw, "bdt_"):
		b, err := a.authenticateDesktop(ctx, raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return false
		}
		return a.desktopQuestionRecoveryAllowed(w, r, b, *sessionID)
	}
	runtime, err := db.ValidateRuntimeToken(ctx, a.dbPool, raw)
	if err == nil {
		*sessionID = runtime.SessionID
		return true
	}
	if !errors.Is(err, db.ErrTokenInvalid) {
		writeError(w, http.StatusServiceUnavailable, "credential unavailable")
		return false
	}
	if _, err := db.ValidateBoardToken(ctx, a.dbPool, raw); err == nil {
		writeError(w, http.StatusForbidden, "only the session's host recovers its questions")
		return false
	}
	writeError(w, http.StatusUnauthorized, "unauthorized")
	return false
}

// HandleRecoverQuestions ends the blocking asks of hostings older than host_generation: their
// waiters died with the host. An ask of that generation or a later one has a live waiter, so a
// retry that lands late never reaches it. A person-only question, at any depth, keeps the interruption
// transition (holdPersonQuestion): it stays open for the person.
func (a *API) HandleRecoverQuestions(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, 503, "no database")
		return
	}
	var request struct {
		SessionID      string `json:"session_id"`
		HostGeneration int64  `json:"host_generation"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeError(w, 400, "session_id required")
		return
	}
	if !a.authQuestionRecovery(w, r, &request.SessionID) {
		return
	}
	if !validSessionID(request.SessionID) {
		writeError(w, 400, "session_id required")
		return
	}
	if request.HostGeneration <= 0 {
		writeError(w, 400, "host_generation required")
		return
	}
	ctx := r.Context()
	err := withLimitResumeActionLock(ctx, a.dbPool, request.SessionID, func(_ *pgxpool.Conn) error {
		rows, err := a.dbPool.Query(ctx, `SELECT id::text FROM messages WHERE session_id=$1 AND kind='ask' AND status='open' AND NOT no_wait AND host_generation<$2 AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=messages.id)`, request.SessionID, request.HostGeneration)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		withdrawn := 0
		for _, id := range ids {
			held, err := a.holdPersonQuestion(ctx, id)
			if err != nil {
				return err
			}
			if held {
				continue
			}
			tag, err := a.dbPool.Exec(ctx, `UPDATE messages SET status='withdrawn',answer=NULL,answered_at=NULL,child_parent_notified=false WHERE id=$1 AND status='open' AND NOT no_wait`, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			row, err := db.GetMessage(ctx, a.dbPool, id)
			if err != nil {
				return err
			}
			if row == nil {
				continue
			}
			withdrawn++
			a.waiters.signal(id)
			a.hub.SignalAskAnswered(id)
			a.hub.QuestionAnswered(*row, "", "")
			recordServerAgentEvent(ctx, a.hub, a.dbPool, request.SessionID, messageEventID(id, "closed"), "update", messagingPayload{MessageID: id, Body: "Question " + id + " withdrawn after restart; its waiter ended without an answer."})
			a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)}) //nolint:contextcheck // browser delivery has its own bounded context and outlives the recovery request
		}
		log.Printf("session %s restart withdrew %d blocking question(s) without answers", request.SessionID, withdrawn)
		return nil
	})
	if err != nil {
		writeError(w, 503, "question recovery unavailable; retry before starting work")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// desktopQuestionRecoveryAllowed shares the host boundary with waiter replay:
// the current desktop owner and revision must still host this exact session.
func (a *API) desktopQuestionRecoveryAllowed(w http.ResponseWriter, r *http.Request, b desktopauth.Binding, sessionID string) bool {
	row, err := db.GetSession(r.Context(), a.dbPool, sessionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session unavailable")
		return false
	}
	if row == nil || row.DaemonID != b.DesktopID {
		writeError(w, http.StatusForbidden, "not this desktop's session")
		return false
	}
	desktop, err := db.GetDesktop(r.Context(), a.dbPool, b.DesktopID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "desktop unavailable")
		return false
	}
	if desktop == nil || desktop.OwnerID != b.OwnerID || desktop.Revision != b.Revision {
		writeError(w, http.StatusForbidden, "not this desktop's session")
		return false
	}
	return true
}

func (a *API) authMessageExpiry(w http.ResponseWriter, r *http.Request, id string, waiterEnded bool) (string, bool) {
	raw := a.rawBearer(r)
	if !strings.HasPrefix(raw, "bdt_") {
		return a.authMessaging(w, r)
	}
	if !waiterEnded {
		writeError(w, 403, "the host may only report an interrupted question")
		return "", false
	}
	b, err := a.authenticateDesktop(r.Context(), raw)
	if err != nil {
		writeError(w, 401, "unauthorized")
		return "", false
	}
	if a.dbPool == nil {
		writeError(w, 503, "no database")
		return "", false
	}
	row, err := db.GetMessage(r.Context(), a.dbPool, id)
	if err != nil {
		writeError(w, 503, "question unavailable")
		return "", false
	}
	if row == nil {
		writeError(w, 404, "message not found")
		return "", false
	}
	if !a.desktopQuestionRecoveryAllowed(w, r, b, row.SessionID) {
		return "", false
	}
	return row.SessionID, true
}
