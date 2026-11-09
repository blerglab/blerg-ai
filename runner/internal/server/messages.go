package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxAnswerHold caps how long a single /answer request blocks. The CLI polls in a
// loop, so this stays well under the ingress proxy-read-timeout; correctness comes
// from the per-call DB re-read, not from holding the request open.
const maxAnswerHold = 25 * time.Second

// messageWaiters lets the reply/expire handlers wake an in-flight /answer poll. It
// is a pure latency optimization over the DB re-read — a missed signal (restart,
// dropped socket) just means the next poll re-reads the persisted answer.
type messageWaiters struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func newMessageWaiters() *messageWaiters {
	return &messageWaiters{m: make(map[string]chan struct{})}
}

// channel returns the wait channel for id, creating it on first wait.
func (w *messageWaiters) channel(id string) chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch, ok := w.m[id]
	if !ok {
		ch = make(chan struct{})
		w.m[id] = ch
	}
	return ch
}

// signal wakes any waiter for id and removes the channel. No-op if none waiting.
func (w *messageWaiters) signal(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ch, ok := w.m[id]; ok {
		close(ch)
		delete(w.m, id)
	}
}

// drop removes a waiter's channel without closing it — used when a poll times out so
// the map doesn't accumulate channels for never-answered messages. A concurrent
// straggler still holding the channel ref just falls back to its DB re-read.
func (w *messageWaiters) drop(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.m, id)
}

// defaultMessageLimit bounds the roll-up feed (open messages always included; this
// caps the recent-history tail) for both the list endpoint and initial_state.
const defaultMessageLimit = 100

var validMessageKinds = map[string]bool{"update": true, "ask": true, "note": true}

type postMessageRequest struct {
	SessionPosted  bool          `json:"-"`
	Urgent         bool          `json:"urgent,omitempty"`
	Subject        *boundSubject `json:"subject,omitempty"`
	PersonRequired bool          `json:"person_required,omitempty"`
	TurnID         string        `json:"turn_id,omitempty"`
	SessionID      string        `json:"session_id"`
	Kind           string        `json:"kind"`
	Body           string        `json:"body"`
	// OwnerOnly (asks only): the answer is accepted only from the session's
	// signed-in owner. The daemon sets it on an engine's command approval.
	OwnerOnly bool `json:"owner_only,omitempty"`
	// NoWait (asks only): a question the agent does not wait on (`blerg-runner ask --no-wait`,
	// questions.go). It is owner-only, and its answer is sent to the session as a message.
	NoWait bool `json:"no_wait,omitempty"`
	// HostGeneration (asks only): the hosting that asked, set by the daemon or from the CLI's
	// environment. Claiming a higher one only keeps a question open through a restart.
	HostGeneration int64 `json:"host_generation,omitempty"`
}

