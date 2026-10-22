package server

// Questions the agent does not wait on: `blerg-runner ask --no-wait "<question>"`.
//
// A blocking `blerg-runner ask` holds the engine inside the command until the answer comes back on
// its stdout. A question is posted the same way (an ask in the messages table and the transcript,
// listed in the session's Needs you and the bell), but the CLI returns at once and the agent keeps
// working. So:
//
//   - the person answers it from its Needs you entry or its card (an answer addressed to it by
//     message id); a message they type in the chat goes to the agent as itself and never answers a
//     question by accident (answerAskFrom skips no_wait asks when no message id is given);
//   - like every answer, it is recorded as the server's user_message{source: "ask_answer",
//     message_id} (never a client's), and then sent to the agent as a message in Blerg's own voice
//     (source "question_answer", reserved: no caller can write it), naming the question it answers;
//   - only the session's owner answers it (it is created owner-only); the session's own token
//     closes it without an answer (`blerg-runner ask --resolve <id>`, POST /api/messages/{id}/expire)
//     when the person answered in the chat instead;
//   - a daemon restart does not close it (db.CloseOpenAsksForSession), the session's end does.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// questionOwners maps each session of rows that holds an open no_wait question to its owner (the
// spawning account), "" when it has none. Read once per session.
func questionOwners(ctx context.Context, pool *pgxpool.Pool, rows []db.MessageRow) map[string]string {
	owners := map[string]string{}
	if pool == nil {
		return owners
	}
	for _, r := range rows {
		if !r.NoWait || r.Kind != "ask" {
			continue
		}
		if _, ok := owners[r.SessionID]; ok {
			continue
		}
		sess, err := db.GetSession(ctx, pool, r.SessionID)
		owners[r.SessionID] = ""
		if err == nil && sess != nil {
			owners[r.SessionID] = derefOrEmpty(sess.SpawningAccountID)
		}
	}
	return owners
}

// messageInfosFor is the messages as viewer sees them: a no_wait question is answerable only by
// its session's owner (account "" is nobody).
func messageInfosFor(ctx context.Context, pool *pgxpool.Pool, rows []db.MessageRow, viewer string) []protocol.MessageInfo {
	owners := questionOwners(ctx, pool, rows)
	out := make([]protocol.MessageInfo, 0, len(rows))
	for _, r := range rows {
		info := messageRowToInfo(r)
		if r.NoWait && viewer != "" && owners[r.SessionID] == viewer {
			info.Answerable = true
		}
		out = append(out, info)
	}
	return out
}

// noWaitQuestion reports whether messageID is an open question of the session the agent did not
// wait on.
func noWaitQuestion(ctx context.Context, pool *pgxpool.Pool, sessionID, messageID string) bool {
	if pool == nil {
		return false
	}
	row, err := db.GetMessage(ctx, pool, messageID)
	return err == nil && row != nil && row.SessionID == sessionID && row.NoWait && row.Status == "open"
}

// redeliverQuestionAnswers hands the session's answered but never delivered questions to the
// delivery again (db.UndeliveredQuestionAnswers). Called on the daemon's heartbeat for a host that
// takes input receipts, so a delivery always leaves its outbox row and is not repeated.
func redeliverQuestionAnswers(ctx context.Context, h *Hub, pool *pgxpool.Pool, sessionID string) {
	rows, err := db.UndeliveredQuestionAnswers(ctx, pool, sessionID)
	if err != nil {
		log.Printf("question answers %s: %v", sessionID, err)
		return
	}
	for _, row := range rows {
		h.QuestionAnswered(row, derefOrEmpty(row.Answer), "")
	}
}

// questionAnswerSource is the source of the message that brings a question's answer to the agent.
const questionAnswerSource = "question_answer"

// questionQuoteMax caps the question quoted back to the agent with its answer.
const questionQuoteMax = 160

// questionAnswerText is what the agent reads when the person answers one of its questions:
//
//	Answer to your question q-123 ("Ship the blue one?"): yes, the blue one
func questionAnswerText(row db.MessageRow, answer string) string {
	return "Answer to your question " + row.ID + " (" + questionQuote(row) + "): " + answer
}

