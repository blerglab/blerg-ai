package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

func acceptSessionInput(ctx context.Context, h *Hub, pool *pgxpool.Pool, msg protocol.AgentUserMessage) (bool, error) {
	if pool == nil || msg.ClientID == "" || harnessCommand(msg.Text) {
		return true, nil
	}
	if len(msg.ClientID) > db.SessionInputMaxClientIDBytes {
		return false, db.ErrSessionInputID
	}
	if len(msg.Text) > db.SessionInputMaxBytes {
		return false, db.ErrSessionInputTooLarge
	}
	frame, err := json.Marshal(msg)
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(map[string]any{"client_id": msg.ClientID, "text": msg.Text})
	if err != nil {
		return false, err
	}
	eventID := messageEventID(msg.ClientID, "input-accepted")
	seq, fresh, err := db.AcceptSessionInput(ctx, pool, msg.SessionID, msg.ClientID, string(protocol.StripNUL(frame)), eventID, string(protocol.StripNUL(payload)))
	if err == nil && fresh {
		fanOutServerEvent(h, msg.SessionID, eventID, "input_accepted", payload, seq, time.Now())
	}
	return fresh, err
}

func settleSessionInput(ctx context.Context, pool *pgxpool.Pool, msg protocol.AgentEvent) error {
	if pool == nil || (msg.Kind != "user_message" && msg.Kind != "error") {
		return nil
	}
	var p struct {
		ClientID string `json:"client_id"`
	}
	// Only valid receipts settle input; other event payloads must still ingest.
	if err := json.Unmarshal(msg.Payload, &p); err == nil && p.ClientID != "" {
		return db.SettleSessionInput(ctx, pool, msg.SessionID, p.ClientID)
	}
	return nil
}

func replayPendingSessionInputs(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, sessionIDs []string) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if pool == nil || dc == nil || !dc.CanInputReceipts() {
		return
	}
	for _, id := range sessionIDs {
		if problem := parentResumeProblem(ctx, pool, id); problem != nil {
			if problem.Status == http.StatusConflict {
				failPendingSessionInputs(ctx, h, pool, id)
			}
			continue
		}
		// An answer to a question the agent did not wait on that a server restart cut off between
		// its record and its delivery is sent now (questions.go); the client id dedupes a race.
		redeliverQuestionAnswers(ctx, h, pool, id)
		// Every heartbeat lists every active session: take the lock only when
		// there is something to replay.
		if pending, err := db.HasPendingSessionInputs(ctx, pool, id); err != nil || !pending {
			if err != nil {
				log.Printf("pending session inputs %s: %v", id, err)
			}
			continue
		}
		err := withLimitResumeActionLock(ctx, pool, id, func(conn *pgxpool.Conn) error {
			return withParentInputGuard(ctx, pool, id, func() error {
				row, err := db.GetSession(ctx, pool, id)
				if err != nil {
					return err
				}
				if row == nil || row.DaemonID != dc.ID {
					return nil
				}
				if sessionInputsEnded(row) {
					failPendingSessionInputs(ctx, h, pool, id)
					return nil
				}
				pending, err := db.PendingSessionInputs(ctx, pool, id)
				if err != nil {
					return err
				}
				for _, input := range pending {
					var msg protocol.AgentUserMessage
					if json.Unmarshal([]byte(input.Frame), &msg) != nil {
						continue
					}
					if (msg.Source == "" || answersAsk(msg.Source) || msg.AnswerMessageID != "") && answerAskFrom(ctx, h, pool, id, msg.Text, msg.ClientID, msg.AnswerMessageID, "", msg.Source) {
						continue
					}
					if err := ctx.Err(); err != nil {
						return err
					}
					if !h.trySendToDaemon(dc, []byte(input.Frame)) {
						return nil // disconnected, replaced, or busy: retried on the next heartbeat
					}
				}
				return nil
			})
		})
		if errors.Is(err, db.ErrDelegationEnded) {
			failPendingSessionInputs(ctx, h, pool, id)
			continue
		}
		if err != nil {
			log.Printf("pending session inputs %s: %v", id, err)
		}
	}
}

func sessionInputsEnded(row *db.SessionRow) bool {
	return row != nil && (row.EndedAt != nil || (row.Status == "stopped" || row.Status == "ended") && row.EndReason != nil)
}

func repairEndedSessionInputs(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID string) {
	if pool == nil {
		return
	}
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil {
		log.Printf("read session input state %s: %v", sessionID, err)
		return
	}
	if sessionInputsEnded(row) {
		failPendingSessionInputs(ctx, h, pool, sessionID)
	}
}

// True session endings settle every durable bubble with a visible failure.
// Revivable errors and daemon disconnects deliberately do not call this helper.
func failPendingSessionInputs(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID string) {
	if pool == nil {
		return
	}
	pending, err := db.PendingSessionInputs(ctx, pool, sessionID)
	if err != nil {
		log.Printf("ended session inputs %s: %v", sessionID, err)
		return
	}
	for _, input := range pending {
		var msg protocol.AgentUserMessage
		if json.Unmarshal([]byte(input.Frame), &msg) != nil {
			continue
		}
		payload, _ := json.Marshal(map[string]any{"message": "Message was not delivered because the session ended.", "retryable": true, "client_id": msg.ClientID})
		eventID := messageEventID(msg.ClientID, "input-undelivered")
		seq, fresh, err := db.FailSessionInput(ctx, pool, sessionID, msg.ClientID, eventID, string(payload))
		if err != nil {
			log.Printf("settle ended session input %s: %v", sessionID, err)
			continue
		}
		if fresh {
			fanOutServerEvent(h, sessionID, eventID, "error", payload, seq, time.Now())
		}
	}
}

func (dc *DaemonConn) CanInputReceipts() bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.inputReceipts
}
func (dc *DaemonConn) SetInputReceipts(v bool) {
	dc.liveMu.Lock()
	dc.inputReceipts = v
	dc.liveMu.Unlock()
}

// sessionInputAPIError preserves retry/validation semantics without exposing
// database details or conversational content.
func sessionInputAPIError(err error) *APIError {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, db.ErrSessionInputConflict), errors.Is(err, db.ErrSessionInputEnded):
		status = http.StatusConflict
	case errors.Is(err, db.ErrSessionInputID), errors.Is(err, db.ErrSessionInputTooLarge):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, db.ErrSessionInputQueue):
		status = http.StatusTooManyRequests
	}
	return apiErrorf(status, "%s", sessionInputError(err))
}