// HandlePostMessages creates a session→user message (bearer-authed, since the CLI
// on the daemon host calls it). Broadcasts message_created and pushes per policy.
// Accepts either the master daemon token (session_id read from body) or a board
// token with the "message" capability (session_id derived from the token, body
// value ignored).
func (a *API) HandlePostMessages(w http.ResponseWriter, r *http.Request) {
	boardSessionID, ok := a.authMessaging(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	var req postMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}
	// Board-token path: override session_id from the token (per spec).
	if boardSessionID != "" {
		req.SessionID = boardSessionID
	}
	if !validMessageKinds[req.Kind] || (req.Body == "" && req.Subject == nil) || req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id, body and a valid kind (update|ask|note) are required")
		return
	}
	ctx := r.Context()
	sess, err := db.GetSession(ctx, a.dbPool, req.SessionID)
	if err != nil {
		log.Printf("api post message GetSession: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	if (req.NoWait || req.Urgent) && req.Kind != "ask" {
		writeError(w, http.StatusBadRequest, "no_wait and urgent are for an ask only")
		return
	}
	if req.NoWait && (sess.SpawningAccountID == nil || *sess.SpawningAccountID == "") {
		writeError(w, http.StatusConflict, "a question that does not wait needs a session started by a signed-in person, who answers it; ask and wait instead (blerg-runner ask without --no-wait)")
		return
	}
	if req.OwnerOnly && req.Kind == "ask" && (sess.SpawningAccountID == nil || *sess.SpawningAccountID == "") {
		writeError(w, http.StatusConflict, "owner-only approvals are unavailable for runner-key sessions; start the session while signed in to answer approvals")
		return
	}

	// Destructive phrases are a worker's routing rule; a plain top-level ask stays a plain ask.
	req.PersonRequired = req.requiresPerson(hasCoordinator(sess))
	req.HostGeneration = max(req.HostGeneration, 0)
	req.SessionPosted = boardSessionID != ""
	var id string
	switch {
	case req.Subject != nil:
		id, err = a.createBoundAsk(ctx, req, sess, boardSessionID == sess.ID)
	case req.Kind == "ask" && req.SessionPosted:
		id, err = db.CreateHostedAskWithSource(ctx, a.dbPool, req.SessionID, req.Body, req.OwnerOnly || req.NoWait, req.NoWait, req.PersonRequired, req.HostGeneration, req.Urgent, true)
	case req.NoWait:
		id, err = db.CreateHostedAsk(ctx, a.dbPool, req.SessionID, req.Body, true, true, req.PersonRequired, req.HostGeneration, req.Urgent)
	case req.OwnerOnly && req.Kind == "ask":
		id, err = db.CreateHostedAsk(ctx, a.dbPool, req.SessionID, req.Body, true, false, req.PersonRequired, req.HostGeneration, req.Urgent)
	case boardSessionID == "" && req.Kind == "ask" && sess.Kind == "agent" && sess.Engine != nil && *sess.Engine == "codex":
		id, err = a.createCodexQuestion(ctx, req)
	case req.Kind == "ask":
		id, err = db.CreateHostedAsk(ctx, a.dbPool, req.SessionID, req.Body, false, false, req.PersonRequired, req.HostGeneration, req.Urgent)
	default:
		id, err = db.CreateMessage(ctx, a.dbPool, req.SessionID, req.Kind, req.Body, req.PersonRequired)
	}
	if err != nil {
		if req.Subject != nil {
			blackboardError(w, err)
			return
		}
		logPersonTimeFailure("create_ask", err)
		failurelog.SetCause(w, err)
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}

	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil || row == nil {
		logPersonTimeFailure("reload_ask", err)
		failurelog.SetCause(w, err)
		writeError(w, http.StatusInternalServerError, "reload failed")
		return
	}
	info := messageRowToInfo(*row)
	// An ask from a session nobody attends is listed, not toasted (notices.go).
	quiet := (row.Kind == "note" && hasCoordinator(sess)) || row.Kind == "ask" && !questionCallsOwner(sess, *row) && (headlessSession(sess) || hasCoordinator(sess))
	if row.Kind == "ask" {
		// Pending child questions stay quiet; top-level no_wait questions notify only their owner.
		// Other viewers of a shared session keep a quiet, read-only copy.
		owner := derefOrEmpty(sess.SpawningAccountID)
		a.hub.BroadcastJSONPerAccount(func(account string) any { //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
			mine := owner != "" && account == owner
			view := info
			view.Answerable = mine && questionCallsOwner(sess, *row)
			return protocol.MessageCreated{Type: "message_created", Message: view, Quiet: quiet || (!mine && (row.NoWait || hasCoordinator(sess)))}
		})
	} else {
		a.hub.BroadcastJSON(protocol.MessageCreated{Type: "message_created", Message: info, Quiet: quiet}) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
	}
	if boardSessionID != "" {
		// `blerg-runner` (the session's own token): shown in an agent session's chat
		// (messageevents.go). The native loop posts with the daemon token and records its own.
		a.recordMessageEvent(ctx, *sess, *row)
	}
	if row.Kind == "ask" {
		// A question is its own notice: a deferred idle notice would only repeat it.
		a.hub.CancelIdleNotice(sess.ID)
		// Every child question tells the parent who must answer it, including
		// no-wait questions. Only answerable pending questions invite a parent reply.
		if hasCoordinator(sess) {
			var routeErr error
			routingOff := a.questionRoutingOff(sess.ID)
			if routingOff {
				a.noteRoutingOff(ctx, sess.ID)
			}
			switch {
			case routingOff:
				routeErr = a.routeUnroutedChildQuestion(ctx, sess, id)
			default:
				routeErr = a.reconcileChildQuestions(ctx, time.Now(), id)
			}
			if routeErr != nil {
				logPersonTimeFailure("child_question_routing", routeErr)
			}
		}
	}
	if row.Kind == "note" && hasCoordinator(sess) {
		if err := a.deliverChildNote(ctx, id); err != nil {
			failurelog.Default.Event("runner:child-notes:deliver", slog.LevelError, "component", "runner", "operation", "child_note_deliver", "cause", failurelog.Cause(err))
		}
	}
	a.pushForMessage(*sess, *row) //nolint:contextcheck // push delivery is best-effort background work with no tie to this request

	result := map[string]string{"id": id}
	if row.Kind == "ask" && hasCoordinator(sess) {
		result["recipient"] = "parent"
		fresh, err := db.GetMessage(ctx, a.dbPool, id)
		if err == nil && fresh != nil && fresh.ChildEscalated {
			result["recipient"] = "person"
		}
	}
	writeJSON(w, http.StatusCreated, result)
}