// questionQuote is the question as it is quoted back: one line, at most questionQuoteMax.
func questionQuote(row db.MessageRow) string { return quotedLine(row.Body, questionQuoteMax) }

// SetQuestionAnswered registers what the hub does once a question the agent did not wait on is
// answered (the API's deliverQuestionAnswer). Called once, by NewAPI.
func (h *Hub) SetQuestionAnswered(fn func(row db.MessageRow, answer, answerer string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.questionAnswered = fn
}

// SetQuestionSessionEnded registers what the hub does when a session ends, for its questions'
// delivery state (the API's questionAttempts). Called once, by NewAPI.
func (h *Hub) SetQuestionSessionEnded(fn func(sessionID string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.questionSessionEnded = fn
}

// QuestionSessionEnded tells the registered handler that sessionID ended.
func (h *Hub) QuestionSessionEnded(sessionID string) {
	h.mu.RLock()
	fn := h.questionSessionEnded
	h.mu.RUnlock()
	if fn != nil {
		fn(sessionID)
	}
}

// QuestionAnswered hands a just-answered (or dismissed) question to the registered delivery. A no-op
// for an ask the agent waits on (its CLI reads the answer), an empty answer (closed, not answered,
// unless the person dismissed it) or without a registered delivery.
func (h *Hub) QuestionAnswered(row db.MessageRow, answer, answerer string) {
	if !row.NoWait || row.Kind != "ask" || (answer == "" && !row.Dismissed) {
		return
	}
	h.mu.RLock()
	fn := h.questionAnswered
	h.mu.RUnlock()
	if fn != nil {
		fn(row, answer, answerer)
	}
}

// deliverQuestionAnswer sends the answer to the session as a message, after the request that
// answered it: that request holds the session's input locks, which delivery takes again. Durable
// (replayed when the daemon reconnects) for an agent session whose host takes receipts; a note
// reply for a terminal session.
func (a *API) deliverQuestionAnswer(row db.MessageRow, answer, answerer string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		a.sendQuestionAnswer(ctx, row, answer, answerer)
	}()
}

// sendQuestionAnswer delivers row's answer, or, for a question the person dismissed, the dismissal
// (questionDismissedText under its own client id): the same durable relay, retries and give-up.
func (a *API) sendQuestionAnswer(ctx context.Context, row db.MessageRow, answer, answerer string) {
	text := questionAnswerText(row, answer)
	if row.Dismissed {
		text = questionDismissedText(row)
	}
	sess, err := db.GetSession(ctx, a.dbPool, row.SessionID)
	if err != nil || sess == nil {
		log.Printf("question %s: session: %v", row.ID, err)
		return
	}
	if sess.Kind != "agent" {
		a.routeNoteReply(db.MessageRow{SessionID: row.SessionID, Body: "question " + row.ID}, text)
		return
	}
	if answerer == "" {
		answerer = derefOrEmpty(sess.SpawningAccountID)
	}
	principal := runnerPrincipal{Kind: "human", Sub: answerer}
	clientID := questionRelayClientID(row)
	if dc := a.hub.FindDaemonForSession(row.SessionID); dc == nil || dc.CanInputReceipts() {
		if apiErr := a.sendRelay(ctx, principal, row.SessionID, text, questionAnswerSource, clientID); apiErr != nil {
			a.questionAnswerFailed(ctx, row, apiErr)
			return
		}
		a.questionAttempts.forget(row.ID)
		return
	}
	// A host without input receipts takes the answer live and leaves no outbox row. Record the
	// delivery as settled, so a heartbeat after that host is upgraded does not send it again.
	if apiErr := a.sendRelay(ctx, principal, row.SessionID, text, questionAnswerSource); apiErr != nil {
		log.Printf("question %s: deliver to %s: %s", row.ID, row.SessionID, apiErr.Message)
		return
	}
	frame, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: row.SessionID, Text: text, Source: questionAnswerSource, ClientID: clientID})
	if _, _, err := db.RecordSettledSessionInput(ctx, a.dbPool, row.SessionID, clientID, string(protocol.StripNUL(frame)),
		messageEventID(clientID, "delivered-live"), ""); err != nil {
		// Rare, and accepted: the answer did reach the agent, but if this host is upgraded to one
		// with input receipts within the day, its first heartbeat delivers the answer once more.
		log.Printf("ERROR question %s: answer delivered to %s without receipts, but the delivery could not be recorded; it may be delivered again after a daemon upgrade: %v", row.ID, row.SessionID, err)
	}
}

