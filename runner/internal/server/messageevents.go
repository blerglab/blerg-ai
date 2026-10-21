package server

// The session→person messages of `blerg-runner ask|update|note`, in an agent session's transcript.
//
// The CLI posts them to /api/messages (messages.go), which keeps them in the messages table; the
// runner hides that thread for an agent session, whose chat is its transcript. So the server also
// records each one as an agent event — kind "ask", "update" or "note", payload
// {body, message_id} (agent.MessagingPayload) — and fans it out like any other.
//
// An ask is answered with POST /api/messages/{id}/reply (the card's reply box), or by the person
// simply typing in the chat while it is open: that message answers the ask instead of reaching the
// engine, which is blocked inside `blerg-runner ask` and would only read it after the ask timed out.
// Either way the answer is recorded as user_message{text, source: "ask_answer", message_id,
// client_id?}, and the CLI's poll returns it to the engine.
//
// The native loop is different: its ask/update/note tools record their own events (and the loop
// takes an answer typed in the chat itself), and it posts with the daemon token. The server records
// nothing for a message posted with the daemon token, nor for a session that is not an agent one.
//
// The events' client ids are derived from the message id (messageEventID), so recording one twice
// stores it once.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// messageEventID preserves the server helper while sharing its stable IDs with daemons.
func messageEventID(messageID, role string) string {
	return protocol.SessionMessageEventID(messageID, role)
}

// messagingPayload is the payload of an ask/update/note event (agent.MessagingPayload).
type messagingPayload struct {
	OwnerOnly bool   `json:"owner_only,omitempty"`
	NoWait    bool   `json:"no_wait,omitempty"`
	Body      string `json:"body"`
	MessageID string `json:"message_id"`
	TurnID    string `json:"turn_id,omitempty"`
}

// askAnswerPayload is the user_message an ask's answer is recorded as (agent.UserMessagePayload).
type askAnswerPayload struct {
	Text   string `json:"text"`
	Source string `json:"source"`
	// AnsweredBy is "session" when the parent session answered its child's ask (the runner sets
	// it from the parent's own session token, never from a request body), so the chat labels it
	// as the parent's and not the person's. Absent for the person's answer.
	AnsweredBy string `json:"answered_by,omitempty"`
	MessageID  string `json:"message_id"`
	ClientID   string `json:"client_id,omitempty"`
	TurnID     string `json:"turn_id,omitempty"`
}

// recordServerAgentEvent appends an event the server itself writes into a session's transcript and
// fans it out. A duplicate (the same client id) is not fanned out again. Failures are logged.
func recordServerAgentEvent(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, clientEventID, kind string, payload any) {
	if err := appendServerAgentEvent(ctx, h, pool, sessionID, clientEventID, kind, payload); err != nil {
		log.Printf("agent event %s/%s: %v", sessionID, kind, err)
	}
}

// appendServerAgentEvent is recordServerAgentEvent for a caller that must know the event landed
// (a delegation request whose card is the only way to decide it).
func appendServerAgentEvent(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, clientEventID, kind string, payload any) error {
	_, err := appendServerAgentEventOnce(ctx, h, pool, sessionID, clientEventID, kind, payload)
	return err
}

