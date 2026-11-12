package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/contracts/identity"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/releasecut"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
)

type boundSubject struct {
	LiveBinding *ld.Revision `json:"live_binding,omitempty"`

	Cut          *boundCutSubject      `json:"cut,omitempty"`
	DraftID      string                `json:"draft_id,omitempty"`
	Kind         string                `json:"kind"`
	BlackboardID string                `json:"blackboard_id"`
	ProposalID   blackboard.ProposalID `json:"proposal_id"`
	Revision     int                   `json:"revision"`
	MilestoneID  string                `json:"milestone_id,omitempty"`
	Digest       string                `json:"digest,omitempty"`
	Changes      []blackboard.Change   `json:"changes,omitempty"`
}

func boundFailure(cause string) error {
	failurelog.Default.Event("bound_ask", slog.LevelWarn, "component", "runner", "cause", cause)
	kind := blackboard.ErrConflict
	switch cause {
	case "main_required", "wrong_owner", "human_login_required", "board_access_revoked", "login_unavailable", "login_expired", "login_revoked":
		kind = blackboard.ErrForbidden
	case "invalid_subject", "ambiguous_answer", "missing_target", "unsupported_live_card":
		kind = blackboard.ErrInvalid
	}
	return &boundAskError{kind: kind, cause: cause}
}
func proposalDigest(q blackboard.Proposal) string {
	raw, _ := json.Marshal(q)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a *API) createBoundAsk(ctx context.Context, req postMessageRequest, session *db.SessionRow, ownCredential bool) (messageID string, resultErr error) {
	defer func() {
		if resultErr != nil {
			failurelog.Default.Event("bound_ask_create", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(resultErr))
		}
	}()
	if !ownCredential || req.Kind != "ask" || session.SpawningAccountID == nil || *session.SpawningAccountID == "" || session.Kind != "agent" || session.ParentSessionID != nil || boundSessionEnded(session) || session.ArchivedAt != nil || session.DiscardRequestedAt != nil {
		return "", boundFailure("main_required")
	}
	root, err := blackboardMainSession(ctx, a.dbPool, *session.SpawningAccountID, session.ID)
	if err != nil {
		return "", err
	}
	if root != session.ID {
		return "", boundFailure("main_required")
	}
	subject := *req.Subject
	if subject.Kind == "live_binding" {
		return a.createBoundLiveAsk(ctx, req, session)
	}
	if subject.Kind == "draft" {
		return a.createBoundDraftAsk(ctx, req, session)
	}
	if subject.Kind == "cut" {
		return a.createBoundCutAsk(ctx, req, session)
	}
	if subject.Kind != "plan" || subject.BlackboardID == "" || subject.ProposalID == "" {
		return "", boundFailure("invalid_subject")
	}
	record, err := a.blackboardStore().LoadPartition(ctx, subject.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionCore})
	if err != nil {
		return "", err
	}
	if record.Owner.Kind != "human" || record.Owner.ID != *session.SpawningAccountID || blackboard.WorkspaceKey(record) != session.ID {
		return "", boundFailure("wrong_owner")
	}
	if err = a.boundBoardAccess(ctx, record, record.Owner.ID, "", session.ID); err != nil {
		return "", err
	}
	var q blackboard.Proposal
	var raw []byte
	err = a.dbPool.QueryRow(ctx, `SELECT data FROM plan_proposals WHERE blackboard_id=$1 AND id=$2 AND status='pending'`, record.ID, subject.ProposalID).Scan(&raw)
	if err != nil {
		return "", boundFailure("proposal_not_pending")
	}
	if err = json.Unmarshal(raw, &q); err != nil {
		return "", err
	}
	revision := record.PlanRevision
	if q.MilestoneID != "" {
		found := false
		for _, m := range record.Milestones {
			if m.ID == q.MilestoneID {
				revision = m.Revision
				found = true
				if m.FrozenAt != nil || m.Stage == "Finalizing" {
					return "", boundFailure("frozen")
				}
			}
		}
		if !found {
			return "", boundFailure("missing_milestone")
		}
	}
	if q.Revision != subject.Revision || revision != subject.Revision {
		return "", boundFailure("stale_revision")
	}
	subject.MilestoneID = q.MilestoneID
	subject.Digest = proposalDigest(q)
	subject.Changes = q.Changes
	if len(q.Changes) == 0 {
		return "", boundFailure("invalid_subject")
	}
	var counts [3]int
	thrustCount := 0
	milestoneCount := 0
	var body strings.Builder
	for _, c := range q.Changes {
		switch c.Operation {
		case "add":
			counts[0]++
		case "remove":
			counts[1]++
		case "move":
			counts[2]++
		case "milestone_add":
			milestoneCount++
		case "thrust_add", "thrust_rename", "thrust_merge", "thrust_remove":
			thrustCount++
		case "link", "unlink":
		default:
			return "", boundFailure("invalid_subject")
		}
	}
	switch {
	case milestoneCount > 0:
		fmt.Fprintf(&body, "Plan changes · %d milestone additions, %d thrust changes, %d add, %d remove, %d move\n\n", milestoneCount, thrustCount, counts[0], counts[1], counts[2])
	case q.Changes[0].Operation == "link" || q.Changes[0].Operation == "unlink":
		fmt.Fprintf(&body, "Board decision · 1 %s\n\n", q.Changes[0].Operation)
	case thrustCount == len(q.Changes):
		noun := "changes"
		if thrustCount == 1 {
			noun = "change"
		}
		fmt.Fprintf(&body, "Plan changes · %d thrust %s\n\n", thrustCount, noun)
	case thrustCount > 0:
		fmt.Fprintf(&body, "Plan changes · %d thrust changes, %d add, %d remove, %d move\n\n", thrustCount, counts[0], counts[1], counts[2])
	default:
		fmt.Fprintf(&body, "Plan changes · %d add, %d remove, %d move\n\n", counts[0], counts[1], counts[2])
	}
	for _, milestone := range record.Milestones {
		if milestone.ID == q.MilestoneID {
			fmt.Fprintf(&body, "Milestone: %s\n\n", boundSummaryField(milestone.Name))
		}
	}
	boards := map[string]struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}{}
	boardName := func(id string) (string, error) {
		board, ok := boards[id]
		if !ok {
			if err := a.blackboardSessionService(record, session.ID, session.ID).Board.Call(ctx, "blerg_board_get", map[string]any{"board_id": id}, &board); err != nil {
				return "", boundFailure("board_snapshot_unavailable")
			}
			boards[id] = board
		}
		name := board.Name
		if name == "" {
			name = "Board"
		}
		if board.Key != "" {
			name += " (" + board.Key + ")"
		}
		return name, nil
	}
	for _, c := range q.Changes {
		if c.Operation == "milestone_add" && c.Milestone != nil {
			m := c.Milestone
			fmt.Fprintf(&body, "- Add milestone: %s\n", boundSummaryField(m.Name))
			if m.BoardID != "" {
				fmt.Fprintf(&body, "  Board: %s\n", boundSummaryField(m.BoardID))
			}
			if m.ReleaseID != "" {
				fmt.Fprintf(&body, "  Release: %s\n", boundSummaryField(m.ReleaseID))
			}
			if m.Target != "" {
				fmt.Fprintf(&body, "  Target: %s\n", boundSummaryField(m.Target))
			}
			if m.Description != "" {
				fmt.Fprintf(&body, "  Description: %s\n", boundSummaryField(m.Description))
			}
			continue
		}
		if c.Thrust != nil {
			t := c.Thrust
			name := t.PreviousName
			switch c.Operation {
			case "thrust_add":
				name = t.Name
			case "thrust_rename":
				name += " → " + t.Name
			case "thrust_merge":
				name += " → " + t.DestinationName
			}
			label := strings.TrimPrefix(c.Operation, "thrust_")
			fmt.Fprintf(&body, "- %s thrust: %s\n  Reason: %s\n", strings.ToUpper(label[:1])+label[1:], boundSummaryField(name), boundSummaryField(t.Reason))
			if c.Operation == "thrust_add" {
				fmt.Fprintf(&body, "  Goal: %s\n", boundSummaryField(t.GoalName))
			}
			if c.Operation == "thrust_merge" {
				body.WriteString("  Moves all cards into the destination and removes the source thrust.\n")
			}
			continue
		}
		if c.Operation == "link" || c.Operation == "unlink" {
			name, err := boardName(c.Item.BoardID)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&body, "- %s board: %s\n  Reason: %s\n", strings.ToUpper(c.Operation[:1])+c.Operation[1:], boundSummaryField(name), boundSummaryField(c.Item.Reason))
			continue
		}
		display := c.Item
		if c.Operation != "add" {
			for _, existing := range record.Items {
				if existing.ID == c.Item.ID && c.Item.ID != "" || existing.BoardID == c.Item.BoardID && c.Item.BoardID != "" && existing.CardNumber == c.Item.CardNumber {
					display.ID = existing.ID
					display.Title = existing.Title
					display.BoardID = existing.BoardID
					display.CardNumber = existing.CardNumber
					break
				}
			}
		}
		title := display.Title
		if title == "" && display.ID != "" {
			title = "Untitled item"
		}
		if display.BoardID != "" {
			var card blackboard.Card
			if err := a.blackboardSessionService(record, session.ID, session.ID).Board.Call(ctx, "blerg_card_get", map[string]any{"board_id": display.BoardID, "number": display.CardNumber}, &card); err != nil {
				return "", boundFailure("card_snapshot_unavailable")
			}
			key := card.Reference
			if key == "" {
				if _, err := boardName(display.BoardID); err != nil {
					return "", err
				}
				if prefix := boards[display.BoardID].Key; prefix != "" {
					key = fmt.Sprintf("%s-%d", prefix, display.CardNumber)
				} else {
					key = fmt.Sprintf("#%d", display.CardNumber)
				}
			}
			title = key + " · " + card.Title
		}
		fmt.Fprintf(&body, "- %s: %s\n  Reason: %s\n", strings.ToUpper(c.Operation[:1])+c.Operation[1:], boundSummaryField(title), boundSummaryField(c.Item.Reason))
		if c.Item.ThrustID != "" {
			name := c.Item.ThrustID
			for _, thrust := range record.Thrusts {
				if thrust.ID == c.Item.ThrustID {
					name = thrust.Name
					break
				}
			}
			fmt.Fprintf(&body, "  Thrust: %s\n", boundSummaryField(name))
		}
		if c.Item.Destination != "" {
			fmt.Fprintf(&body, "  Destination: %s\n", boundSummaryField(boundMilestoneName(record, c.Item.Destination)))
		}
	}
	body.WriteString("\nReply to this question with yes to commit these exact changes, or no to decline. Other replies ask for clarification. Expires in 24 hours.")
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := db.LockBlackboardDecision(ctx, tx, subject.BlackboardID)
	if err != nil {
		return "", err
	}
	if !sameBoundScope(record, current) {
		return "", boundFailure("stale_revision")
	}
	if err = verifyBoundPlan(ctx, tx, current, subject); err != nil {
		return "", err
	}
	return a.persistBoundAsk(ctx, tx, session.ID, record.Owner.ID, subject, body.String(), req.NoWait, req.Urgent, req.SessionPosted)
}

