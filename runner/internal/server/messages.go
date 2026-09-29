package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
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
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	Body      string `json:"body"`
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
	if !validMessageKinds[req.Kind] || req.Body == "" || req.SessionID == "" {
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

	id, err := db.CreateMessage(ctx, a.dbPool, req.SessionID, req.Kind, req.Body)
	if err != nil {
		log.Printf("api post message CreateMessage: %v", err)
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil || row == nil {
		log.Printf("api post message reload: %v", err)
		writeError(w, http.StatusInternalServerError, "reload failed")
		return
	}
	info := messageRowToInfo(*row)
	a.hub.BroadcastJSON(protocol.MessageCreated{Type: "message_created", Message: info})
	a.pushForMessage(*sess, *row) //nolint:contextcheck // push delivery is best-effort background work with no tie to this request

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// pushForMessage sends a web push for a new message. ask/note always push (they need
// the user); update pushes only when the user is away (matching the session-state
// push policy), so progress chatter doesn't buzz while they're watching.
func (a *API) pushForMessage(sess db.SessionRow, msg db.MessageRow) {
	if msg.Kind == "update" && a.hub.ActiveWithin(activityTTL) {
		return
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
	// Land on the roll-up chat (mobile "/"), where the user can read and reply.
	SendPush(a.dbPool, heading, msg.Body, "/")
}

type answerResponse struct {
	Answered bool    `json:"answered"`
	Answer   *string `json:"answer"` // nil/'' distinguishes a closed message from a real empty reply
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

	if done, resp := a.answeredNow(ctx, w, id); done {
		if resp != nil {
			writeJSON(w, http.StatusOK, resp)
		}
		return
	}

	hold := maxAnswerHold
	if q := r.URL.Query().Get("wait"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			if d := time.Duration(n) * time.Second; d < hold {
				hold = d
			}
		}
	}
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
	if done, resp := a.answeredNow(ctx, w, id); done {
		if resp != nil {
			writeJSON(w, http.StatusOK, resp)
		}
		return
	}
	writeJSON(w, http.StatusOK, answerResponse{Answered: false})
}

// answeredNow reads the message; returns done=true with a response when the message
// is answered or missing (writing a 404 itself in the missing case → resp nil).
func (a *API) answeredNow(ctx context.Context, w http.ResponseWriter, id string) (bool, *answerResponse) {
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
	if row.Status == "answered" {
		return true, &answerResponse{Answered: true, Answer: row.Answer}
	}
	return false, nil
}

type postMessageReplyRequest struct {
	Answer string `json:"answer"`
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

	var req postMessageReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}

	changed, err := db.AnswerMessage(ctx, a.dbPool, id, req.Answer)
	if err != nil {
		log.Printf("api reply AnswerMessage %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if !changed {
		// Already answered/closed, or kind=update (created answered).
		writeError(w, http.StatusConflict, "message already answered or not open")
		return
	}

	// Order: commit (done) → signal waiter → note routing seam → broadcast.
	a.waiters.signal(id)

	// Reload to get the committed row for broadcasting and note routing.
	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil || row == nil {
		log.Printf("api reply reload %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "reload failed")
		return
	}

	// Phase 1.5: send inject_note_reply to the owning daemon.
	if row.Kind == "note" {
		a.routeNoteReply(*row, req.Answer)
	}

	a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
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
// inject_note_reply message. The daemon gates injection on classifyScreen == "idle"
// and queues it otherwise. Returns silently when no daemon is connected for the
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

// HandlePostMessageExpire closes an open message with answer=” — used only by
// the CLI on timeout. Accepts daemon token or board token with the "message"
// capability. Conditional close: if the message is already closed, returns 200
// anyway (idempotent). On a successful close, signals any ask waiter and
// broadcasts message_answered.
func (a *API) HandlePostMessageExpire(w http.ResponseWriter, r *http.Request) {
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
			a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)})
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
	if _, ok := a.authBrowser(w, r); !ok {
		return
	}
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, []protocol.MessageInfo{})
		return
	}
	limit := defaultMessageLimit
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	rows, err := db.ListMessages(r.Context(), a.dbPool, limit)
	if err != nil {
		log.Printf("api list messages: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	out := make([]protocol.MessageInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, messageRowToInfo(row))
	}
	writeJSON(w, http.StatusOK, out)
}
