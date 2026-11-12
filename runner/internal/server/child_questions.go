package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Child questions remain in the child's history, but only the parent may answer
// until escalation. Failure alerts have their own routing and are excluded.
func childQuestionPending(ctx context.Context, pool *pgxpool.Pool, m db.MessageRow) bool {
	if m.Kind != "ask" || m.ChildEscalated {
		return false
	}
	s, err := db.GetSession(ctx, pool, m.SessionID)
	if err != nil {
		return true
	}
	if !isChildSession(s) {
		return false
	}
	alert, err := failureAlertMessage(ctx, pool, m.ID)
	return err != nil || !alert
}

// A parent's normal shutdown stops its ordinary children. Their unresolved
// questions still need a human review, but an answer must not restart the child.
func orphanedChildQuestion(ctx context.Context, pool *pgxpool.Pool, m db.MessageRow) bool {
	if !m.ChildEscalated {
		return false
	}
	child, err := db.GetSession(ctx, pool, m.SessionID)
	return err == nil && child != nil && derefOrEmpty(child.ParentSessionID) != "" && derefOrEmpty(child.EndReason) == db.EndReasonParentEnded
}

func (a *API) childQuestion(w http.ResponseWriter, r *http.Request) (*db.SessionRow, *db.MessageRow, bool) {
	parent, _, child, ok := a.delegateChild(w, r)
	if !ok {
		return nil, nil, false
	}
	id := r.PathValue("question")
	if !validSessionID(id) {
		writeError(w, 404, "question not found")
		return nil, nil, false
	}
	m, err := db.GetMessage(r.Context(), a.dbPool, id)
	if err != nil {
		writeError(w, 503, "question unavailable")
		return nil, nil, false
	}
	if m == nil || m.SessionID != child || m.Kind != "ask" {
		writeError(w, 404, "question not found")
		return nil, nil, false
	}
	alert, err := failureAlertMessage(r.Context(), a.dbPool, id)
	if err != nil || alert {
		writeError(w, 409, "this is a failure alert, not a child question")
		return nil, nil, false
	}
	return parent, m, true
}

func (a *API) handleChildQuestionAnswer(w http.ResponseWriter, r *http.Request) {
	_, m, ok := a.childQuestion(w, r)
	if !ok {
		return
	}
	var req postMessageReplyRequest
	if !decodeChildBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Answer) == "" {
		writeError(w, 400, "answer must not be empty")
		return
	}
	// Command approvals still require the person; a parent can escalate them.
	if m.PersonRequired || (m.OwnerOnly && !m.NoWait) {
		writeError(w, 403, "escalate this approval to the person")
		return
	}
	ctx := r.Context()
	err := withLimitResumeActionLock(ctx, a.dbPool, m.SessionID, func(conn *pgxpool.Conn) error {
		return withParentInputGuard(ctx, a.dbPool, m.SessionID, func() error {
			if a.replyMessageUnlocked(w, r, m.ID, req, "", promptSourceSession) {
				return cancelLimitResumeUnlocked(ctx, a.dbPool, conn, m.SessionID)
			}
			return nil
		})
	})
	if errors.Is(err, db.ErrDelegationEnded) {
		writeError(w, http.StatusConflict, "session was discarded or its parent ended")
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, "question reply is busy; retry shortly")
	}
}

// holdPersonQuestion refuses a session-side close of a person-only question and
// reports whether it did. The session may stop waiting, but only the person
// resolves it: it stays in Needs you, no longer waited on, and their answer
// reaches the session as a message.
func (a *API) holdPersonQuestion(ctx context.Context, id string) (bool, error) {
	m, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil {
		return false, err
	}
	if m == nil || m.Kind != "ask" || m.Status != "open" || !personOnlyQuestion(*m) {
		return false, nil
	}
	session, err := db.GetSession(ctx, a.dbPool, m.SessionID)
	if err != nil {
		return false, err
	}
	if session == nil {
		return false, nil
	}
	alert, err := failureAlertMessage(ctx, a.dbPool, id)
	if err != nil || alert {
		return false, err
	}
	changed, err := db.HoldPersonQuestion(ctx, a.dbPool, id)
	if err != nil || !changed {
		return true, err
	}
	a.waiters.signal(id)
	recordServerAgentEvent(ctx, a.hub, a.dbPool, m.SessionID, messageEventID(id, "closed"), "update", messagingPayload{Body: "Question " + id + " is no longer waited on. It stays with the person, and their answer reaches this session as a message.", MessageID: id})
	if m, err = db.GetMessage(ctx, a.dbPool, id); err != nil || m == nil {
		return true, err
	}
	a.hub.BroadcastJSONPerAccount(func(account string) any { //nolint:contextcheck // bounded independent browser delivery
		info := messageRowToInfo(*m)
		info.Answerable = (!hasCoordinator(session) || m.ChildEscalated) && decidesEscalation(account, session)
		return protocol.MessageCreated{Type: "message_created", Message: info, Quiet: true}
	})
	a.hub.ChildActivity(m.SessionID)
	return true, nil
}