func (a *API) persistBoundAsk(ctx context.Context, tx pgx.Tx, sid, owner string, subject boundSubject, body string, noWait, urgent, sessionPosted bool, registeredID ...string) (string, error) {
	if _, err := tx.Exec(ctx, `UPDATE messages SET status='answered',answer='',answered_at=now() WHERE id IN (SELECT message_id FROM bound_asks WHERE workspace_id=$1 AND status='open' AND expires_at<=now())`, sid); err != nil {
		return "", err
	}
	var id, requestedID string
	if len(registeredID) == 1 {
		requestedID = registeredID[0]
	}
	err := tx.QueryRow(ctx, `INSERT INTO messages(id,session_id,kind,body,person_required,owner_only,no_wait,urgent,question_session_posted) VALUES(coalesce(NULLIF($4,'')::uuid,gen_random_uuid()),$1,'ask',$2,true,true,$3,$5,$6) RETURNING id`, sid, body, noWait, requestedID, urgent, sessionPosted).Scan(&id)
	if err != nil {
		return "", err
	}
	var superseded []string
	if subject.Kind == "plan" {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, sid); err != nil {
			return "", err
		}
		rows, e := tx.Query(ctx, `SELECT message_id::text FROM bound_asks WHERE workspace_id=$1 AND status='open' AND subject->>'kind'='plan' FOR UPDATE`, sid)
		if e != nil {
			return "", e
		}
		for rows.Next() {
			var old string
			if e = rows.Scan(&old); e != nil {
				rows.Close()
				return "", e
			}
			superseded = append(superseded, old)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return "", e
		}
	}
	// Append the new ask before its cancellation notices, matching delivery order.
	// All events and both binding states still become visible in one transaction.
	payload, _ := json.Marshal(messagingPayload{Decision: true, Bound: true, LiveBinding: subject.Kind == "live_binding", Body: body, MessageID: id, OwnerOnly: true, NoWait: noWait})
	at := time.Now()
	seq, _, err := db.AppendAgentEventTx(ctx, tx, sid, messageEventID(id, "ask"), "ask", string(payload), at)
	if err != nil {
		return "", err
	}
	for _, old := range superseded {
		if err = a.recordBoundCancellation(ctx, tx, old, "superseded_by="+id); err != nil {
			return "", err
		}
	}
	var epoch int64
	if err = tx.QueryRow(ctx, `SELECT bound_ask_epoch FROM sessions WHERE id=$1 FOR SHARE`, sid).Scan(&epoch); err != nil {
		return "", err
	}
	live, err := db.GetSession(ctx, tx, sid)
	if err != nil {
		return "", err
	}
	if live == nil || boundSessionEnded(live) || live.ArchivedAt != nil || live.DiscardRequestedAt != nil || live.SpawningAccountID == nil || *live.SpawningAccountID != owner {
		return "", boundFailure("main_required")
	}
	raw, _ := json.Marshal(subject)
	if _, err = tx.Exec(ctx, `INSERT INTO bound_asks(message_id,workspace_id,blackboard_id,owner_id,subject,session_epoch) VALUES($1,$2,$3,$4,$5,$6)`, id, sid, nullableBoundBlackboardID(subject.BlackboardID), owner, raw, epoch); err != nil {
		return "", boundFailure("pending_ask_conflict")
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	fanOutServerEvent(a.hub, sid, messageEventID(id, "ask"), "ask", payload, seq, at)
	for _, old := range superseded {
		a.deliverBoundCancellation(ctx, old)
	}
	return id, nil
}

