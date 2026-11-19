package server

// A parent is told when its child's turn ends.
//
// A parent session that starts a child would otherwise have to keep `blerg-runner session watch`
// running to learn that the child finished, asked something, is waiting on the person's approval
// or failed. Instead the runner tells it, as a note from Blerg that arrives at the parent's next
// step with fixed event metadata. Main explicitly reads untrusted worker data
// through `blerg-runner session result <id>`; worker text never enters a wake.
//
// The note is relayed in Blerg's own voice (source childTurnSource, which reservedMessageSource
// refuses from every caller, so no agent can write one) through sendRelay. A completed turn's
// notification is saved with its transcript event and retried until the parent's input outbox
// accepts it. Its stable client ID names the child and the first outstanding root work event.
// Background continuations update that report while its parent input is unconsumed. The latest
// summary survives reconnect without another engine wake. Repeated ends share that ID,
// including a turn that both errors and marks the session errored.
// Questions are delivered independently by child_questions.go, once per question. A parent
// that has ended is never woken.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// childTurnSource is the source of a child-turn note in the parent's transcript.
const childTurnSource = "child_turn"

// What a child's turn ended in.
const (
	childDiscardUnanswered = "discard_unanswered"
	childTurnFinished      = "finished"
	childTurnAsked         = "asked"
	childTurnApproval      = "approval"
	childTurnFailed        = "failed"
	childRestartPaused     = "restart_paused"
	// childSessionFailed: the session itself failed (its runtime stopped, it exited with an
	// error) rather than one of its turns. Its note is deduplicated apart from the turn's, so a
	// child that dies after its last turn was reported is still reported.
	childSessionFailed = "session_failed"
)

// childSyncErrorCode is the code of the error a daemon records when it could not bring a sandboxed
// child's commits back to wip/<id> at the end of a turn (agent.ErrorCodeChildSync).
const childSyncErrorCode = "child_sync"

// childTurnNoteTimeout bounds one note: lookups plus the relay.
const childTurnNoteTimeout = 30 * time.Second

// ChildTurnEnded tells the API that sessionID's turn ended in kind; detail is what the event
// itself carries (a question's text, a failure), "" when the API reads it from the transcript. A
// no-op without a registered hook.
func (h *Hub) ChildTurnEnded(sessionID, kind, detail string) {
	h.mu.RLock()
	fn := h.childTurn
	h.mu.RUnlock()
	if fn != nil {
		fn(sessionID, kind, detail)
	}
}

// SetChildTurnHook registers the API's child-turn notifier (NewAPI).
func (h *Hub) SetChildTurnHook(fn func(sessionID, kind, detail string)) {
	h.mu.Lock()
	h.childTurn = fn
	h.mu.Unlock()
}

// childTurnSeen deduplicates notes in process too: a parent on a daemon without input receipts
// gets a relay with no client id, which the database cannot deduplicate.
type childTurnSeen struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func (s *childTurnSeen) first(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil || len(s.keys) > 4096 {
		s.keys = map[string]struct{}{}
	}
	if _, ok := s.keys[key]; ok {
		return false
	}
	s.keys[key] = struct{}{}
	return true
}

func (s *childTurnSeen) forget(key string) {
	s.mu.Lock()
	delete(s.keys, key)
	s.mu.Unlock()
}