// decidesEscalation is actorDecidesForSession for a browser account: the child's owner, or any
// signed-in person when a runner key started it. The hub still scopes delivery to who can see it.
func decidesEscalation(account string, child *db.SessionRow) bool {
	return account != "" && (ownsSession(account, child) || derefOrEmpty(child.SpawningAccountID) == "")
}

var errChildQuestionClosed = errors.New("question already closed")

func (a *API) handleChildQuestionEscalate(w http.ResponseWriter, r *http.Request) {
	parent, m, ok := a.childQuestion(w, r)
	if !ok {
		return
	}
	if m.Status != "open" {
		writeError(w, 409, "question already closed")
		return
	}
	var req struct {
		Summary        string `json:"summary"`
		Recommendation string `json:"recommendation"`
	}
	if !decodeChildBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Summary) == "" || strings.TrimSpace(req.Recommendation) == "" {
		writeError(w, 400, "a plain-language summary and recommendation are required")
		return
	}
	child, err := db.GetSession(r.Context(), a.dbPool, m.SessionID)
	if err != nil || child == nil {
		logPersonTimeFailure("escalation_child_read", err)
		writeError(w, 503, "child unavailable")
		return
	}
	if err := a.escalateChildQuestion(r.Context(), child, parent, m.ID, "session:"+parent.ID, "parent_requested", req.Summary, req.Recommendation); err != nil {
		if errors.Is(err, errChildQuestionClosed) {
			writeError(w, 409, "question already closed")
		} else {
			logPersonTimeFailure("child_question_escalation", err)
			writeError(w, 503, "escalation unavailable")
		}
		return
	}
	writeJSON(w, 200, map[string]string{"id": m.ID})
}

func (a *API) escalateChildQuestion(ctx context.Context, child, parent *db.SessionRow, id string, audit ...string) error {
	err := withLimitResumeActionLock(ctx, a.dbPool, child.ID, func(_ *pgxpool.Conn) error {
		return a.escalateChildQuestionUnlocked(ctx, child, parent, id, audit...)
	})
	if err != nil {
		return err
	}
	m, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil || m == nil || m.Status != "open" {
		return err
	}
	return a.notifyChildQuestionParent(ctx, child, parent, m)
}

func (a *API) escalateChildQuestionUnlocked(ctx context.Context, child, parent *db.SessionRow, id string, audit ...string) error {
	if len(audit) >= 2 {
		current, err := db.GetMessage(ctx, a.dbPool, id)
		if err != nil {
			return err
		}
		if current == nil || current.Status != "open" {
			return errChildQuestionClosed
		}
	}
	// The lookup can precede archive admission. A stale candidate must never notify the person.
	currentChild, err := db.GetSession(ctx, a.dbPool, child.ID)
	if err != nil {
		return err
	}
	if currentChild == nil {
		return errChildQuestionClosed
	}
	if currentChild.ArchivedAt != nil {
		if err := a.withdrawDiscardQuestions(ctx, child.ID, []string{id}); err != nil {
			return err
		}
		return errChildQuestionClosed
	}
	label := "Main"
	if parent != nil && personWorkNoun(parent) != "Workspace" {
		label = childTitle(parent)
	}
	by, reason := "system", "parent_unavailable"
	if len(audit) >= 2 {
		by, reason = audit[0], audit[1]
	}
	prefix := fmt.Sprintf("Worker %s, via %s: ", quotedName(childTitle(child)), quotedName(label))
	if len(audit) == 4 {
		prefix += audit[2] + "\n\nRecommendation: " + audit[3] + "\n\nWorker's original question:\n"
	}
	// The session action lock orders escalation notifications and answers; the
	// conditional update also locks the same row as AnswerMessage. An answer that
	// wins first prevents escalation; after escalation only the person can answer.
	_, err = a.dbPool.Exec(ctx, `UPDATE messages SET child_escalated=true,child_parent_notified=false,body=$2||body,escalated_by=$3,escalation_reason=$4 WHERE id=$1 AND status='open' AND NOT child_escalated`, id, prefix, by, reason)
	if err != nil {
		return err
	}
	var notified bool
	if err := a.dbPool.QueryRow(ctx, `SELECT child_person_notified FROM messages WHERE id=$1`, id).Scan(&notified); err != nil {
		return err
	}
	if notified {
		return nil
	}
	m, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil || m == nil || m.Status != "open" || !m.ChildEscalated {
		return err
	}
	a.hub.BroadcastJSONPerAccount(func(account string) any { //nolint:contextcheck // bounded independent browser delivery
		info := messageRowToInfo(*m)
		info.Answerable = decidesEscalation(account, child)
		return protocol.MessageCreated{Type: "message_created", Message: info, Quiet: !info.Answerable}
	})
	a.pushForMessage(*child, *m) //nolint:contextcheck // push has its own bounded context
	_, err = a.dbPool.Exec(ctx, `UPDATE messages SET child_person_notified=true WHERE id=$1`, id)
	return err
}