// pushForMessage sends a web push for a new message. ask/note always push (they need
// the user). Child and no_wait asks target their owner, including private sessions;
// other headless asks stay quiet. An update pushes only
// when the user is away (matching the session-state push policy), so progress
// chatter doesn't buzz while they're watching.
func (a *API) pushForMessage(sess db.SessionRow, msg db.MessageRow) {
	if hasCoordinator(&sess) && (msg.Kind == "note" || (msg.Kind == "ask" && !msg.ChildEscalated)) {
		return
	}
	if msg.Kind == "ask" && questionCallsOwner(&sess, msg) {
		SendPushToAccount(a.dbPool, derefOrEmpty(sess.SpawningAccountID), sessionLabel(derefOrEmpty(sess.Title), sess.Repo, sess.ID)+" — question", msg.Body, "/sessions/"+sess.ID)
		return
	}
	// Nobody attends a headless session: its question calls no one (notices.go), until a child's
	// is escalated. The send below stays account-scoped.
	if msg.Kind == "ask" && headlessSession(&sess) && !msg.ChildEscalated && !msg.PersonRequired {
		return
	}
	// A malformed legacy no_wait row without an owner must never fall back to a shared push.
	if msg.NoWait {
		return
	}
	var quiet *Hub
	if msg.Kind == "update" {
		quiet = a.hub
	}
	var title string
	if sess.Title != nil {
		title = *sess.Title
	}
	label := sessionLabel(title, sess.Repo, sess.ID)
	heading := label
	switch msg.Kind {
	case "ask":
		heading = label + " — question"
	case "note":
		heading = label + " — note"
	}
	// Land on the session, where the user can read and reply.
	SendPushForSession(a.dbPool, sess.ID, heading, msg.Body, "/sessions/"+sess.ID, quiet)
}

type answerResponse struct {
	Discussion      string         `json:"discussion,omitempty"`
	DecisionReceipt map[string]any `json:"decision_receipt,omitempty"`
	QuestionID      string         `json:"question_id"`
	Outcome         string         `json:"outcome,omitempty"`
	AnsweredBy      string         `json:"answered_by,omitempty"`
	// AnswerPrefix labels a child's free-text answer for its model. It is never part of Answer.
	AnswerPrefix string  `json:"answer_prefix,omitempty"`
	Answered     bool    `json:"answered"`
	Answer       *string `json:"answer"` // nil/'' distinguishes a closed message from a real empty reply
	// Dismissed: the person closed it from Needs you without answering (the CLI exits 4).
	Dismissed bool `json:"dismissed,omitempty"`
	Withdrawn bool `json:"withdrawn,omitempty"`
}