// onChildTurnEnded is the hub hook: it runs off the caller's goroutine (a daemon's read pump, a
// request handler), since relaying to the parent can wait on the parent's own locks.
func (a *API) onChildTurnEnded(sessionID, kind, detail string) {
	if a == nil || a.dbPool == nil || !validSessionID(sessionID) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), childTurnNoteTimeout)
		defer cancel()
		if kind == "recommended_channel_closed" {
			if _, err := a.dbPool.Exec(ctx, `INSERT INTO channel_close_notices(child_id) VALUES($1) ON CONFLICT DO NOTHING`, sessionID); err != nil {
				failurelog.Default.Event("runner:channel-close:record", slog.LevelError, "component", "runner", "operation", "channel_close", "stage", "record", "session_id", sessionID, "cause", failurelog.Cause(err))
				return
			}
			if err := a.reconcileChannelCloses(ctx); err != nil {
				failurelog.Default.Event("runner:channel-close:reconcile", slog.LevelError, "component", "runner", "operation", "channel_close", "stage", "reconcile", "session_id", sessionID, "cause", failurelog.Cause(err))
			}
			return
		}
		if kind == "durable_turn" {
			// Scoped to sessionID: every "durable_turn" call site names the one child whose
			// notice might now be ready, so there is no need to sweep every other child's
			// pending row too (BLG-447 review).
			if err := a.reconcileChildTurn(ctx, sessionID); err != nil {
				log.Printf("child turn delivery: %v", err)
			}
			return
		}
		if err := recordSessionFailure(ctx, a.hub, a.dbPool, sessionID); err != nil {
			log.Printf("session failure alert: %v", err)
		}
		sent := a.noteChildTurn(ctx, sessionID, kind, detail)
		if sent && (kind == childSessionFailed || kind == childRestartPaused) {
			_, err := a.dbPool.Exec(ctx, `UPDATE session_failure_alerts SET parent_notified=true WHERE session_id=$1`, sessionID)
			if err != nil {
				log.Printf("child failure notice receipt: %v", err)
			}
		}
		if err := a.escalateSessionFailures(ctx, time.Now()); err != nil {
			log.Printf("child failure escalation: %v", err)
		}
	}()
}

// noteChildTurn relays one child-turn note to childID's parent, if it has one that is still
// running. This is one attempt; durable turn intents retry failures.
func (a *API) noteChildTurn(ctx context.Context, childID, kind, detail string, notice ...childTurnDelivery) bool {
	// Reconciliation calls this directly too. A stuck coordinator must not
	// hold up the independent owner alert or its escalation sweep.
	ctx, cancel := context.WithTimeout(ctx, childTurnNoteTimeout)
	defer cancel()
	child, err := db.GetSession(ctx, a.dbPool, childID)
	if err != nil || child == nil {
		return false
	}
	if kind == "recommended_channel_closed" {
		return a.noteRecommendedChannelClosed(ctx, child)
	}
	if child.DelegationVia == "person" {
		return true
	} // Personal chats never relay their content to Main.
	parentID := derefOrEmpty(child.ParentSessionID)
	if parentID == "" || child.ArchivedAt != nil {
		return false
	}
	parent, err := db.GetSession(ctx, a.dbPool, parentID)
	if err != nil || parent == nil || parent.Kind != "agent" {
		return false
	}
	if parentEnded(parent) || parent.Status == "error" {
		return false
	}
	if a.hub.FindDaemonForSession(parentID) == nil && derefOrEmpty(parent.Runtime) == runnerRuntimeCluster {
		return false
	}
	if kind == childTurnFinished || (kind == childTurnFailed && detail == "") {
		failure := ""
		if len(notice) > 0 {
			var err error
			_, failure, _, err = a.childTurnAnswer(ctx, childID, notice[0].event)
			if err != nil {
				return false
			}
		} else {
			_, failure = a.lastTurnAnswer(ctx, childID)
		}
		if failure != "" {
			kind = childTurnFailed
		}
	}
	coalesce := len(notice) > 0
	turn := int64(0)
	if len(notice) > 0 {
		turn = notice[0].turn
	} else {
		var err error
		if kind == childTurnFinished || kind == childTurnFailed {
			err = a.dbPool.QueryRow(ctx, `SELECT COALESCE(max(seq),0) FROM agent_events WHERE session_id=$1 AND (kind='user_message' OR (kind IN ('assistant_text','tool_call') AND COALESCE(payload->>'parent_call_id','')=''))`, childID).Scan(&turn)
		} else {
			turn, err = db.LatestUserMessageSeq(ctx, a.dbPool, childID)
		}
		if err != nil {
			log.Printf("child turn note %s: %v", childID, err)
			return false
		}
	}
	if !coalesce && (kind == childTurnFinished || kind == childTurnFailed) {
		var outstanding int64
		err := a.dbPool.QueryRow(ctx, `SELECT COALESCE((SELECT c.turn_seq FROM child_turn_notices c WHERE c.child_id=$1 AND (NOT c.delivered OR EXISTS(SELECT 1 FROM session_inputs i WHERE i.session_id=$2 AND i.client_id='child-turn-'||c.child_id::text||'-finished-'||c.turn_seq::text AND i.settled_at IS NULL)) ORDER BY c.event_seq DESC LIMIT 1),-1)`, childID, parentID).Scan(&outstanding)
		if err != nil {
			return false
		}
		if outstanding >= 0 {
			turn = outstanding
			coalesce = true
		}
	}
	group := kind
	if kind == childTurnFailed {
		group = childTurnFinished // one end of a turn, however it is reported
	}
	clientID := fmt.Sprintf("child-turn-%s-%s-%d", childID, group, turn)
	if !coalesce && !a.childTurns.first(clientID) {
		return false
	}
	noticeEvent := workerEvent{childID, kind, workerEventID(clientID)}
	text := workerNotice(noticeEvent.child, noticeEvent.kind, noticeEvent.id)
	if coalesce {
		refreshed, err := db.RefreshChildTurnInput(ctx, a.dbPool, parentID, clientID, text)
		if err != nil {
			return false
		}
		if refreshed {
			return true
		}
		if !a.childTurns.first(clientID) {
			return true
		}
	}
	principal := runnerPrincipal{Kind: "human", Sub: derefOrEmpty(parent.SpawningAccountID)}
	var apiErr *APIError
	if dc := a.hub.FindDaemonForSession(parentID); dc == nil || dc.CanInputReceipts() {
		apiErr = a.sendWorkerNotice(ctx, principal, parentID, noticeEvent, clientID)
	} else {
		apiErr = a.sendWorkerNotice(ctx, principal, parentID, noticeEvent)
	}
	if apiErr != nil {
		a.childTurns.forget(clientID)
		log.Printf("child turn note %s → %s: %s", childID, parentID, apiErr.Message)
	}
	return apiErr == nil
}