func (a *API) reconcileChildQuestions(ctx context.Context, _ time.Time, onlyIDs ...string) error {
	rows, err := a.dbPool.Query(ctx, `SELECT m.id::text,m.session_id::text,m.child_parent_notified FROM messages m JOIN sessions s ON s.id=m.session_id
 WHERE m.kind='ask' AND ((m.status='open' AND (NOT m.child_escalated OR NOT m.child_person_notified OR NOT m.child_parent_notified OR s.archived_at IS NOT NULL)) OR (m.status<>'open' AND (m.child_escalated OR m.status='withdrawn') AND NOT m.child_parent_notified)) AND (s.parent_session_id IS NOT NULL OR COALESCE(s.delegation_via,'') <> '' OR s.started_by_kind='session') AND COALESCE(s.delegation_via,'') <> 'person'
 AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=m.id) AND (COALESCE(cardinality($1::text[]),0)=0 OR m.id::text=ANY($1::text[])) ORDER BY m.created_at,m.id`, onlyIDs)
	if err != nil {
		return err
	}
	type item struct {
		id, child string
		notified  bool
	}
	var items []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.child, &i.notified); err != nil {
			break
		}
		items = append(items, i)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var sweepErr error
	for _, i := range items {
		itemErr := func() error {
			m, err := db.GetMessage(ctx, a.dbPool, i.id)
			if err != nil {
				return err
			}
			if m == nil {
				return nil
			}
			if m.Status != "open" {
				return a.notifyResolvedChildQuestionParent(ctx, m.ID)
			}
			child, err := db.GetSession(ctx, a.dbPool, i.child)
			if err != nil {
				return err
			}
			if child == nil {
				return nil
			}
			if child.ArchivedAt != nil {
				err := withLimitResumeActionLock(ctx, a.dbPool, child.ID, func(_ *pgxpool.Conn) error { return a.withdrawDiscardQuestions(ctx, child.ID, []string{m.ID}) })
				if err != nil {
					return err
				}
				return nil
			}
			var parent *db.SessionRow
			if parentID := derefOrEmpty(child.ParentSessionID); parentID != "" {
				parent, err = db.GetSession(ctx, a.dbPool, parentID)
				if err != nil {
					return err
				}
			}
			if m.ChildEscalated {
				if err := a.escalateChildQuestion(ctx, child, parent, m.ID, "system", "person_required"); err != nil {
					return err
				}
				return nil
			}
			if parent == nil || parentEnded(parent) || parent.Status == "error" {
				return a.endWorkerQuestion(ctx, child, m.ID, "Main has ended or is unavailable")
			}
			if a.questionRoutingOff(child.ID) {
				return a.routeUnroutedChildQuestion(ctx, child, m.ID)
			}
			return a.notifyChildQuestionParent(ctx, child, parent, m)
		}()
		sweepErr = errors.Join(sweepErr, itemErr)
	}
	return sweepErr
}

// Each question phase has its own durable parent notice. Escalation resets the
// delivery flag so a pending notice cannot hide the later person-only state.
func (a *API) notifyChildQuestionParent(ctx context.Context, child, parent *db.SessionRow, m *db.MessageRow) error {
	return withLimitResumeActionLock(ctx, a.dbPool, child.ID, func(_ *pgxpool.Conn) error {
		fresh, err := db.GetMessage(ctx, a.dbPool, m.ID)
		if err != nil || fresh == nil || (fresh.Kind != "note" && fresh.Status != "open") {
			return err
		}
		return a.notifyChildQuestionParentUnlocked(ctx, child, parent, fresh)
	})
}

