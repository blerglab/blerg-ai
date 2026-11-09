package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/contracts/releasecut"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/jackc/pgx/v5"
)

type releaseCutExecution struct {
	AskID, WorkspaceID, BlackboardID, MilestoneID string
	LinkID                                        int64
	Principal                                     identity.Principal
	Subject                                       boundSubject
	Deadline                                      time.Time
}

func cutDiagnostic(cause string) {
	failurelog.Default.Event("release_cut", slog.LevelWarn, "component", "runner", "cause", cause)
}

// RunBlackboardReleaseCuts is owned by the server lifetime, independent of any daemon.
func (a *API) RunBlackboardReleaseCuts(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	a.runReleaseCutLoop(ctx, ticker.C)
}
func (a *API) runReleaseCutLoop(ctx context.Context, ticks <-chan time.Time) {
	a.processReleaseCuts(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			a.processReleaseCuts(ctx)
		case <-a.releaseCutWake:
			a.processReleaseCuts(ctx)
		}
	}
}
func (a *API) processReleaseCuts(ctx context.Context) {
	if a.dbPool == nil {
		return
	}
	for range 10 {
		execution, err := a.claimReleaseCut(ctx)
		if err != nil {
			cutDiagnostic("execution_store_unavailable")
			break
		}
		if execution == nil {
			break
		}
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = a.executeReleaseCut(callCtx, *execution)
		cancel()
		if err != nil {
			cutDiagnostic("execution_unavailable")
		}
	}
	rows, err := a.dbPool.Query(ctx, `SELECT m.blackboard_id FROM milestones m LEFT JOIN blackboard_cut_observations o ON o.milestone_id=m.id WHERE coalesce(m.data->>'cut_receipt_id','')='' AND m.board_id IS NOT NULL AND m.release_id IS NOT NULL AND coalesce(m.data->>'legacy_evidence_id','0')='0' AND coalesce(o.cause,'')<>'released_without_receipt' AND (o.next_check_at IS NULL OR o.next_check_at<=clock_timestamp()) ORDER BY o.observed_at NULLS FIRST,m.id LIMIT 100`)
	if err != nil {
		cutDiagnostic("reconciliation_store_unavailable")
		return
	}
	ids := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if err != nil || rows.Err() != nil {
		cutDiagnostic("reconciliation_store_unavailable")
	}
	rows.Close()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		record, err := a.blackboardStore().LoadPartition(ctx, id, blackboard.Partition{Kind: blackboard.PartitionCore})
		if err != nil {
			cutDiagnostic("reconciliation_store_unavailable")
			continue
		}
		a.reconcileBlackboardCuts(ctx, record)
	}
}
func (a *API) claimReleaseCut(ctx context.Context) (*releaseCutExecution, error) {
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var execution releaseCutExecution
	var principal, subject []byte
	err = tx.QueryRow(ctx, `SELECT e.ask_id::text,e.workspace_id::text,e.blackboard_id,e.milestone_id,e.link_id,e.principal,e.deadline,b.subject FROM blackboard_cut_executions e JOIN bound_asks b ON b.message_id=e.ask_id WHERE b.status='committed' AND e.next_attempt_at<=clock_timestamp() AND (e.status='authorized' OR e.status='executing' AND e.lease_until<clock_timestamp()) ORDER BY e.authorized_at LIMIT 1 FOR UPDATE OF e SKIP LOCKED`).Scan(&execution.AskID, &execution.WorkspaceID, &execution.BlackboardID, &execution.MilestoneID, &execution.LinkID, &principal, &execution.Deadline, &subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(principal, &execution.Principal.Claims); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(subject, &execution.Subject); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE blackboard_cut_executions SET status='executing',lease_until=clock_timestamp()+interval '3 minutes',next_attempt_at=clock_timestamp()+interval '60 seconds' WHERE ask_id=$1`, execution.AskID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &execution, nil
}
func (a *API) executeReleaseCut(ctx context.Context, e releaseCutExecution) error {
	record, err := a.blackboardStore().LoadPartition(ctx, e.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionCore})
	if err != nil {
		if errors.Is(err, blackboard.ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
			return a.failReleaseCut(ctx, e, "subject_unavailable")
		}
		return a.deferReleaseCut(ctx, e, "db_unavailable")
	}
	var ownerKind, ownerID string
	if err = a.dbPool.QueryRow(ctx, `SELECT owner_kind,owner_id FROM blackboards WHERE id=$1`, record.ID).Scan(&ownerKind, &ownerID); err != nil {
		if errors.Is(err, blackboard.ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
			return a.failReleaseCut(ctx, e, "subject_unavailable")
		}
		return a.deferReleaseCut(ctx, e, "db_unavailable")
	}
	if ownerKind != record.Owner.Kind || ownerID != record.Owner.ID {
		return a.failReleaseCut(ctx, e, "ownership_changed")
	}
	if e.Subject.Cut == nil || e.Subject.Kind != "cut" || record.Owner.ID != e.Principal.Sub || blackboard.WorkspaceKey(record) != e.WorkspaceID {
		return a.failReleaseCut(ctx, e, "ownership_changed")
	}
	c := e.Subject.Cut
	// Recovery reads use Main's current stored read access, even when the original
	// answer login has since expired. An existing receipt is evidence, not a retry.
	proof, err := a.cutBoardProof(ctx, record, c.BoardID, e.WorkspaceID, "", false)
	if err != nil {
		return a.cutProofFailure(ctx, e, err)
	}
	view, err := a.readCutRelease(ctx, proof, c.BoardID, c.ReleaseID, e.WorkspaceID)
	if err != nil {
		if cutFailureCause(err, "") == "reconciliation_access_expired_or_revoked" {
			return a.failReleaseCut(ctx, e, "lost_credentials")
		}
		return a.deferReleaseCut(ctx, e, "board_outcome_unverified")
	}
	if view.Release.Status == "released" {
		if err = verifyCutExecutionReceipt(e, view.Receipt); err != nil {
			return a.failReleaseCut(ctx, e, "release_already_closed")
		}
		return a.recordExecutedCut(ctx, e, *view.Receipt)
	}
	if !time.Now().Before(e.Deadline) {
		return a.failReleaseCut(ctx, e, "execution_deadline")
	}
	if err = a.cutLoginSessionLive(ctx, e.Principal); err != nil {
		return a.cutProofFailure(ctx, e, err)
	}
	if _, err = a.cutBoardProof(ctx, record, c.BoardID, e.WorkspaceID, e.Principal.Sid, true); err != nil {
		return a.cutProofFailure(ctx, e, err)
	}
	if err = a.checkCutExecutionLink(ctx, e, record); err != nil {
		if cutFailureCause(err, "") != "changed_release_link" {
			return a.deferReleaseCut(ctx, e, "db_unavailable")
		}
		return a.failReleaseCut(ctx, e, "changed_release_link")
	}
	args, _ := json.Marshal(map[string]any{"board_id": c.BoardID, "release_id": c.ReleaseID, "next_release_id": c.NextReleaseID, "cut_ask_id": e.AskID, "card_digest": c.CardDigest, "execution_deadline": e.Deadline})
	caller := a.sessionBoardCaller()
	if caller == nil {
		return a.deferReleaseCut(ctx, e, "board_unavailable")
	}
	raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: mcpgw.Proof{AccountID: e.Principal.Sub, SessionID: e.Principal.Sid}, BoardID: c.BoardID, SessionRef: e.WorkspaceID, Tool: "blerg_release_close", Arguments: args, CutExecutor: true})
	if errors.Is(err, mcpgw.ErrConnectionGone) {
		return a.failReleaseCut(ctx, e, "lost_credentials")
	}
	knownRefusal := false
	if err == nil {
		var out struct {
			Executed bool   `json:"executed"`
			Cause    string `json:"cause"`
		}
		if failure := boardToolResult(raw, &out); failure == nil {
			if !out.Executed {
				if terminalCutCause(out.Cause) {
					return a.failReleaseCut(ctx, e, out.Cause)
				}
				return a.failReleaseCut(ctx, e, "cut_refused")
			}
		} else if failure.Status == 401 || failure.Status == 403 {
			return a.failReleaseCut(ctx, e, "lost_credentials")
		} else {
			knownRefusal = true
		}
	}
	// A transport failure may follow a committed close. Read the receipt before
	// deciding whether another call is eligible; the board also deduplicates by ask.
	view, readErr := a.readCutRelease(ctx, proof, c.BoardID, c.ReleaseID, e.WorkspaceID)
	if readErr == nil && view.Release.Status == "released" {
		if verifyCutExecutionReceipt(e, view.Receipt) == nil {
			return a.recordExecutedCut(ctx, e, *view.Receipt)
		}
		return a.failReleaseCut(ctx, e, "release_already_closed")
	}
	if knownRefusal && readErr == nil {
		return a.failReleaseCut(ctx, e, "cut_refused")
	}
	return a.deferReleaseCut(ctx, e, "board_outcome_unverified")
}
func terminalCutCause(cause string) bool {
	switch cause {
	case "archive_gate_deny", "archive_gate_hold", "active_card_sessions", "stale_version", "card_digest_mismatch", "main_ended", "release_already_closed", "lost_credentials", "execution_deadline":
		return true
	}
	return false
}
func verifyCutExecutionReceipt(e releaseCutExecution, receipt *releasecut.Receipt) error {
	if receipt == nil || e.Subject.Cut == nil {
		return errors.New("missing receipt")
	}
	c := e.Subject.Cut
	if receipt.AskID != e.AskID || receipt.OwnerID != e.Principal.Sub || receipt.NextReleaseID != c.NextReleaseID || receipt.CardDigest != c.CardDigest || !receipt.ExecutionDeadline.Equal(e.Deadline) {
		return errors.New("receipt cause mismatch")
	}
	return validateReleaseCutReceipt(*receipt, c.BoardID, c.ReleaseID)
}
func (a *API) deferReleaseCut(ctx context.Context, e releaseCutExecution, cause string) error {
	cutDiagnostic(cause)
	if !time.Now().Before(e.Deadline) {
		return a.failReleaseCut(ctx, e, "execution_deadline")
	}
	_, err := a.dbPool.Exec(ctx, `UPDATE blackboard_cut_executions SET status='authorized',cause=$2,lease_until=NULL,next_attempt_at=clock_timestamp()+interval '60 seconds' WHERE ask_id=$1 AND status='executing'`, e.AskID, cause)
	return err
}
func (a *API) recordExecutedCut(ctx context.Context, e releaseCutExecution, receipt releasecut.Receipt) error {
	if err := verifyCutExecutionReceipt(e, &receipt); err != nil {
		return a.failReleaseCut(ctx, e, "invalid_cut_receipt")
	}
	raw, _ := json.Marshal(receipt)
	_, err := a.dbPool.Exec(ctx, `UPDATE blackboard_cut_executions SET status='executed',cause='',lease_until=NULL,receipt=$2 WHERE ask_id=$1`, e.AskID, raw)
	if err != nil {
		return err
	}
	record, err := a.blackboardStore().LoadPartition(ctx, e.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionCore})
	if err != nil {
		return err
	}
	a.reconcileBlackboardCutsNow(ctx, record, e.MilestoneID)
	return nil
}
func (a *API) failReleaseCut(ctx context.Context, e releaseCutExecution, cause string) error {
	cutDiagnostic(cause)
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM blackboard_cut_executions WHERE ask_id=$1 FOR UPDATE`, e.AskID).Scan(&status); err != nil {
		return err
	}
	if status == "failed" || status == "executed" {
		return nil
	}
	body := "Cut authorized, not executed: " + strings.ReplaceAll(cause, "_", " ") + "\n\nNo new attempt was executed. If an earlier response was lost, its outcome remains unverified until the board receipt can be read. Ask Main to check the board and resolve this cause before raising a fresh bound cut ask. The earlier authorization will not be retried."
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO messages(session_id,kind,body,person_required,owner_only,no_wait) VALUES($1,'ask',$2,true,true,true) RETURNING id::text`, e.WorkspaceID, body).Scan(&id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE blackboard_cut_executions SET status='failed',cause=$2,lease_until=NULL,failure_message_id=$3 WHERE ask_id=$1`, e.AskID, cause, id); err != nil {
		return err
	}
	payload, _ := json.Marshal(messagingPayload{Body: body, MessageID: id, OwnerOnly: true, NoWait: true})
	at := time.Now()
	seq, _, err := db.AppendAgentEventTx(ctx, tx, e.WorkspaceID, messageEventID(id, "ask"), "ask", string(payload), at)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	fanOutServerEvent(a.hub, e.WorkspaceID, messageEventID(id, "ask"), "ask", payload, seq, at)
	// The ordinary ask projection puts this in the existing Needs you queue.
	a.hub.ChildActivity(e.WorkspaceID)
	a.waiters.signal(e.AskID)
	return nil
}