// questionAnswerClientID is the outbox id of a question's answer: one per question, so a delivery
// is never queued twice.
func questionAnswerClientID(messageID string) string { return "question-" + messageID }

// questionDismissedClientID is the outbox id of a question's dismissal (card #155).
func questionDismissedClientID(messageID string) string { return "question-dismissed-" + messageID }

// questionRelayClientID is the outbox id of what row brings the agent: its dismissal or its answer.
func questionRelayClientID(row db.MessageRow) string {
	if row.Dismissed {
		return questionDismissedClientID(row.ID)
	}
	return questionAnswerClientID(row.ID)
}

// questionRelayWhat names what is being delivered, for the owner: "Your answer to question …" or
// "Your dismissal of question …".
func questionRelayWhat(row db.MessageRow) string {
	if row.Dismissed {
		return "Your dismissal of question " + row.ID + " (" + questionQuote(row) + ")"
	}
	return "Your answer to question " + row.ID + " (" + questionQuote(row) + ")"
}

// maxQuestionAnswerAttempts is how many permanent delivery failures of one answer (the first
// delivery, then one per heartbeat of the session's host) are tried before giving up on it.
const maxQuestionAnswerAttempts = 3

// questionAnswerDelayNotice is how long transient failures of one answer last before the owner is
// told, once, that it is still waiting to be delivered.
const questionAnswerDelayNotice = 10 * time.Minute

// questionAnswerRetryWindow is how long an answer is retried at all (UndeliveredQuestionAnswers:
// answered within the last day). State older than that is dropped.
const questionAnswerRetryWindow = 24 * time.Hour

// questionAttempts is the delivery state of each question whose answer failed to deliver: its
// permanent failures, and since when transient ones have lasted. It is kept in memory on purpose,
// and a server restart starts it again: the retries are then bounded by attempts times restarts
// within the day an answer is retried, and a durable count would need a migration. (The "still
// waiting" notice is recorded under a fixed event id, so a restart never repeats it.) An entry goes
// when the answer is delivered or given up, when its session ends, and in any case once it is older
// than questionAnswerRetryWindow.
type questionAttempts struct {
	mu   sync.Mutex
	byID map[string]*questionDelivery
	now  func() time.Time // nil: time.Now
}

type questionDelivery struct {
	sessionID      string
	permanent      int
	firstTransient time.Time // zero until a transient failure
	noticed        bool      // the owner was told it is still waiting
	since          time.Time // first failure
}

func (q *questionAttempts) clock() time.Time {
	if q.now != nil {
		return q.now()
	}
	return time.Now()
}

// entryLocked returns id's state, making it, after dropping state past the retry window.
func (q *questionAttempts) entryLocked(id, sessionID string, now time.Time) *questionDelivery {
	if q.byID == nil {
		q.byID = map[string]*questionDelivery{}
	}
	for k, d := range q.byID {
		if now.Sub(d.since) > questionAnswerRetryWindow {
			delete(q.byID, k)
		}
	}
	d := q.byID[id]
	if d == nil {
		d = &questionDelivery{sessionID: sessionID, since: now}
		q.byID[id] = d
	}
	return d
}

// permanentFailure counts a permanent failure of id's delivery and returns the count.
func (q *questionAttempts) permanentFailure(id, sessionID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	d := q.entryLocked(id, sessionID, q.clock())
	d.permanent++
	return d.permanent
}