// Creation uses Main's stored access; answers recheck the live owner's login.
func (a *API) boundBoardAccess(ctx context.Context, r blackboard.Record, owner, login, sid string) error {
	caller := a.sessionBoardCaller()
	for _, board := range blackboard.ReferencedBoards(r) {
		if caller == nil {
			return boundFailure("board_unavailable")
		}
		if login == "" {
			row, err := db.GetSession(ctx, a.dbPool, sid)
			if err != nil {
				return err
			}
			if row == nil {
				return boundFailure("main_missing")
			}
			level, err := a.effectiveBoardLevel(ctx, row, board)
			if err != nil {
				return err
			}
			if !mcpgw.BoardLevelAllows(level.Level, "blerg_board_get") {
				return boundFailure("board_access_revoked")
			}
			// The normal session service resolves Main's stored proof on each call.
			var out any
			if err = a.blackboardSessionService(r, sid, sid).Board.Call(ctx, "blerg_board_get", map[string]any{"board_id": board}, &out); err != nil {
				return boundFailure("board_access_revoked")
			}
			continue
		}
		proof := mcpgw.Proof{AccountID: owner, SessionID: login}
		args, _ := json.Marshal(map[string]string{"board_id": board})
		raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: board, SessionRef: sid, Tool: "blerg_board_get", Arguments: args, ReadOnly: true})
		if err != nil {
			return boundFailure("board_access_revoked")
		}
		var out any
		if boardToolResult(raw, &out) != nil {
			return boundFailure("board_access_revoked")
		}
	}
	return nil
}