// The answer JWT is evidence of the act, not the lifetime of its authorization.
// Recheck the original login chain against revocations and Core's live session.
func (a *API) cutLoginSessionLive(ctx context.Context, p identity.Principal) error {
	if p.Kind != "human" || p.Sub == "" || p.Sid == "" || p.OnBehalfOf != "" {
		return boundFailure("lost_credentials")
	}
	if a.coreAuth == nil || a.coreAuth.StaleBeyondCeiling() {
		return boundFailure("core_unreachable")
	}
	if a.coreAuth.RevokedFor("", p.Lineage, p.Sub, p.Sid, p.IssuedAt) {
		return boundFailure("lost_credentials")
	}
	if err := verifyCorePrincipal(ctx, nil, a.coreURL, a.coreInternalKey, p.Sub, coreProof{SessionID: p.Sid}); err != nil {
		if errors.Is(err, errCorePrincipalRefused) {
			return boundFailure("lost_credentials")
		}
		return boundFailure("core_unreachable")
	}
	return nil
}
func (a *API) cutProofFailure(ctx context.Context, e releaseCutExecution, err error) error {
	cause := cutFailureCause(err, "db_unavailable")
	switch cause {
	case "reconciliation_main_ended", "main_required":
		return a.failReleaseCut(ctx, e, "main_ended")
	case "reconciliation_access_revoked", "reconciliation_access_expired_or_revoked", "lost_credentials":
		return a.failReleaseCut(ctx, e, "lost_credentials")
	case "reconciliation_access_unavailable":
		cause = "db_unavailable"
	}
	return a.deferReleaseCut(ctx, e, cause)
}