// parentEnded reports whether a parent is over for good: a note must not start it again.
func parentEnded(parent *db.SessionRow) bool {
	return db.TerminallyEnded(parent) || parent.DelegationEndedAt != nil || parent.Status == "ended" || parent.Status == "stopped"
}

func childTitle(child *db.SessionRow) string {
	if t := strings.TrimSpace(derefOrEmpty(child.Title)); t != "" {
		return t
	}
	return sessionLabel("", child.Repo, child.ID)
}

// quotedLine is the first non-empty line of s, its whitespace collapsed and double quotes made
// single, clipped to n runes and quoted; "" for blank text.
func quotedLine(s string, n int) string {
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ' ' || r == ' ' }) {
		var b strings.Builder
		space := false
		for _, r := range l {
			switch {
			case unicode.IsControl(r) || unicode.IsSpace(r):
				space = b.Len() > 0
				continue
			case r == '"':
				r = '\''
			}
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		}
		if b.Len() > 0 {
			return `"` + clipRunes(b.String(), n) + `"`
		}
	}
	return ""
}

// lastTurnSyncProblem is the message of the child_sync error in the child's last turn (between the
// turn_done before it and its own), "" when its commits were brought back.
func (a *API) lastTurnSyncProblem(ctx context.Context, childID string) string {
	rows, err := db.ListAgentEventsTailKinds(ctx, a.dbPool, childID, []string{turnDoneKind, "error"}, resultAssistantScanLimit)
	if err != nil {
		return ""
	}
	return turnSyncProblem(rows)
}

func turnSyncProblem(rows []db.AgentEventRow) string {
	for i, ev := range rows { // newest first
		if ev.Kind == turnDoneKind {
			if i > 0 {
				return ""
			}
			continue
		}
		var p struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if json.Unmarshal([]byte(ev.Payload), &p) == nil && p.Code == childSyncErrorCode && p.Message != "" {
			return p.Message
		}
	}
	return ""
}