// Bound answers are intercepted before acceptSessionInput. Any failure is consumed
// here, never handed to an engine or its replay queue.
func (a *API) answerBoundAsk(ctx context.Context, msg protocol.AgentUserMessage, p identity.Principal) (handled bool, resultErr error) {
	defer func() {
		if resultErr != nil {
			failurelog.Default.Event("bound_ask_answer", slog.LevelWarn, "component", "runner", "cause", blackboardFailureCause(resultErr))
		}
	}()
	if a.dbPool == nil {
		return msg.AnswerMessageID != "", boundFailure("storage_unavailable")
	}
	var bid, id, preStatus string
	if msg.AnswerMessageID == "" {
		text := strings.ToLower(strings.TrimSpace(msg.Text))
		if text != "yes" && text != "approve" && text != "no" && text != "decline" {
			return false, nil
		}
		err := a.dbPool.QueryRow(ctx, `SELECT coalesce(blackboard_id,''),message_id::text FROM bound_asks WHERE workspace_id=$1 AND status='open'`, msg.SessionID).Scan(&bid, &id)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			_ = boundFailure("storage_unavailable")
			return false, nil
		}
		_ = a.boundAskNote(ctx, id, "Not applied: reply to this question.")
		return true, boundConversation("missing_target")
	}

	var preRaw []byte
	err := a.dbPool.QueryRow(ctx, `SELECT coalesce(blackboard_id,''),message_id::text,status,subject FROM bound_asks WHERE message_id::text=$1`, msg.AnswerMessageID).Scan(&bid, &id, &preStatus, &preRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, boundFailure("storage_unavailable")
	}
	if err := a.boundLoginLive(p); err != nil {
		return true, err
	}
	answer := strings.ToLower(strings.TrimSpace(msg.Text))
	var checked blackboard.Record
	hasProof := false
	var cutPreflightErr error
	var milestoneBoard blackboard.Board
	var preSubject boundSubject
	if err = json.Unmarshal(preRaw, &preSubject); err != nil {
		return true, err
	}
	if preSubject.Kind == "live_binding" {
		return a.answerBoundLiveAsk(ctx, msg, p, id)
	}
	if preStatus == "open" && (answer == "yes" || answer == "approve") {
		if bid != "" {
			checked, err = a.blackboardStore().LoadPartition(ctx, bid, blackboard.Partition{Kind: blackboard.PartitionCore})
		} else {
			checked, err = a.preflightBoundDraft(ctx, id, msg.SessionID, p.Sub, p.Sid)
		}
		if err != nil {
			return true, err
		}
		if checked.Owner.Kind != "human" || checked.Owner.ID != p.Sub || blackboard.WorkspaceKey(checked) != msg.SessionID {
			return true, boundFailure("wrong_owner")
		}
		if preSubject.Kind == "cut" {
			_, cutPreflightErr = a.preflightBoundCut(ctx, checked, preSubject, p.Sid, msg.SessionID)
			// Keep remote work outside the transaction, but settle lifecycle
			// invalidation under its session lock before returning this refusal.
		} else if err = a.boundBoardAccess(ctx, checked, p.Sub, p.Sid, msg.SessionID); err != nil {
			return true, err
		}
		if bid != "" {
			// Capture the immutable binding before external reads. The transaction
			// below rechecks its proposal digest and current revision under lock.
			var raw []byte
			if err = a.dbPool.QueryRow(ctx, `SELECT subject FROM bound_asks WHERE message_id=$1`, id).Scan(&raw); err != nil {
				return true, err
			}
			var subject boundSubject
			if err = json.Unmarshal(raw, &subject); err != nil {
				return true, err
			}
			if subject.Kind == "plan" {
				service, e := a.blackboardSessionService(checked, msg.SessionID, blackboard.WorkspaceKey(checked)).PreflightMilestones(ctx, checked, subject.Changes)
				if e != nil {
					return true, e
				}
				milestoneBoard = service.Board
			}
			target := blackboard.Record{}
			for _, c := range subject.Changes {
				if c.Operation == "link" {
					target.Links = append(target.Links, blackboard.Link{BoardID: c.Item.BoardID})
				}
			}
			if err = a.boundBoardAccess(ctx, target, p.Sub, p.Sid, msg.SessionID); err != nil {
				return true, err
			}
		}
		hasProof = true
	}
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return true, boundFailure("storage_unavailable")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var record blackboard.Record
	if bid != "" {
		record, err = db.LockBlackboardDecision(ctx, tx, bid)
	} else {
		if err = db.LockDraftWorkspace(ctx, tx, msg.SessionID); err == nil {
			var currentOwner string
			err = tx.QueryRow(ctx, `SELECT coalesce(spawning_account_id,'') FROM sessions WHERE id=$1`, msg.SessionID).Scan(&currentOwner)
			record = blackboard.Record{Owner: blackboard.Actor{Kind: "human", ID: currentOwner}, WorkspaceID: msg.SessionID}
		}
	}
	if err != nil {
		return true, err
	}
	var subject boundSubject
	var raw []byte
	var status, owner, sid string
	var expired bool
	var askEpoch int64
	err = tx.QueryRow(ctx, `SELECT subject,status,owner_id,workspace_id::text,expires_at<=now(),session_epoch FROM bound_asks WHERE message_id=$1 FOR UPDATE`, id).Scan(&raw, &status, &owner, &sid, &expired, &askEpoch)
	if err != nil {
		return true, err
	}
	if owner != p.Sub || sid != msg.SessionID {
		return true, boundFailure("wrong_owner")
	}
	if err = json.Unmarshal(raw, &subject); err != nil {
		return true, err
	}
	if status != "open" {
		var prior string
		if err := tx.QueryRow(ctx, `SELECT coalesce(answer,'') FROM messages WHERE id=$1`, id).Scan(&prior); err == nil && prior == msg.Text && (status == "committed" || status == "declined") {
			return true, nil
		}
		return true, boundFailure("already_decided")
	}
	var messageOpen bool
	if err = tx.QueryRow(ctx, `SELECT status='open' FROM messages WHERE id=$1 FOR UPDATE`, id).Scan(&messageOpen); err != nil {
		return true, err
	}
	// Lock order: subject, binding, message, then session. Session transitions
	// only bump their own epoch; they never wait for messages or bindings.
	var liveEpoch int64
	if err = tx.QueryRow(ctx, `SELECT bound_ask_epoch FROM sessions WHERE id=$1 FOR SHARE`, sid).Scan(&liveEpoch); err != nil {
		return true, err
	}
	session, err := db.GetSession(ctx, tx, sid)
	if err != nil {
		return true, err
	}
	if !messageOpen || expired {
		return true, a.cancelStaleBoundAsk(ctx, tx, id, "expired_or_closed")
	}
	if session == nil || boundSessionEnded(session) || session.ArchivedAt != nil || session.DiscardRequestedAt != nil || liveEpoch != askEpoch {
		return true, a.cancelStaleBoundAsk(ctx, tx, id, "session_invalidated")
	}
	if session.SpawningAccountID == nil || *session.SpawningAccountID != owner {
		return true, a.cancelStaleBoundAsk(ctx, tx, id, "session_invalidated")
	}
	if record.Owner.ID != p.Sub || record.Owner.Kind != "human" {
		return true, boundFailure("wrong_owner")
	}
	// Closing a question always cancels, even if its subject or access went stale.
	if answer != "" {
		if answer != "yes" && answer != "approve" && answer != "no" && answer != "decline" && answer != "" {
			_ = tx.Rollback(ctx)
			_ = a.boundAskNote(ctx, id, "Not applied: reply yes or no to this question, or ask Main to revise it.")
			return true, boundConversation("ambiguous_answer")
		}
		if answer == "yes" || answer == "approve" {
			switch {
			case subject.Kind == "cut":
				if !hasProof || !sameBoundScope(checked, record) {
					return true, a.cancelStaleBoundAsk(ctx, tx, id, "stale_revision")
				}
				if cutPreflightErr != nil {
					var failure *boundAskError
					if errors.As(cutPreflightErr, &failure) && staleBoundCause(failure.cause) {
						return true, a.cancelStaleBoundAsk(ctx, tx, id, failure.cause)
					}
					return true, cutPreflightErr
				}
				err = verifyBoundCut(record, subject)
			case subject.Kind == "draft":
				if !hasProof {
					return true, boundFailure("board_access_revoked")
				}
				err = a.validateBoundDraft(ctx, tx, subject, p.Sub, sid, checked)
			case !hasProof || !sameBoundScope(checked, record):
				return true, a.cancelStaleBoundAsk(ctx, tx, id, "stale_revision")
			default:
				err = verifyBoundPlan(ctx, tx, record, subject)
			}
			if err != nil {
				var failure *boundAskError
				if errors.As(err, &failure) {
					return true, a.cancelStaleBoundAsk(ctx, tx, id, failure.cause)
				}
				return true, err
			}

		}
	}
	if err := a.boundLoginLive(p); err != nil {
		return true, err
	}
	selected := []blackboard.ItemID{}
	state := "declined"
	switch answer {
	case "":
		state = "canceled"
	case "yes", "approve":
		state = "committed"
		for _, c := range subject.Changes {
			selected = append(selected, c.ID)
		}
	}
	if subject.Kind == "plan" && state != "canceled" {
		// Decide validates links before persisting any plan changes. Keep the
		// binding open until that validation succeeds so stale links can cancel.
		err = (blackboard.Service{Store: db.BlackboardStore{Pool: a.dbPool, Tx: tx}, BlackboardID: bid, Board: milestoneBoard}).Decide(ctx, blackboard.Person{Actor: blackboard.Actor{ID: p.Sub, Kind: "human"}, LoginID: p.Sid}, subject.MilestoneID, subject.ProposalID, subject.Revision, selected)
		if err != nil {
			var link *blackboard.PlanLinkError
			if errors.As(err, &link) && link.Cause == "board_in_use" {
				return true, a.cancelStaleBoundAsk(ctx, tx, id, link.Cause)
			}
			return true, err
		}
	}
	receipt, _ := json.Marshal(map[string]any{"ask_id": id, "status": state, "proposal_id": subject.ProposalID, "owner": p.Sub, "login": p.Sid, "client_id": msg.ClientID})
	if _, err = tx.Exec(ctx, `UPDATE bound_asks SET status=$2,decision=$3,decision_input_id=NULLIF($4,'') WHERE message_id=$1`, id, state, receipt, msg.ClientID); err != nil {
		return true, err
	}
	if state != "canceled" {
		switch subject.Kind {
		case "draft":
			var result string
			result, err = a.applyBoundDraft(ctx, tx, subject, p.Sub, sid, state, id)
			if err == nil {
				_, err = tx.Exec(ctx, `UPDATE bound_asks SET result_id=NULLIF($2,'') WHERE message_id=$1`, id, result)
			}
		case "cut":
			if state == "committed" {
				err = a.authorizeBoundCut(ctx, tx, subject, record, p, id)
			}
		}
		if err != nil {
			return true, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE messages SET status='answered',answer=$2,answered_at=now(),answered_by=$3 WHERE id=$1`, id, msg.Text, "person:"+p.Sub); err != nil {
		return true, err
	}
	payload, _ := json.Marshal(askAnswerPayload{Text: msg.Text, Source: "ask_answer", MessageID: id, ClientID: msg.ClientID})
	seq, _, err := db.AppendAgentEventTx(ctx, tx, sid, messageEventID(id, "answer"), "user_message", string(payload), time.Now())
	if err != nil {
		return true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	if subject.Kind == "draft" {
		a.broadcastBlackboardDraftChanged(&http.Request{}, sid) //nolint:contextcheck // independent bounded browser visibility context
	}
	fanOutServerEvent(a.hub, sid, messageEventID(id, "answer"), "user_message", payload, seq, time.Now())
	if subject.Kind == "cut" && state == "committed" {
		select {
		case a.releaseCutWake <- struct{}{}:
		default:
		}
	}
	a.hub.SignalAskAnswered(id)
	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err == nil && row != nil {
		a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)}) //nolint:contextcheck // broadcast owns a bounded lifetime after the committed decision
		if row.NoWait {
			a.hub.QuestionAnswered(*row, msg.Text, p.Sub)
		}
	}
	return true, nil
}
func (a *API) boundAskNote(ctx context.Context, id, note string) (resultErr error) {
	defer func() {
		if resultErr != nil {
			failurelog.Default.Event("bound_ask_note", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(resultErr))
		}
	}()
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sid string
	if err = tx.QueryRow(ctx, `UPDATE bound_asks SET note=$2 WHERE message_id=$1 AND status='open' RETURNING workspace_id::text`, id, note).Scan(&sid); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE messages SET body=(SELECT payload->>'body' FROM agent_events WHERE session_id=$3 AND client_event_id=$4) || chr(10) || chr(10) || $2 WHERE id=$1 AND status='open'`, id, note, sid, messageEventID(id, "ask")); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"message_id": id, "text": note})
	eventID := newUUID()
	at := time.Now()
	seq, _, err := db.AppendAgentEventTx(ctx, tx, sid, eventID, "bound_ask_note", string(payload), at)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	fanOutServerEvent(a.hub, sid, eventID, "bound_ask_note", payload, seq, at)
	owner := ""
	_ = a.dbPool.QueryRow(ctx, `SELECT owner_id FROM bound_asks WHERE message_id=$1`, id).Scan(&owner)
	row, err := db.GetMessage(ctx, a.dbPool, id)
	if err == nil && row != nil {
		a.hub.BroadcastJSONPerAccount(func(account string) any { //nolint:contextcheck // viewer-scoped broadcast has its own bounded lifetime
			view := messageRowToInfo(*row)
			view.Answerable = account == owner
			return protocol.MessageCreated{Type: "message_created", Message: view, Quiet: true}
		})
	}
	return err
}