// appendServerAgentEventOnce is appendServerAgentEvent that reports whether the event is new (an
// event with the same client id already recorded is neither stored nor fanned out again).
func appendServerAgentEventOnce(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, clientEventID, kind string, payload any) (bool, error) {
	if pool == nil {
		return false, errors.New("no database")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	raw = protocol.StripNUL(raw)
	at := time.Now()
	seq, inserted, err := db.AppendAgentEventAt(ctx, pool, sessionID, clientEventID, kind, string(raw), at)
	if err != nil {
		return false, err
	}
	if inserted {
		fanOutServerEvent(h, sessionID, clientEventID, kind, raw, seq, at)
	}
	return inserted, nil
}

// fanOutServerEvent sends an event the server recorded to the session's subscribers.
func fanOutServerEvent(h *Hub, sessionID, clientEventID, kind string, raw []byte, seq int64, at time.Time) {
	msg, err := json.Marshal(protocol.AgentEvent{
		Type: "agent_event", SessionID: sessionID, ClientEventID: clientEventID, Seq: seq,
		Ts: at.UTC().Format(time.RFC3339Nano), Kind: kind, Payload: raw,
	})
	if err == nil {
		h.FanOutAgentEvent(sessionID, seq, msg)
		switch kind {
		case "ask", "user_message", "delegation_request", "delegation_decided":
			h.ChildActivity(sessionID)
		}
	}
}

// recordMessageEvent records a message posted by `blerg-runner` in its agent session's transcript.
func (a *API) recordMessageEvent(ctx context.Context, sess db.SessionRow, row db.MessageRow) {
	if sess.Kind != "agent" {
		return
	}
	recordServerAgentEvent(ctx, a.hub, a.dbPool, row.SessionID, messageEventID(row.ID, row.Kind), row.Kind,
		messagingPayload{Body: row.Body, MessageID: row.ID, OwnerOnly: row.OwnerOnly, NoWait: row.NoWait})
}

// createCodexQuestion commits the visible message and routing marker together,
// before broadcasting a card that a person could immediately answer.
func (a *API) createCodexQuestion(ctx context.Context, req postMessageRequest) (string, error) {
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO messages (session_id,kind,body,status) VALUES ($1,'ask',$2,'open') RETURNING id`, req.SessionID, req.Body).Scan(&id); err != nil {
		return "", err
	}
	raw, err := json.Marshal(messagingPayload{Body: req.Body, MessageID: id, TurnID: req.TurnID})
	if err != nil {
		return "", err
	}
	at := time.Now()
	eventID := messageEventID(id, "ask")
	seq, fresh, err := db.AppendAgentEventTx(ctx, tx, req.SessionID, eventID, "ask", string(protocol.StripNUL(raw)), at)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	if fresh {
		fanOutServerEvent(a.hub, req.SessionID, eventID, "ask", raw, seq, at)
	}
	return id, nil
}

// askTurnID preserves a native question's original turn when its answer is recorded.
// CLI asks and the native loop omit this field and retain their existing behavior.
func askTurnID(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, sessionID, messageID string) string {
	var turnID string
	_ = pool.QueryRow(ctx, `SELECT COALESCE(payload->>'turn_id','') FROM agent_events WHERE session_id=$1 AND client_event_id=$2`, sessionID, messageEventID(messageID, "ask")).Scan(&turnID)
	return turnID
}

// recordAskAnswer records the answer to an ask the transcript holds (one `blerg-runner ask` posted;
// the native loop records its own answers).
func recordAskAnswer(ctx context.Context, h *Hub, pool *pgxpool.Pool, row db.MessageRow, answer, clientID string) {
	if pool == nil || row.Kind != "ask" {
		return
	}
	recorded, err := db.HasAgentEvent(ctx, pool, row.SessionID, messageEventID(row.ID, "ask"))
	if err != nil {
		return
	}
	if !recorded {
		// Repair questions created before native Codex routing markers were persisted.
		sess, err := db.GetSession(ctx, pool, row.SessionID)
		if err != nil || sess == nil || sess.Engine == nil || *sess.Engine != "codex" {
			return
		}
	}
	recordServerAgentEvent(ctx, h, pool, row.SessionID, messageEventID(row.ID, "answer"), "user_message",
		askAnswerPayload{Text: answer, Source: "ask_answer", MessageID: row.ID, ClientID: clientID, TurnID: askTurnID(ctx, pool, row.SessionID, row.ID)})
}

// answerAskFromChat answers an open ask with a message the person sent to the session, and reports
// whether it did (the message then does not go to the engine). messageID names the ask (the card's
// reply); without one, the newest open ask the transcript holds is answered, if any — an ask of the
// native loop is left to the loop, which takes the message itself.
func answerAskFromChat(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, text, clientID, messageID string) bool {
	return answerAskFromChatAs(ctx, h, pool, sessionID, text, clientID, messageID, "")
}

// answerAskFromChatAs is answerAskFromChat for a sender known to be signed in
// as answerer ("" when the sender is not a signed-in person, or unknown). Only
// the session's owner answers an owner-only ask (an engine's command approval);
// anyone else's text is left to reach the session as an ordinary message.
func answerAskFromChatAs(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, text, clientID, messageID, answerer string) bool {
	return answerAskFrom(ctx, h, pool, sessionID, text, clientID, messageID, answerer, "")
}

// answeredBy is who an answer sent with source is from, for the chat: the parent session
// ("session", a source only a parent's own token can send: SendMessage), or "" for the person.
func answeredBy(source string) string {
	if source == promptSourceSession {
		return promptSourceSession
	}
	return ""
}

// answerAskFrom is answerAskFromChatAs for a message sent with source: the parent's answer to its
// child's ask is recorded as the parent's (askAnswerPayload.AnsweredBy).
func answerAskFrom(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID, text, clientID, messageID, answerer, source string) bool {
	if pool == nil {
		return false
	}
	ownerKnown, isOwner := false, false
	owner := func() bool {
		if !ownerKnown {
			ownerKnown = true
			if answerer != "" {
				row, err := db.GetSession(ctx, pool, sessionID)
				isOwner = err == nil && ownsSession(answerer, row)
			}
		}
		return isOwner
	}
	var target *db.MessageRow
	if messageID != "" {
		row, err := db.GetMessage(ctx, pool, messageID)
		if err != nil || row == nil || row.SessionID != sessionID || row.Kind != "ask" || row.Status != "open" {
			return false
		}
		if row.OwnerOnly && !owner() {
			return false
		}
		target = row
	} else {
		if harnessCommand(text) {
			return false // `/model …`, `/effort …` are for the session, never an answer
		}
		rows, err := db.ListOpenAsks(ctx, pool, sessionID)
		if err != nil {
			log.Printf("answer ask from chat %s: %v", sessionID, err)
			return false
		}
		for i := range rows { // newest first
			r := rows[i]
			// A question the agent did not wait on is answered only by name (its card, its Needs
			// you entry): the agent reads a chat message itself (questions.go).
			if r.NoWait || r.OwnerOnly && !owner() {
				continue
			}
			if ok, err := db.HasAgentEvent(ctx, pool, sessionID, messageEventID(r.ID, "ask")); err == nil && ok {
				target = &r
				break
			}
		}
		if target == nil {
			return false
		}
	}
	recorded, lookupErr := db.HasAgentEvent(ctx, pool, sessionID, messageEventID(target.ID, "ask"))
	if lookupErr != nil {
		return true
	}
	// The answer, its chat record, and the input receipt commit together. A daemon
	// reconnect must never turn an already-answered question into another engine input.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return true
	} // accepted input remains pending for retry
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE messages SET answer=$2,status='answered',answered_at=now() WHERE id=$1 AND status='open'`, target.ID, text)
	if err != nil {
		return true
	}
	if tag.RowsAffected() == 0 {
		return false
	}
	payload, err := json.Marshal(askAnswerPayload{Text: text, Source: "ask_answer", AnsweredBy: answeredBy(source), MessageID: target.ID, ClientID: clientID, TurnID: askTurnID(ctx, tx, sessionID, target.ID)})
	if err != nil {
		return true
	}
	at := time.Now()
	eventID := messageEventID(target.ID, "answer")
	var seq int64
	var fresh bool
	if recorded {
		seq, fresh, err = db.AppendAgentEventTx(ctx, tx, sessionID, eventID, "user_message", string(protocol.StripNUL(payload)), at)
		if err != nil {
			return true
		}
	}
	if clientID != "" {
		if _, err := tx.Exec(ctx, `UPDATE session_inputs SET settled_at=COALESCE(settled_at,now()) WHERE session_id=$1 AND client_id=$2`, sessionID, clientID); err != nil {
			return true
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return true
	}
	if fresh {
		fanOutServerEvent(h, sessionID, eventID, "user_message", protocol.StripNUL(payload), seq, at)
	}
	h.SignalAskAnswered(target.ID)
	row, err := db.GetMessage(ctx, pool, target.ID)
	if err != nil || row == nil {
		return true
	}
	h.QuestionAnswered(*row, text, answerer)

	h.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)})
	return true
}

// answersAsk reports whether a message sent through the runner API with this source may answer an
// open ask: a person's (human, chat), an API caller's (runner) or a parent session's for its child
// (session), never a board's automation.
func answersAsk(source string) bool {
	switch source {
	case "human", "chat", "runner", "session":
		return true
	}
	return false
}

// harnessCommand reports whether a chat message is one of the commands the daemon itself handles
// (AgentHost.UserInput): a model or effort change.
func harnessCommand(text string) bool {
	return topics.HarnessCommand(text)
}

// SetAskSignal registers how the hub wakes an `ask` answer poll (the API's waiters). Called once,
// by NewAPI.
func (h *Hub) SetAskSignal(fn func(messageID string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.askSignal = fn
}

// SignalAskAnswered wakes a poll waiting for the answer to messageID. A no-op without a registered
// signal (the poll re-reads the database on its own within its hold).
func (h *Hub) SignalAskAnswered(messageID string) {
	h.mu.RLock()
	fn := h.askSignal
	h.mu.RUnlock()
	if fn != nil {
		fn(messageID)
	}
}