// transientFailure records a transient failure of id's delivery. notice is true once, when such
// failures have lasted questionAnswerDelayNotice.
func (q *questionAttempts) transientFailure(id, sessionID string) (notice bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.clock()
	d := q.entryLocked(id, sessionID, now)
	if d.firstTransient.IsZero() {
		d.firstTransient = now
	}
	if !d.noticed && now.Sub(d.firstTransient) >= questionAnswerDelayNotice {
		d.noticed = true
		return true
	}
	return false
}

func (q *questionAttempts) forget(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.byID, id)
}

// forgetSession drops the state of every question of a session that ended: nothing retries them.
func (q *questionAttempts) forgetSession(sessionID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for k, d := range q.byID {
		if d.sessionID == sessionID {
			delete(q.byID, k)
		}
	}
}

func (q *questionAttempts) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.byID)
}

// permanentDeliveryFailure reports whether a delivery failure would fail the same way on every
// retry. Only a refusal about the target itself counts: it is gone (404, 410), it ended (409: the
// session or its parent ended, or the id was used for other content), or the answer can never be
// queued (422: too large once the question is quoted, an invalid id). Everything else can clear by
// itself and never uses up an attempt: a full queue (429), any 5xx (continuation busy, a database
// error, a resume that could not start), and any other 4xx.
func permanentDeliveryFailure(apiErr *APIError) bool {
	if apiErr == nil {
		return false
	}
	switch apiErr.Status {
	case http.StatusNotFound, http.StatusGone, http.StatusConflict, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// questionAnswerFailed handles a failed delivery of row's answer. A transient failure is only
// logged: the next heartbeat of the session's host tries again. A permanent one is counted, and
// after maxQuestionAnswerAttempts the answer is given up: it is recorded as settled
// (UndeliveredQuestionAnswers no longer lists it, so no heartbeat retries or logs it again) and the
// transcript says so, for the owner to send the answer as a message instead.
func (a *API) questionAnswerFailed(ctx context.Context, row db.MessageRow, apiErr *APIError) {
	permanent := permanentDeliveryFailure(apiErr)
	if a.questionDeliveryFailed != nil {
		defer a.questionDeliveryFailed(row.ID, permanent)
	}
	clientID := questionRelayClientID(row)
	reason := strings.TrimSuffix(strings.TrimSpace(apiErr.Message), ".")
	if !permanent {
		log.Printf("question %s: deliver to %s (will retry): %s", row.ID, row.SessionID, apiErr.Message)
		if a.questionAttempts.transientFailure(row.ID, row.SessionID) && a.dbPool != nil {
			// Once, under a fixed id (a restart does not repeat it): the owner knows the answer has
			// not reached the agent yet, and can send it as a message instead of waiting.
			recordServerAgentEvent(ctx, a.hub, a.dbPool, row.SessionID, messageEventID(clientID, "input-delayed"), "error", map[string]any{
				"message": questionRelayWhat(row) + " is still waiting to be delivered to the agent: " +
					reason + ". Blerg keeps trying; you can also send it to the agent as a chat message.",
				"retryable": true,
			})
		}
		return
	}
	n := a.questionAttempts.permanentFailure(row.ID, row.SessionID)
	log.Printf("question %s: deliver to %s (attempt %d of %d): %s", row.ID, row.SessionID, n, maxQuestionAnswerAttempts, apiErr.Message)
	if n < maxQuestionAnswerAttempts || a.dbPool == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"message":   questionRelayWhat(row) + " could not be delivered to the agent: " + reason + ". Send it to the agent as a chat message.",
		"retryable": false,
	})
	frame, _ := json.Marshal(map[string]any{"type": "agent_user_message", "session_id": row.SessionID, "source": questionAnswerSource, "client_id": clientID, "undelivered": true})
	eventID := messageEventID(clientID, "input-undelivered")
	seq, fresh, err := db.RecordSettledSessionInput(ctx, a.dbPool, row.SessionID, clientID, string(frame), eventID, string(payload))
	if err != nil {
		log.Printf("question %s: give up on delivery: %v", row.ID, err)
		return
	}
	a.questionAttempts.forget(row.ID)
	if fresh {
		log.Printf("question %s: gave up delivering the answer to %s after %d attempts", row.ID, row.SessionID, n)
		fanOutServerEvent(a.hub, row.SessionID, eventID, "error", payload, seq, time.Now())
	}
}