// HTTP reply and the browser socket share the same authenticated decision path.
func (a *API) handleBoundHTTP(w http.ResponseWriter, r *http.Request, id string, req postMessageReplyRequest) bool {
	var exists bool
	if err := a.dbPool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM bound_asks WHERE message_id::text=$1)`, id).Scan(&exists); err != nil {
		writeError(w, 503, "Decision unavailable.")
		return true
	}
	if !exists {
		return false
	}
	if req.RevisionSeq != nil && *req.RevisionSeq != 0 {
		writeError(w, 409, "Main revised this question; reload before answering")
		return true
	}
	p, ok := a.authBrowser(w, r)
	if !ok {
		return true
	}
	var sid string
	if err := a.dbPool.QueryRow(r.Context(), `SELECT workspace_id::text FROM bound_asks WHERE message_id=$1`, id).Scan(&sid); err != nil {
		writeError(w, 503, "Decision unavailable.")
		return true
	}
	_, err := a.answerBoundAsk(r.Context(), protocol.AgentUserMessage{SessionID: sid, AnswerMessageID: id, Text: req.Answer, ClientID: req.ClientID}, p)
	if err != nil {
		if errors.Is(err, errBoundConversation) {
			_, problem := a.SendMessage(r.Context(), runnerPrincipal{Kind: p.Kind, Sub: p.Sub, Sid: p.Sid, OnBehalfOf: p.OnBehalfOf, ExpiresAt: p.ExpiresAt}, sid, req.Answer, "chat", newUUID())
			if problem != nil {
				writeAPIError(w, problem)
				return true
			}
		}
		var failure *boundAskError
		if errors.As(err, &failure) && failure.cause == "fresh_login_required" {
			writeError(w, http.StatusUnauthorized, boundReplyMessage(err))
			return true
		}
		writeError(w, 409, boundReplyMessage(err))
		return true
	}

	receipt, _, err := a.boundReceipt(r.Context(), id)
	if err != nil {
		writeError(w, 503, "Decision receipt unavailable.")
		return true
	}
	writeJSON(w, 200, map[string]any{"id": id, "status": "recorded", "decision_receipt": receipt})
	return true
}

// boundReceipt is deliberately a public result, not the protected login/provenance row.
func (a *API) boundReceipt(ctx context.Context, id string) (map[string]any, bool, error) {
	var raw []byte
	var status, note, resultID string
	err := a.dbPool.QueryRow(ctx, `SELECT subject,status,note,coalesce(result_id,'') FROM bound_asks WHERE message_id::text=$1`, id).Scan(&raw, &status, &note, &resultID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	var subject boundSubject
	if err = json.Unmarshal(raw, &subject); err != nil {
		return nil, true, err
	}
	if subject.Kind == "live_binding" && subject.LiveBinding != nil {
		return map[string]any{"ask_id": id, "status": status, "binding_id": subject.LiveBinding.Scope.Binding, "revision": subject.Revision, "digest": subject.Digest}, true, nil
	}
	if subject.Kind == "cut" {
		receipt := map[string]any{"ask_id": id, "status": status, "release_id": subject.Cut.ReleaseID, "next_release_id": subject.Cut.NextReleaseID, "revision": subject.Revision}
		if status == "committed" {
			var execution, cause string
			var evidence []byte
			if err = a.dbPool.QueryRow(ctx, `SELECT status,cause,receipt FROM blackboard_cut_executions WHERE ask_id=$1`, id).Scan(&execution, &cause, &evidence); err != nil {
				return nil, true, err
			}
			receipt["status"] = "authorized"
			receipt["execution"] = execution
			if execution == "failed" {
				receipt["cause"] = cause
			}
			if execution == "executed" {
				var cut releasecut.Receipt
				if json.Unmarshal(evidence, &cut) == nil {
					receipt["cut_receipt_id"] = cut.ID
					receipt["released_at"] = cut.ReleasedAt
				}
			}
		} else if status == "canceled" && staleBoundCause(note) {
			receipt["cause"] = note
		}
		return receipt, true, nil
	}
	if subject.Kind == "draft" {
		receipt := map[string]any{"ask_id": id, "status": status, "draft_id": subject.DraftID, "revision": subject.Revision, "blackboard_id": resultID}
		if status == "canceled" && staleBoundCause(note) {
			receipt["cause"] = note
		}
		return receipt, true, nil
	}
	revision := subject.Revision
	if status == "committed" {
		revision++
	}
	receipt := map[string]any{"ask_id": id, "status": status, "proposal_id": subject.ProposalID, "revision": revision}
	if status == "canceled" && staleBoundCause(note) {
		receipt["cause"] = note
	}
	return receipt, true, nil
}

// Socket authentication happened at upgrade; a decision must recheck the live
// login even for a plan with no linked board (and therefore no board auth call).
func (a *API) boundLoginLive(p identity.Principal) error {
	if p.Kind != "human" || p.Sub == "" || p.Sid == "" || p.OnBehalfOf != "" {
		return boundFailure("human_login_required")
	}
	if a.coreAuth == nil || a.coreAuth.StaleBeyondCeiling() {
		return boundFailure("login_unavailable")
	}
	if p.ExpiresAt == 0 || time.Now().Unix() >= p.ExpiresAt {
		return boundFailure("login_expired")
	}
	if a.coreAuth.RevokedFor("", p.Lineage, p.Sub, p.Sid, p.IssuedAt) {
		return boundFailure("login_revoked")
	}
	return nil
}

// Keep untrusted multi-line fields inside one labeled operation in the server summary.
func boundSummaryField(value string) string {
	if strings.ContainsAny(value, "\r\n\t") {
		return strconv.Quote(value)
	}
	return value
}

func nullableBoundBlackboardID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// A move can target a named milestone; keep arbitrary destinations readable too.
func boundMilestoneName(record blackboard.Record, id string) string {
	for _, milestone := range record.Milestones {
		if milestone.ID == id {
			return milestone.Name
		}
	}
	return id
}