// HandleGetMessageAnswer is the `ask` short-hold poll: returns immediately if the
// message is already answered, else blocks up to ?wait (capped at maxAnswerHold) for
// a signal, then re-reads the DB and returns. Accepts daemon token or board token
// with the "message" capability.
func (a *API) HandleGetMessageAnswer(w http.ResponseWriter, r *http.Request) {
	boardSessionID, ok := a.authMessaging(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()

	// Board-token path: enforce session ownership — the token's session must
	// own the addressed message.
	if boardSessionID != "" {
		row, err := db.GetMessage(ctx, a.dbPool, id)
		if err != nil {
			log.Printf("api answer ownership check GetMessage: %v", err)
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		if row == nil {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		if row.SessionID != boardSessionID {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
	}

	// Only an authenticated CLI poll renews the lease. It covers the maximum
	// long poll plus 20 seconds for reconnects, including a lost response.
	if boardSessionID != "" && r.URL.Query().Get("waiter") == "cli" {
		if _, err := a.dbPool.Exec(ctx, `UPDATE messages SET cli_poll_until=now()+interval '45 seconds' WHERE id=$1 AND status='open' AND NOT cli_wait_released`, id); err != nil {
			failurelog.Default.Event("cli_waiter_lease", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
			writeError(w, http.StatusServiceUnavailable, "Question waiter unavailable.")
			return
		}
	}

	if done, resp := a.answeredNow(ctx, w, id, boardSessionID != "" && r.URL.Query().Get("waiter") == "cli"); done {
		if resp != nil {
			writeJSON(w, http.StatusOK, resp)
		}
		return
	}

	hold := a.messageAnswerHold(r)
	ch := a.waiters.channel(id)
	timer := time.NewTimer(hold)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		a.waiters.drop(id) // don't leak the channel for a never-answered message
	case <-ctx.Done():
		return
	}
	// Re-read after waking (or timing out) — the DB is the source of truth.
	if done, resp := a.answeredNow(ctx, w, id, boardSessionID != "" && r.URL.Query().Get("waiter") == "cli"); done {
		if resp != nil {
			writeJSON(w, http.StatusOK, resp)
		}
		return
	}
	writeJSON(w, http.StatusOK, answerResponse{Answered: false})
}

func (a *API) messageAnswerHold(r *http.Request) time.Duration {
	hold := a.answerHold
	if hold <= 0 {
		hold = maxAnswerHold
	}
	if q := r.URL.Query().Get("wait"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			if d := time.Duration(n) * time.Second; d < hold {
				hold = d
			}
		}
	}
	return hold
}

// answeredNow reads the message; returns done=true with a response when the message
// is answered or missing (writing a 404 itself in the missing case → resp nil).
func (a *API) answeredNow(ctx context.Context, w http.ResponseWriter, id string, cli ...bool) (bool, *answerResponse) {
	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil {
		log.Printf("api answer GetMessage: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return true, nil
	}
	if row == nil {
		writeError(w, http.StatusNotFound, "message not found")
		return true, nil
	}
	if row.Status == "withdrawn" {
		return true, &answerResponse{QuestionID: row.ID, Withdrawn: true, Outcome: "withdrawn"}
	}
	if len(cli) > 0 && cli[0] && row.CLIWaitReleased && row.Status == "answered" {
		return true, cliDiscussionResponse(id)
	}

	if row.Status == "answered" {
		if len(cli) > 0 && cli[0] {
			// A final answer consumed by the CLI must not later look abandoned.
			var consumed string
			err := a.dbPool.QueryRow(ctx, `UPDATE messages SET cli_poll_until=NULL WHERE id=$1 AND NOT cli_wait_released RETURNING id::text`, id).Scan(&consumed)
			if errors.Is(err, pgx.ErrNoRows) {
				return true, cliDiscussionResponse(id)
			}
			if err != nil {
				a.threadError(w, err)
				return true, nil
			}
		}
		outcome := "answered"
		prefix := ""
		switch {
		case row.Dismissed:
			outcome = "dismissed"
		case derefOrEmpty(row.Answer) == "":
			outcome = "closed"
		default:
			prefix = childAnswerLabel(ctx, a.dbPool, *row)
		}
		receipt, _, err := a.boundReceipt(ctx, id)
		if err != nil {
			writeError(w, 503, "Decision receipt unavailable.")
			return true, nil
		}
		return true, &answerResponse{DecisionReceipt: receipt, QuestionID: row.ID, Outcome: outcome, Answered: true, Answer: row.Answer, Dismissed: row.Dismissed, AnsweredBy: row.AnsweredBy, AnswerPrefix: prefix}
	}
	if len(cli) > 0 && cli[0] {
		var consumed string
		err := a.dbPool.QueryRow(ctx, `UPDATE messages SET cli_discussion='',cli_wait_released=true WHERE id=$1 AND question_session_posted AND status='open' AND NOT no_wait AND (cli_discussion<>'' OR cli_wait_released) RETURNING id::text`, id).Scan(&consumed)
		if err == nil {
			return true, cliDiscussionResponse(id)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			a.threadError(w, err)
			return true, nil
		}

	}

	return false, nil
}

func cliDiscussionResponse(id string) *answerResponse {
	return &answerResponse{QuestionID: id, Outcome: "discussion", Discussion: "The person replied in the thread; the reply arrives as your next message. Respond with ask --reply " + id + "."}
}

func replyRevision(revision *int64) []int64 {
	if revision == nil {
		return nil
	}
	return []int64{*revision}
}

type postMessageReplyRequest struct {
	RevisionSeq *int64  `json:"revision_seq"`
	Reply       *string `json:"reply,omitempty"`
	Revise      *string `json:"revise,omitempty"`
	ClientID    string  `json:"client_id,omitempty"`
	Answer      string  `json:"answer"`
}

// HandlePostMessageReply answers an open message. Body: {answer string}.
//
// Credential: the daemon token, a core token with session.start, or — when
// the message's session is bound to a board — a board token scoped to that
// board. A message on a session with no board can only be answered by the
// daemon or a core principal. There is no unauthenticated path; a missing
// message is 404 (looked up before the gate so the board scope can be
// resolved, which discloses nothing beyond existence of an opaque UUID).
//
// Conditional update: if the message is already answered or is a kind=update
// (created answered), returns 409 and does nothing. On success, the order is:
// DB commit → signal ask waiter → note routing seam → broadcast message_answered.
func (a *API) HandlePostMessageReply(w http.ResponseWriter, r *http.Request) {
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
		log.Printf("api reply resolve board %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	actor, ok := a.authBrowserOrBoard(w, r, boardID, "session.start")
	if !ok {
		return
	}
	if boardID == "" && !requireCoreOrDaemon(w, actor) {
		return
	}
	// A person may not answer a private session's question unless it is theirs
	// (the daemon token and a session's own board token are the session's side).
	if actor.Core != nil && !a.coreActorCanSeeMessage(ctx, actor.Core, id) {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}

	var req postMessageReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}

	if req.Reply != nil || req.Revise != nil {
		a.handleQuestionThreadWrite(w, r, id, req)
		return
	}
	if (!actor.Daemon || req.Answer != "") && a.handleBoundHTTP(w, r, id, req) {
		return
	}
	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil {
		writeError(w, 503, "message unavailable")
		return
	}
	if row == nil {
		writeError(w, 404, "message not found")
		return
	}
	// A daemon interruption closes a dead waiter without inventing an answer.
	// Native input never calls this path to answer: only the committed poll can.
	if actor.Daemon && req.Answer == "" {
		err := withLimitResumeActionLock(ctx, a.dbPool, row.SessionID, func(_ *pgxpool.Conn) error { a.expireMessageUnlocked(w, r, id, false); return nil })
		if err != nil {
			writeError(w, 503, "question close is busy; retry shortly")
		}
		return
	}
	if childQuestionPending(ctx, a.dbPool, *row) {
		if problem := parentResumeProblem(ctx, a.dbPool, row.SessionID); problem != nil {
			writeAPIError(w, problem)
			return
		}
		writeError(w, http.StatusForbidden, "only the parent session can answer this question before escalation")
		return
	}
	// An older native loop that took input as this question's answer closes it here, as in 0.5.4.
	olderLoopClose := actor.Daemon && row.EscalationReason == unroutedEscalation && a.questionRoutingOff(row.SessionID)
	if questionNeedsPerson(*row) && !olderLoopClose && !a.actorDecidesForSession(ctx, actor, row.SessionID) {
		writeError(w, http.StatusForbidden, "only the session's owner can answer this approval")
		return
	}
	alert, alertErr := failureAlertMessage(ctx, a.dbPool, id)
	if alertErr != nil {
		writeError(w, 503, "alert unavailable")
		return
	}
	if alert {
		a.replyMessageUnlocked(w, r, id, req, coreAnswerer(actor))
		return
	}
	if orphanedChildQuestion(ctx, a.dbPool, *row) {
		// Historical answers do not resume the child, but still order against escalation.
		err = withLimitResumeActionLock(ctx, a.dbPool, row.SessionID, func(_ *pgxpool.Conn) error {
			a.replyMessageUnlocked(w, r, id, req, coreAnswerer(actor))
			return nil
		})
		if err != nil {
			writeError(w, 503, "question reply is busy; retry shortly")
		}
		return
	}
	err = withCancelledLimitResume(ctx, a.dbPool, row.SessionID, func() error {
		return withParentInputGuard(ctx, a.dbPool, row.SessionID, func() error {
			a.replyMessageUnlocked(w, r, id, req, coreAnswerer(actor))
			return nil
		})
	})
	if errors.Is(err, db.ErrDelegationEnded) {
		writeError(w, 409, "session was discarded or its parent ended")
	} else if err != nil {
		writeError(w, 503, "message reply is busy; retry shortly")
	}
}

// coreAnswerer is the account of a person signed in as themselves, "" for any other credential.
func coreAnswerer(actor boardActor) string {
	if actor.Core == nil || actor.Core.Kind != "human" || actor.Core.OnBehalfOf != "" {
		return ""
	}
	return actor.Core.Sub
}

func (a *API) replyMessageUnlocked(w http.ResponseWriter, r *http.Request, id string, req postMessageReplyRequest, answerer string, source ...string) bool {
	ctx := r.Context()
	if a.handleBoundHTTP(w, r, id, req) {
		return true
	}
	pendingOnly := len(source) > 0 && source[0] == promptSourceSession
	by := ""
	if pendingOnly {
		message, err := db.GetMessage(ctx, a.dbPool, id)
		if err != nil || message == nil {
			writeError(w, 503, "question unavailable")
			return false
		}
		child, err := db.GetSession(ctx, a.dbPool, message.SessionID)
		if err != nil || child == nil {
			writeError(w, 503, "child unavailable")
			return false
		}
		by = "session:" + derefOrEmpty(child.ParentSessionID)
	} else if answerer != "" {
		by = "person:" + answerer
	}
	changed, err := db.AnswerMessageAs(ctx, a.dbPool, id, req.Answer, pendingOnly, by, replyRevision(req.RevisionSeq)...)
	if err != nil {
		log.Printf("api reply AnswerMessage %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "update failed")
		return false
	}
	if !changed {
		// Already answered/closed, or kind=update (created answered).
		message := "message already answered or not open"
		if current, loadErr := db.GetMessage(ctx, a.dbPool, id); loadErr == nil && current != nil && req.RevisionSeq != nil && current.QuestionRevision != *req.RevisionSeq {
			message = "Main revised this question; reload before answering"
		}
		writeError(w, http.StatusConflict, message)
		return false
	}

	// Order: commit (done) → signal waiter → note routing seam → broadcast.
	a.waiters.signal(id)

	// Reload to get the committed row for broadcasting and note routing.
	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil || row == nil {
		log.Printf("api reply reload %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "reload failed")
		return false
	}

	alert, alertErr := failureAlertMessage(ctx, a.dbPool, id)
	if alertErr != nil {
		writeError(w, 503, "alert unavailable")
		return false
	}
	if alert {
		a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)}) //nolint:contextcheck // browser notification outlives its request
		writeJSON(w, http.StatusOK, map[string]string{"id": id})
		return true
	}

	// Phase 1.5: send inject_note_reply to the owning daemon.
	if row.Kind == "note" {
		a.routeNoteReply(*row, req.Answer)
	}
	// An ask in an agent session's transcript: its answer is recorded there too (messageevents.go).
	recordAskAnswer(ctx, a.hub, a.dbPool, *row, req.Answer, "", source...)
	// A question the agent did not wait on: the answer goes to it as a message (questions.go).
	if !orphanedChildQuestion(ctx, a.dbPool, *row) {
		a.hub.QuestionAnswered(*row, req.Answer, answerer)
	}

	a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)}) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
	return true
}

