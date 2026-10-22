package server

// A parent is told when its child's turn ends.
//
// A parent session that starts a child would otherwise have to keep `blerg-runner session watch`
// running to learn that the child finished, asked something, is waiting on the person's approval
// or failed. Instead the runner tells it, as a note from Blerg that arrives at the parent's next
// step: `Child "<title>" finished its turn: "<first line>". blerg-runner session result <id>`.
//
// The note is relayed in Blerg's own voice (source childTurnSource, which reservedMessageSource
// refuses from every caller, so no agent can write one) through sendRelay. It is deduplicated per
// child turn: its client id names the child, the kind of note and the child's newest user message
// (the input its current turn answers), so a re-sent event, a turn that both ends in error and
// marks the session errored, or a second question in the same turn tells the parent once. A parent
// that has ended is never woken.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// childTurnSource is the source of a child-turn note in the parent's transcript.
const childTurnSource = "child_turn"

// What a child's turn ended in.
const (
	childTurnFinished = "finished"
	childTurnAsked    = "asked"
	childTurnApproval = "approval"
	childTurnFailed   = "failed"
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
	if a == nil || a.dbPool == nil || sessionID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), childTurnNoteTimeout)
		defer cancel()
		a.noteChildTurn(ctx, sessionID, kind, detail)
	}()
}

// noteChildTurn relays one child-turn note to childID's parent, if it has one that is still
// running. Best effort: a failure is logged.
func (a *API) noteChildTurn(ctx context.Context, childID, kind, detail string) {
	child, err := db.GetSession(ctx, a.dbPool, childID)
	if err != nil || child == nil {
		return
	}
	parentID := derefOrEmpty(child.ParentSessionID)
	if parentID == "" || child.ArchivedAt != nil {
		return
	}
	parent, err := db.GetSession(ctx, a.dbPool, parentID)
	if err != nil || parent == nil || parent.Kind != "agent" {
		return
	}
	if parentEnded(parent) {
		return
	}
	if kind == childTurnFinished || (kind == childTurnFailed && detail == "") {
		answer, failure := a.lastTurnAnswer(ctx, childID)
		switch {
		case failure != "":
			kind, detail = childTurnFailed, failure
		case kind == childTurnFinished:
			detail = answer
		}
	}
	if (kind == childTurnFailed || kind == childSessionFailed) && detail == "" {
		detail = derefOrEmpty(child.ErrorReason)
	}
	turn, err := db.LatestUserMessageSeq(ctx, a.dbPool, childID)
	if err != nil {
		log.Printf("child turn note %s: %v", childID, err)
		return
	}
	group := kind
	if kind == childTurnFailed {
		group = childTurnFinished // one end of a turn, however it is reported
	}
	clientID := fmt.Sprintf("child-turn-%s-%s-%d", childID, group, turn)
	if !a.childTurns.first(clientID) {
		return
	}
	text := childTurnNote(childTitle(child), childID, kind, detail)
	if problem := a.lastTurnSyncProblem(ctx, childID); problem != "" {
		text += " Its commits from this turn were NOT brought back to wip/" + childID + ": " + quotedLine(problem, 160) + "."
	}
	principal := runnerPrincipal{Kind: "human", Sub: derefOrEmpty(parent.SpawningAccountID)}
	var apiErr *APIError
	if dc := a.hub.FindDaemonForSession(parentID); dc == nil || dc.CanInputReceipts() {
		apiErr = a.sendRelay(ctx, principal, parentID, text, childTurnSource, clientID)
	} else {
		apiErr = a.sendRelay(ctx, principal, parentID, text, childTurnSource)
	}
	if apiErr != nil {
		a.childTurns.forget(clientID)
		log.Printf("child turn note %s → %s: %s", childID, parentID, apiErr.Message)
	}
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

// childTurnNote is the note's text. The title and the child's own words are quoted on one line
// and clipped, so a child cannot make its text read as Blerg's.
func childTurnNote(title, childID, kind, detail string) string {
	head := "Child " + quotedName(title)
	line := quotedLine(detail, 160)
	with := func(s string) string {
		if line == "" {
			return s
		}
		return s + ": " + line
	}
	switch kind {
	case childTurnAsked:
		return with(head+" asked a question") + fmt.Sprintf(". Answer it with blerg-runner session send %s \"<answer>\"; escalate anything destructive with blerg-runner ask.", childID)
	case childTurnApproval:
		return with(head+" is waiting on an approval from the person") + fmt.Sprintf(". Only the person can decide it, in the child's chat. blerg-runner session result %s", childID)
	case childTurnFailed:
		return with(head+" failed its turn") + fmt.Sprintf(". blerg-runner session result %s", childID)
	case childSessionFailed:
		return with(head+" failed") + fmt.Sprintf(" and stopped; it takes no more messages. blerg-runner session result %s", childID)
	default:
		return with(head+" finished its turn") + fmt.Sprintf(". blerg-runner session result %s", childID)
	}
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