// ─── dismiss (the person, from Needs you) ─────────────────────────────────────

// questionDismissedText is what the agent reads when the person dismisses a question it did not
// wait on (a question it waits on gets the dismissal from its CLI: exit code 4).
func questionDismissedText(row db.MessageRow) string {
	return "The person dismissed your question " + row.ID + " (" + questionQuote(row) + "); they may have answered in the chat instead. Ask again if you still need an answer."
}

// HandlePostMessageDismiss is POST /api/messages/{id}/dismiss: the person closes an open question
// from Needs you without answering it (they may have answered in the chat). Only a person signed in
// as themselves who may answer it: the session's owner for an owner-only question, anyone who can
// see the session otherwise. The session's own credential, the daemon, board and agent tokens are
// refused (the agent closes its own questions with `blerg-runner ask --resolve`). An engine's
// command approval cannot be dismissed: it is allowed or denied. 409 when it is no longer open.
func (a *API) HandlePostMessageDismiss(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	boardID, err := db.MessageBoardID(ctx, a.dbPool, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		log.Printf("api dismiss resolve board %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	actor, ok := a.authBrowserOrBoard(w, r, boardID, "session.start")
	if !ok {
		return
	}
	person := coreAnswerer(actor)
	if person == "" {
		writeError(w, http.StatusForbidden, "only the person can dismiss a question (the agent closes its own with blerg-runner ask --resolve)")
		return
	}
	if !a.coreActorCanSeeMessage(ctx, actor.Core, id) {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}
	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "message unavailable")
		return
	}
	if row == nil || row.Kind != "ask" {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}
	if row.OwnerOnly && !row.NoWait {
		writeError(w, http.StatusConflict, "an approval is allowed or denied, not dismissed")
		return
	}
	if row.OwnerOnly && !a.actorOwnsMessageSession(ctx, actor, row.SessionID) {
		writeError(w, http.StatusForbidden, "only the session's owner can dismiss this question")
		return
	}
	// Under the same locks as a reply: a discarded child's or an ended parent's question is refused
	// (409), and a pending limit resume does not race it.
	err = withCancelledLimitResume(ctx, a.dbPool, row.SessionID, func() error {
		return withParentInputGuard(ctx, a.dbPool, row.SessionID, func() error {
			a.dismissMessageUnlocked(w, r, id, person)
			return nil
		})
	})
	if errors.Is(err, db.ErrDelegationEnded) {
		writeError(w, http.StatusConflict, "session was discarded or its parent ended")
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, "dismiss is busy; retry shortly")
	}
}

// dismissMessageUnlocked closes the question as dismissed by person and tells whoever waits.
func (a *API) dismissMessageUnlocked(w http.ResponseWriter, r *http.Request, id, person string) {
	ctx := r.Context()
	changed, err := db.DismissMessage(ctx, a.dbPool, id, person)
	if err != nil {
		log.Printf("api dismiss %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if !changed {
		writeError(w, http.StatusConflict, "message already answered or not open")
		return
	}
	a.waiters.signal(id)
	log.Printf("question %s dismissed by %s", id, person)
	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil || row == nil {
		// The dismissal stands; the next heartbeat delivers it (db.UndeliveredQuestionAnswers).
		log.Printf("api dismiss %s: read back: %v", id, err)
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "dismissed": true})
		return
	}
	// A question the agent did not wait on hears of it in a Blerg message, delivered like an answer:
	// queued in the session's outbox, and redelivered on a heartbeat if it never got there
	// (db.UndeliveredQuestionAnswers). One it waits on hears from its CLI (exit code 4).
	a.hub.QuestionAnswered(*row, "", person)
	a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)})
	a.hub.ChildActivity(row.SessionID)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "dismissed": true})
}