// noteBodyMax is the maximum number of characters from the note body included in
// the formatted reply prefix. Keeps the injected line short and avoids embedded
// newlines from untrimmed long bodies overflowing.
const noteBodyMax = 80

// stripCRLF removes all carriage-return and line-feed bytes from s. Used to
// ensure no embedded newline in the formatted note reply submits the text early
// when injected into the PTY.
func stripCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// formatNoteReply produces the text to inject into the PTY:
//
//	[Re: "<note body (truncated to 80 chars, CRLF stripped)>"] <answer (CRLF stripped)>
func formatNoteReply(note, answer string) string {
	note = stripCRLF(note)
	if runes := []rune(note); len(runes) > noteBodyMax {
		note = string(runes[:noteBodyMax])
	}
	answer = stripCRLF(answer)
	return `[Re: "` + note + `"] ` + answer
}

// routeNoteReply delivers a note reply to the session's daemon as an
// inject_note_reply message, which the agent host handles as user input.
// Returns silently when no daemon is connected for the
// session (e.g. daemon was restarted; the DB row stays answered).
func (a *API) routeNoteReply(row db.MessageRow, answer string) {
	text := formatNoteReply(row.Body, answer)
	dc := a.hub.FindDaemonForSession(row.SessionID)
	if dc == nil {
		log.Printf("routeNoteReply: no daemon found for session %s", row.SessionID)
		return
	}
	data, err := json.Marshal(protocol.InjectNoteReply{
		Type:      "inject_note_reply",
		SessionID: row.SessionID,
		Text:      text,
	})
	if err != nil {
		log.Printf("routeNoteReply: marshal: %v", err)
		return
	}
	select {
	case dc.send <- data:
	default:
		log.Printf("routeNoteReply: daemon %s send channel full", dc.ID)
	}
}