func (a *API) notifyChildQuestionParentUnlocked(ctx context.Context, child, parent *db.SessionRow, m *db.MessageRow) error {
	if parent == nil || parentEnded(parent) {
		_, err := a.dbPool.Exec(ctx, `UPDATE messages SET child_parent_notified=true WHERE id=$1`, m.ID)
		return err
	}
	var notified bool
	if err := a.dbPool.QueryRow(ctx, `SELECT child_parent_notified FROM messages WHERE id=$1`, m.ID).Scan(&notified); err != nil {
		return err
	}
	if notified {
		return nil
	}
	notice := workerEvent{child.ID, childTurnAsked, m.ID}
	key := "child-question-" + m.ID
	if m.Kind == "note" {
		key = "child-note-" + m.ID
		notice.kind = "note"
	} else if m.ChildEscalated || m.PersonRequired {
		key = "child-question-person-" + m.ID
		notice.kind = "question_person"
	}
	if !a.childTurns.first(key) {
		return nil
	}
	principal := runnerPrincipal{Kind: "human", Sub: derefOrEmpty(parent.SpawningAccountID)}
	var problem *APIError
	if dc := a.hub.FindDaemonForSession(parent.ID); dc == nil || dc.CanInputReceipts() {
		problem = a.sendWorkerNotice(ctx, principal, parent.ID, notice, key)
	} else {
		problem = a.sendWorkerNotice(ctx, principal, parent.ID, notice)
	}
	if problem != nil {
		a.childTurns.forget(key)
		log.Printf("worker message %s: parent notice pending: %s", m.ID, problem.Message)
		return nil //nolint:nilerr // The next tick retries; waiting for Main has no time limit.
	}
	if _, err := a.dbPool.Exec(ctx, `UPDATE messages SET child_parent_notified=true WHERE id=$1`, m.ID); err != nil {
		return err
	}
	return nil
}

// Strip only the server's generated escalation tag when embedding the question
// in a parent notice, whose own prefix already identifies both sessions.
var childEscalationTag = regexp.MustCompile(`^(?:Child "(?:\\.|[^"\\])*", parent|Worker "(?:\\.|[^"\\])*", via) "(?:\\.|[^"\\])*": `)

func childQuestionText(m db.MessageRow) string {
	if m.ChildEscalated {
		return childEscalationTag.ReplaceAllString(m.Body, "")
	}
	return m.Body
}

func (a *API) notifyResolvedChildQuestionParent(ctx context.Context, id string) error {
	m, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil || m == nil || m.Status == "open" || (!m.ChildEscalated && m.Status != "withdrawn") {
		return err
	}
	return withLimitResumeActionLock(ctx, a.dbPool, m.SessionID, func(_ *pgxpool.Conn) error {
		m, err := db.GetMessage(ctx, a.dbPool, id)
		if err != nil || m == nil || m.Status == "open" {
			return err
		}
		child, err := db.GetSession(ctx, a.dbPool, m.SessionID)
		if err != nil || child == nil || child.DelegationVia == "person" || derefOrEmpty(child.ParentSessionID) == "" {
			return err
		}
		var notified bool
		if err = a.dbPool.QueryRow(ctx, `SELECT child_parent_notified FROM messages WHERE id=$1`, id).Scan(&notified); err != nil || notified {
			return err
		}
		parent, err := db.GetSession(ctx, a.dbPool, *child.ParentSessionID)
		if err != nil {
			return err
		}
		if parent != nil && !parentEnded(parent) {
			notice := workerEvent{child.ID, "question_resolved", id}
			key := "child-turn-question-resolved-" + id + "-" + m.Status
			principal := runnerPrincipal{Kind: "human", Sub: derefOrEmpty(parent.SpawningAccountID)}
			var problem *APIError
			if dc := a.hub.FindDaemonForSession(parent.ID); dc == nil || dc.CanInputReceipts() {
				problem = a.sendWorkerNotice(ctx, principal, parent.ID, notice, key)
			} else if a.childTurns.first(key) {
				problem = a.sendWorkerNotice(ctx, principal, parent.ID, notice)
			}
			if problem != nil {
				a.childTurns.forget(key)
				return problem
			}
		}
		_, err = a.dbPool.Exec(ctx, `UPDATE messages SET child_parent_notified=true WHERE id=$1`, id)
		return err
	})
}
