package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
)

type boundAskError struct {
	kind  error
	cause string
}

func (e *boundAskError) Error() string {
	if e.cause == "unsupported_live_card" {
		return ld.NativeCardSetupRefusal
	}
	return fmt.Sprintf("%s: %s", e.kind, e.cause)
}
func (e *boundAskError) Unwrap() error { return e.kind }

var errBoundConversation = errors.New("bound clarification is conversation")

func boundConversation(cause string) error {
	return fmt.Errorf("%w: %w", errBoundConversation, boundFailure(cause))
}
func boundReplyMessage(err error) string {
	var link *blackboard.PlanLinkError
	if errors.As(err, &link) {
		return "Not applied: " + link.Error()
	}
	var failure *boundAskError
	if errors.As(err, &failure) {
		switch failure.cause {
		case "live_data_decision_refused":
			return "Not applied: the live binding changed or needs permission in Settings. Ask Main to check the exact revision."
		case "live_data_unavailable":
			return "Not applied: live data approval is unavailable. Try again after the connection recovers."
		case "missing_target":
			return "Not applied: reply to this question."
		case "ambiguous_answer":
			return "Not applied: reply yes or no to this question, or ask Main to revise it."
		case "board_in_use":
			return "Not applied: this board is now in use. Main needs to ask again."
		case "stale_revision", "changed_proposal", "frozen", "missing_milestone", "stale_draft", "blackboard_exists", "invalid_draft", "incomplete_draft", "changed_release", "changed_release_link", "next_release_closed":
			return "Not applied: this proposal changed. Main needs to ask again."
		case "human_login_required", "wrong_owner":
			return "Not applied: sign in as the blackboard owner and answer this question."
		case "login_revoked", "login_expired", "fresh_login_required":
			return "Not applied: sign in again to answer this question."
		case "login_unavailable":
			return "Not applied: login verification is unavailable. Try again."
		case "expired_or_closed", "session_invalidated":
			return "Not applied: this question expired or closed. Main needs to ask again."
		case "already_decided":
			return "Not applied: this question already has a decision."
		case "release_already_released":
			return "Not applied: this release is already released. Ask Main to revise the draft."
		case "release_unavailable", "invalid_release_link":
			return "Not applied: the linked release could not be verified. Ask Main to check the draft and board access."
		case "board_access_revoked", "board_unavailable":
			return "Not applied: board access is unavailable. Check access and try again."
		}
	}
	return "Not applied: the decision could not be saved. Try again."
}
func staleBoundCause(cause string) bool {
	if strings.HasPrefix(cause, "superseded_by=") || cause == "withdrawn_by_main" || cause == "session_invalidated" || cause == "expired_or_closed" {
		return true
	}
	switch cause {
	case "board_in_use", "stale_revision", "changed_proposal", "frozen", "missing_milestone", "stale_draft", "blackboard_exists", "invalid_draft", "incomplete_draft", "changed_release", "changed_release_link", "release_already_released", "next_release_closed":
		return true
	}
	return false
}

// A preflight may read remote permissions, but never while holding a plan lock.
// Inside the transaction the identities/scope, subject, revision and login are checked again.
func sameBoundScope(a, b blackboard.Record) bool {
	if a.Owner != b.Owner || blackboard.WorkspaceKey(a) != blackboard.WorkspaceKey(b) {
		return false
	}
	left, right := blackboard.ReferencedBoards(a), blackboard.ReferencedBoards(b)
	if len(left) != len(right) {
		return false
	}
	for _, board := range left {
		found := false
		for _, other := range right {
			if board == other {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func verifyBoundPlan(ctx context.Context, q db.DBTX, r blackboard.Record, subject boundSubject) error {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT data FROM plan_proposals WHERE id=$1 AND blackboard_id=$2`, subject.ProposalID, r.ID).Scan(&raw); err != nil {
		return err
	}
	var proposal blackboard.Proposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return err
	}
	if proposal.Status != "pending" || proposalDigest(proposal) != subject.Digest {
		return boundFailure("changed_proposal")
	}
	revision := r.PlanRevision
	if subject.MilestoneID != "" {
		found := false
		for _, m := range r.Milestones {
			if m.ID == subject.MilestoneID {
				found = true
				revision = m.Revision
				if m.FrozenAt != nil || m.Stage == "Finalizing" {
					return boundFailure("frozen")
				}
			}
		}
		if !found {
			return boundFailure("missing_milestone")
		}
	}
	if revision != subject.Revision {
		return boundFailure("stale_revision")
	}
	return nil
}

// Supersession is derived from the locked record, never recorded as the person's
// decline. The existing receipt notice is recoverable after a server restart.
func (a *API) recordBoundCancellation(ctx context.Context, tx pgx.Tx, id, cause string) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM bound_asks WHERE message_id=$1 FOR UPDATE`, id).Scan(&status); err != nil {
		return err
	}
	if status != "open" {
		return boundFailure("already_decided")
	}
	receipt, _ := json.Marshal(map[string]string{"ask_id": id, "status": "canceled", "cause": cause})
	if _, err := tx.Exec(ctx, `UPDATE bound_asks SET status='canceled',note=$2,decision=$3 WHERE message_id=$1 AND status='open'`, id, cause, receipt); err != nil {
		return err
	}
	var sid string
	if err := tx.QueryRow(ctx, `UPDATE messages SET status='answered',answer='',answered_by='server',answered_at=now(),no_wait=true WHERE id=$1 RETURNING session_id::text`, id).Scan(&sid); err != nil {
		return err
	}
	at := time.Now()
	eventID := messageEventID(id, "superseded")
	payload, _ := json.Marshal(messagingPayload{MessageID: id, Body: "Question superseded: " + cause + ". Main must raise a fresh question."})
	_, _, err := db.AppendAgentEventTx(ctx, tx, sid, eventID, "update", string(payload), at)
	if err != nil {
		return err
	}
	return nil
}
func (a *API) cancelStaleBoundAsk(ctx context.Context, tx pgx.Tx, id, cause string) error {
	if err := a.recordBoundCancellation(ctx, tx, id, cause); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	a.deliverBoundCancellation(ctx, id)
	return boundFailure(cause)
}
func (a *API) deliverBoundCancellation(ctx context.Context, id string) {
	a.hub.SignalAskAnswered(id)
	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil || row == nil {
		cause := "question_missing"
		if err != nil {
			cause = failurelog.Cause(err)
		}
		failurelog.Default.Event("bound_ask_notice", slog.LevelWarn, "component", "runner", "cause", cause)
		return
	}
	var seq int64
	var payload []byte
	var at time.Time
	eventID := messageEventID(id, "superseded")
	if err = a.dbPool.QueryRow(ctx, `SELECT seq,payload,ts FROM agent_events WHERE session_id=$1 AND client_event_id=$2`, row.SessionID, eventID).Scan(&seq, &payload, &at); err != nil {
		failurelog.Default.Event("bound_ask_notice", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
	} else {
		fanOutServerEvent(a.hub, row.SessionID, eventID, "update", payload, seq, at)
	}
	a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)}) //nolint:contextcheck // committed cancellation has independently bounded delivery
	a.hub.QuestionAnswered(*row, "", "")
}