// HandlePostMessageExpire closes an open message with answer=” — used by the
// CLI on timeout and a host on interruption. A person-only question is
// refused (403) and kept for the person (holdPersonQuestion). Accepts daemon token or board token with the "message"
// capability. Conditional close: if the message is already closed, returns 200
// anyway (idempotent). On a successful close, signals any ask waiter and
// broadcasts message_answered. With waiter_ended the host says its waiter left without reading the
// result: an answer committed first then reaches the session through its outbox (relayLateAnswer).
// Desktop host credentials may report waiter_ended only for sessions they currently host.
func (a *API) HandlePostMessageExpire(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WaiterEnded bool `json:"waiter_ended"`
		Withdraw    bool `json:"withdraw"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req)
	}
	if req.Withdraw && req.WaiterEnded {
		_ = boundFailure("withdraw_waiter_conflict")
		writeError(w, http.StatusForbidden, "Withdrawal requires Main’s own credential.")
		return
	}
	boardSessionID, ok := a.authMessageExpiry(w, r, r.PathValue("id"), req.WaiterEnded)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()

	// Board-token path: enforce session ownership — the token's session must
	// own the addressed message.
	if boardSessionID != "" {
		row, err := db.GetMessage(ctx, a.dbPool, id)
		if err != nil {
			log.Printf("api expire ownership check GetMessage: %v", err)
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		if row == nil {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		if row.SessionID != boardSessionID {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
	}

	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil {
		writeError(w, 503, "question unavailable")
		return
	}
	if row == nil {
		writeJSON(w, http.StatusOK, map[string]string{"id": id})
		return
	}
	var boundWithdrawal bool
	if req.Withdraw {
		if err = a.dbPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bound_asks WHERE message_id=$1 AND status='open')`, id).Scan(&boundWithdrawal); err != nil {
			_ = boundFailure("storage_unavailable")
			writeError(w, 503, "Question unavailable.")
			return
		}
	}
	if boundWithdrawal {
		if boardSessionID == "" || boardSessionID != row.SessionID {
			_ = boundFailure("main_required")
			writeError(w, 403, "Main credential required.")
			return
		}
		session, e := db.GetSession(ctx, a.dbPool, row.SessionID)
		if e != nil || session == nil || session.SpawningAccountID == nil || session.Kind != "agent" || session.ParentSessionID != nil || sessionInputsEnded(session) || session.ArchivedAt != nil || session.DiscardRequestedAt != nil {
			_ = boundFailure("main_required")
			writeError(w, 403, "Main required.")
			return
		}
		root, e := blackboardMainSession(ctx, a.dbPool, *session.SpawningAccountID, session.ID)
		if e != nil || root != session.ID {
			_ = boundFailure("main_required")
			writeError(w, 403, "Main required.")
			return
		}
		tx, e := a.dbPool.Begin(ctx)
		if e != nil {
			_ = boundFailure("storage_unavailable")
			writeError(w, 503, "Question unavailable.")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var bound bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bound_asks WHERE message_id=$1 AND workspace_id=$2 AND status='open')`, id, session.ID).Scan(&bound); e != nil {
			_ = boundFailure("storage_unavailable")
			writeError(w, 503, "Question unavailable.")
			return
		}
		if bound {
			e = a.cancelStaleBoundAsk(ctx, tx, id, "withdrawn_by_main")
			var canceled *boundAskError
			if !errors.As(e, &canceled) || canceled.cause != "withdrawn_by_main" {
				_ = boundFailure("storage_unavailable")
				writeError(w, 503, "Question unavailable.")
				return
			}
			writeJSON(w, 200, map[string]string{"id": id, "status": "canceled"})
			return
		}
	}
	err = withLimitResumeActionLock(ctx, a.dbPool, row.SessionID, func(_ *pgxpool.Conn) error {
		if req.WaiterEnded {
			relayed, err := a.relayLateAnswer(ctx, id)
			if err != nil {
				return err
			}
			if relayed {
				writeJSON(w, http.StatusOK, map[string]string{"id": id})
				return nil
			}
		}
		a.expireMessageUnlocked(w, r, id, req.Withdraw)
		return nil
	})
	if err != nil {
		writeError(w, 503, "question expiry is busy; retry shortly")
	}
}

func (a *API) expireMessageUnlocked(w http.ResponseWriter, r *http.Request, id string, withdrawn bool) {
	ctx := r.Context()
	// An interrupted waiter is held for the person. Only the binding deadline
	// makes /expire cancel a bound question; CLI/host wait lifetimes are not consent.
	var expiredBound bool
	if err := a.dbPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bound_asks WHERE message_id::text=$1 AND expires_at<=now())`, id).Scan(&expiredBound); err != nil {
		writeError(w, 503, "Question unavailable.")
		return
	}
	if expiredBound {
		if _, err := db.AnswerMessage(ctx, a.dbPool, id, ""); err != nil {
			writeError(w, 503, "Question unavailable.")
			return
		}
		failurelog.Default.Event("bound_ask", slog.LevelInfo, "component", "runner", "cause", "binding_expired")
		a.waiters.signal(id)
		writeJSON(w, 200, map[string]string{"id": id, "status": "canceled"})
		return
	}
	// Read under the session's action lock: escalation may have just won.
	held, err := a.holdPersonQuestion(ctx, id)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "question unavailable")
		return
	}
	if held {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "only the person can resolve this question; it stays open for them", "waiter_ended": true})
		return
	}
	changed, err := db.AnswerMessage(ctx, a.dbPool, id, "")
	if err != nil {
		log.Printf("api expire AnswerMessage %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if changed {
		a.waiters.signal(id)
		row, err := db.GetMessage(ctx, a.dbPool, id)
		if err != nil || row == nil {
			log.Printf("api expire reload %s: %v", id, err)
		} else {
			log.Printf("question %s closed without an answer by its session side (person_required=%t escalated=%t)", id, row.PersonRequired, row.ChildEscalated)
			kind, body := "update", "Question closed without an answer."
			if withdrawn {
				kind, body = "note", "Question withdrawn"
			}
			recordServerAgentEvent(ctx, a.hub, a.dbPool, row.SessionID, messageEventID(id, "closed"), kind, messagingPayload{Body: body, MessageID: id, QuestionWithdrawn: withdrawn})
			a.hub.QuestionAnswered(*row, "", "")
			a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)}) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
			a.hub.ChildActivity(row.SessionID)
		}
	}
	// Always 200 — idempotent.
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// HandleGetMessages returns the roll-up feed: all open messages plus the most-recent
// up to ?limit (default 100), newest-first.
//
// Browser-gated (authBrowser): this returns real session message bodies, so it
// carries the same core-issued-token requirement as every other browser-facing
// endpoint. It used to be documented as "open (browser trust level)" — that
// trust level was removed by the platform-identity project; there is no longer
// an unauthenticated browser tier.
func (a *API) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, []protocol.MessageInfo{})
		return
	}
	if _, err := a.dbPool.Exec(r.Context(), `UPDATE messages SET status='answered',answer='',answered_at=now() WHERE id IN (SELECT message_id FROM bound_asks WHERE status='open' AND expires_at<=now() AND owner_id=$1)`, principal.Sub); err != nil {
		writeError(w, 503, "Questions unavailable.")
		return
	}

	limit := defaultMessageLimit
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	rows, err := db.ListMessagesFor(r.Context(), a.dbPool, limit, principal.Sub) // private sessions' messages: owner only
	if err != nil {
		log.Printf("api list messages: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	viewer := "" // only a person signed in as themselves answers a question
	if principal.Kind == "human" && principal.OnBehalfOf == "" {
		viewer = principal.Sub
	}
	writeJSON(w, http.StatusOK, messageInfosFor(r.Context(), a.dbPool, rows, viewer))
}

// actorDecidesForSession is who resolves a person-only question: the session's
// owner, or any signed-in person when a runner key started the session.
func (a *API) actorDecidesForSession(ctx context.Context, actor boardActor, sessionID string) bool {
	if actor.Core == nil || actor.Core.Kind != "human" || actor.Core.OnBehalfOf != "" {
		return false
	}
	row, err := db.GetSession(ctx, a.dbPool, sessionID)
	return err == nil && row != nil && (ownsSession(actor.Core.Sub, row) || derefOrEmpty(row.SpawningAccountID) == "")
}

// Child questions call their owner only after escalation. Top-level no_wait and
// person-only questions go to the owner's inbox; a plain top-level ask does not.
func questionCallsOwner(sess *db.SessionRow, msg db.MessageRow) bool {
	return sess != nil && derefOrEmpty(sess.SpawningAccountID) != "" && ((!hasCoordinator(sess) && (msg.NoWait || msg.PersonRequired)) || (hasCoordinator(sess) && msg.ChildEscalated))
}
