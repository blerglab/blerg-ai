package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/releasecut"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const directCutClockMargin = 5 * time.Minute

func validateReleaseCutReceipt(receipt releasecut.Receipt, board, release string) error {
	if receipt.ID == "" || receipt.BoardID != board || receipt.ReleaseID != release || receipt.OwnerID == "" || !receipt.PersonAct || receipt.ReleasedAt.IsZero() || receipt.ReleasedAt.After(time.Now().Add(directCutClockMargin)) {
		return errors.New("invalid cut receipt provenance")
	}
	digest, err := releasecut.SnapshotDigest(receipt.Snapshot, board, release)
	if err != nil || digest != receipt.CardDigest {
		return errors.New("invalid cut receipt digest")
	}
	archived, moved := map[string]bool{}, map[string]bool{}
	for _, id := range receipt.Archived {
		if archived[id] {
			return errors.New("duplicate archive evidence")
		}
		archived[id] = true
	}
	for _, id := range receipt.Moved {
		if moved[id] || archived[id] {
			return errors.New("duplicate move evidence")
		}
		moved[id] = true
	}
	for _, entry := range receipt.Snapshot {
		var card struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(entry.Card, &card) != nil {
			return errors.New("invalid snapshot")
		}
		if entry.Terminal {
			if !archived[card.ID] {
				return errors.New("archive evidence missing")
			}
			delete(archived, card.ID)
		} else {
			if !moved[card.ID] || receipt.NextReleaseID == "" {
				return errors.New("move evidence missing")
			}
			delete(moved, card.ID)
		}
	}
	if len(archived) != 0 || len(moved) != 0 {
		return errors.New("foreign cut effects")
	}
	return nil
}
func (a *API) checkCutExecutionLink(ctx context.Context, e releaseCutExecution, r blackboard.Record) error {
	c := e.Subject.Cut
	found := false
	for _, m := range r.Milestones {
		if m.ID == e.MilestoneID && m.BoardID == c.BoardID && m.ReleaseID == c.ReleaseID {
			found = true
		}
	}
	if !found {
		return boundFailure("changed_release_link")
	}
	var valid bool
	err := a.dbPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blackboard_release_links l WHERE l.id=$1 AND l.blackboard_id=$2 AND l.milestone_id=$3 AND l.board_id=$4 AND l.release_id=$5 AND NOT EXISTS(SELECT 1 FROM blackboard_release_links newer WHERE newer.milestone_id=l.milestone_id AND newer.id>l.id AND (newer.board_id<>l.board_id OR newer.release_id<>l.release_id)))`, e.LinkID, e.BlackboardID, e.MilestoneID, c.BoardID, c.ReleaseID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return boundFailure("changed_release_link")
	}
	return nil
}

const cutReconciliationInterval = 30 * time.Second

func (a *API) reconcileBlackboardCuts(ctx context.Context, record blackboard.Record) {
	a.reconcileBlackboardCutsNow(ctx, record, "")
}
func (a *API) reconcileBlackboardCutsNow(ctx context.Context, record blackboard.Record, forceMilestone string) {
	// A blackboard read checks evidence, but a slow board must not multiply
	// its read deadline by the number of milestones or block the read forever.
	ctx, cancelAll := context.WithTimeout(ctx, 5*time.Second)
	defer cancelAll()
	a.clearLegacyCutLocks(ctx, record)
	for _, m := range record.Milestones {
		if ctx.Err() != nil {
			cutDiagnostic("reconciliation_deadline")
			return
		}
		if m.ShippedAt != nil && m.CutReceiptID != "" {
			continue
		}
		if m.BoardID == "" || m.ReleaseID == "" || m.LegacyEvidenceID != 0 || m.Stage == "Final" || m.Stage == "Finalizing" {
			continue
		}
		if !a.claimCutReconciliation(ctx, m.ID, m.ID == forceMilestone) {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		sid := blackboard.WorkspaceKey(record)
		proof, err := a.cutBoardProof(readCtx, record, m.BoardID, sid, "", false)
		if err != nil {
			cancel()
			a.noteCutObservation(ctx, m.ID, false, cutFailureCause(err, "reconciliation_access_unavailable"))
			continue
		}
		view, err := a.readCutRelease(readCtx, proof, m.BoardID, m.ReleaseID, sid)
		if err != nil {
			cancel()
			a.noteCutObservation(ctx, m.ID, false, cutFailureCause(err, "reconciliation_access_unavailable"))
			continue
		}
		if view.Release.Status != "released" {
			cancel()
			a.noteCutObservation(ctx, m.ID, false, "release_open")
			continue
		}
		if view.Receipt == nil {
			cancel()
			if a.markCutEvidenceIncomplete(ctx, record, m.ID) {
				a.noteCutObservation(ctx, m.ID, true, "released_without_receipt")
			} else {
				a.noteCutObservation(ctx, m.ID, true, "db_unavailable")
			}
			continue
		}
		if validateReleaseCutReceipt(*view.Receipt, m.BoardID, m.ReleaseID) != nil {
			cancel()
			a.noteCutObservation(ctx, m.ID, true, "cut_evidence_incomplete")
			a.markCutEvidenceIncomplete(ctx, record, m.ID)
			continue
		}
		err = a.finalizeMilestoneFromCut(readCtx, record, m, *view.Receipt, proof)
		cancel()
		if err != nil {
			cause := cutFinalizationCause(err)
			a.noteCutObservation(ctx, m.ID, true, cause)
			if cutEvidenceFailure(cause) {
				a.markCutEvidenceIncomplete(ctx, record, m.ID)
			}
			continue
		}
		a.noteCutObservation(ctx, m.ID, true, "cut_verified")
	}
}
func cutFailureCause(err error, fallback string) string {
	var failure *boundAskError
	if errors.As(err, &failure) {
		return failure.cause
	}
	return fallback
}
func (a *API) noteCutObservation(ctx context.Context, mid string, released bool, cause string) {
	if cause != "release_open" && cause != "cut_verified" {
		cutDiagnostic(cause)
	}
	_, err := a.dbPool.Exec(ctx, `INSERT INTO blackboard_cut_observations(milestone_id,known_released,cause) VALUES($1,$2,$3) ON CONFLICT(milestone_id) DO UPDATE SET observed_at=clock_timestamp(),known_released=blackboard_cut_observations.known_released OR EXCLUDED.known_released,cause=EXCLUDED.cause`, mid, released, cause)
	if err != nil {
		cutDiagnostic("reconciliation_store_unavailable")
	}
}
func (a *API) markCutEvidenceIncomplete(ctx context.Context, record blackboard.Record, mid string) bool {
	err := a.blackboardStore().UpdatePartition(ctx, record.ID, blackboard.Partition{Kind: blackboard.PartitionPlan, MilestoneID: mid}, func(r *blackboard.Record) error {
		for i := range r.Milestones {
			m := &r.Milestones[i]
			if m.ID == mid && m.ShippedAt == nil && !m.EvidenceIncomplete {
				m.EvidenceIncomplete = true
				m.Stage = "Evidence incomplete"
				m.Version++
			}
		}
		return nil
	})
	if err != nil {
		cutDiagnostic("reconciliation_store_unavailable")
	}
	return err == nil
}

// Recheck and lock the stored Main access inside Final. Permission setters lock
// Main; revocations lock the chosen grant. Neither can slip past this commit.
func (a *API) lockCutReadProof(ctx context.Context, tx pgx.Tx, r blackboard.Record, board string, expected mcpgw.Proof) error {
	sid := blackboard.WorkspaceKey(r)
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM sessions WHERE id=$1 FOR SHARE`, sid).Scan(&id); err != nil {
		return boundFailure("reconciliation_main_ended")
	}
	row, err := db.GetSession(ctx, tx, sid)
	if err != nil {
		return err
	}
	if row == nil || row.Kind != "agent" || row.ParentSessionID != nil || row.SpawningAccountID == nil || *row.SpawningAccountID != r.Owner.ID || (sessionInputsEnded(row) || row.Status == "ended" || row.Status == "stopped") || row.ArchivedAt != nil || row.DiscardRequestedAt != nil {
		return boundFailure("reconciliation_main_ended")
	}
	state, err := db.LatestBoardGrant(ctx, tx, sid, board)
	if err != nil {
		return err
	}
	var proof mcpgw.Proof
	var granted time.Time
	if state != nil {
		if !state.Live || db.BoardLevelRank(state.Grant.Level) < db.BoardLevelRank(db.BoardLevelRead) {
			return boundFailure("reconciliation_access_revoked")
		}
		if err = tx.QueryRow(ctx, `SELECT id::text FROM session_proposal_grants WHERE id=$1 AND revoked_at IS NULL FOR SHARE`, state.Grant.ID).Scan(&id); err != nil {
			return boundFailure("reconciliation_access_revoked")
		}
		proof = mcpgw.Proof{AccountID: state.Grant.AccountID, SessionID: state.Grant.ProofSessionID}
		granted = state.Grant.CreatedAt
	} else {
		var account, kind, value string
		if err = tx.QueryRow(ctx, `SELECT account_id,proof_kind,proof_value,created_at FROM session_mcp_grants WHERE session_id=$1 AND connection_id=$2 AND builtin='board' AND builtin_ref=$3 FOR SHARE`, sid, mcpgw.BoardConnectionID, board).Scan(&account, &kind, &value, &granted); err != nil {
			return boundFailure("reconciliation_access_revoked")
		}
		proof = mcpgw.ProofFromGrant(account, kind, value)
	}
	if !proof.Valid() || proof != expected {
		return boundFailure("reconciliation_access_changed")
	}
	if a.coreAuth == nil || a.coreAuth.StaleBeyondCeiling() {
		return boundFailure("reconciliation_access_unavailable")
	}
	if a.coreAuth.RevokedFor(proof.TokenID, "", proof.AccountID, proof.SessionID, granted.Unix()) {
		return boundFailure("reconciliation_access_expired_or_revoked")
	}
	return nil
}
func (a *API) finalizeMilestoneFromCut(ctx context.Context, record blackboard.Record, milestone blackboard.Milestone, receipt releasecut.Receipt, proof mcpgw.Proof) error {
	if err := validateReleaseCutReceipt(receipt, milestone.BoardID, milestone.ReleaseID); err != nil {
		return err
	}
	tx, err := a.dbPool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var resolvedFailure string
	err = (db.BlackboardStore{Pool: a.dbPool, Tx: tx}).UpdatePartition(ctx, record.ID, blackboard.Partition{Kind: blackboard.PartitionPlan, MilestoneID: milestone.ID, IncludeCut: true}, func(r *blackboard.Record) error {
		var ownerKind, ownerID string
		if err := tx.QueryRow(ctx, `SELECT owner_kind,owner_id FROM blackboards WHERE id=$1 FOR SHARE`, record.ID).Scan(&ownerKind, &ownerID); err != nil {
			return err
		}
		if ownerKind != record.Owner.Kind || ownerID != record.Owner.ID {
			return boundFailure("ownership_changed")
		}
		if r.Owner != record.Owner || blackboard.WorkspaceKey(*r) != blackboard.WorkspaceKey(record) {
			return boundFailure("ownership_changed")
		}
		var current *blackboard.Milestone
		for i := range r.Milestones {
			if r.Milestones[i].ID == milestone.ID {
				current = &r.Milestones[i]
				break
			}
		}
		if current == nil || current.BoardID != receipt.BoardID || current.ReleaseID != receipt.ReleaseID {
			return boundFailure("changed_release_link")
		}
		if current.ShippedAt != nil {
			if current.CutReceiptID == receipt.ID {
				return nil
			}
			return boundFailure("conflicting_final_evidence")
		}
		if err := a.lockCutReadProof(ctx, tx, *r, current.BoardID, proof); err != nil {
			return err
		}
		var linkID int64
		var linkedAt time.Time
		if receipt.AskID != "" {
			var principalRaw, subjectRaw []byte
			var sid, bid, mid, status string
			var deadline time.Time
			if err := tx.QueryRow(ctx, `SELECT e.link_id,e.principal,b.subject,e.workspace_id::text,e.blackboard_id,e.milestone_id,b.status,e.deadline FROM blackboard_cut_executions e JOIN bound_asks b ON b.message_id=e.ask_id WHERE e.ask_id=$1`, receipt.AskID).Scan(&linkID, &principalRaw, &subjectRaw, &sid, &bid, &mid, &status, &deadline); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return boundFailure("missing_cut_authorization")
				}
				return err
			}
			e := releaseCutExecution{AskID: receipt.AskID, LinkID: linkID, WorkspaceID: sid, BlackboardID: bid, MilestoneID: mid, Deadline: deadline}
			if json.Unmarshal(principalRaw, &e.Principal.Claims) != nil || json.Unmarshal(subjectRaw, &e.Subject) != nil || status != "committed" || sid != blackboard.WorkspaceKey(*r) || bid != r.ID || mid != current.ID || e.Principal.Sub != r.Owner.ID || verifyCutExecutionReceipt(e, &receipt) != nil {
				return boundFailure("cut_authorization_mismatch")
			}
		} else {
			// The oldest still-continuous exact tuple supplies direct-cut provenance.
			if err := tx.QueryRow(ctx, `SELECT l.id,l.linked_at FROM blackboard_release_links l WHERE l.blackboard_id=$1 AND l.milestone_id=$2 AND l.board_id=$3 AND l.release_id=$4 AND NOT EXISTS(SELECT 1 FROM blackboard_release_links newer WHERE newer.milestone_id=l.milestone_id AND newer.id>l.id AND (newer.board_id<>l.board_id OR newer.release_id<>l.release_id)) ORDER BY l.id LIMIT 1 FOR SHARE`, r.ID, current.ID, current.BoardID, current.ReleaseID).Scan(&linkID, &linkedAt); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return boundFailure("missing_release_link_evidence")
				}
				return err
			}
			if !linkedAt.Add(directCutClockMargin).Before(receipt.ReleasedAt) {
				return boundFailure("link_after_cut")
			}
		}
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blackboard_release_links l JOIN bound_asks origin ON origin.message_id=l.origin_ask_id WHERE l.id=$1 AND l.blackboard_id=$2 AND l.milestone_id=$3 AND l.board_id=$4 AND l.release_id=$5 AND origin.status='committed' AND origin.owner_id=$6 AND NOT EXISTS(SELECT 1 FROM blackboard_release_links newer WHERE newer.milestone_id=l.milestone_id AND newer.id>l.id AND (newer.board_id<>l.board_id OR newer.release_id<>l.release_id)))`, linkID, r.ID, current.ID, current.BoardID, current.ReleaseID, r.Owner.ID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return boundFailure("link_after_cut")
		}
		at := time.Now().UTC()
		cutRecord, err := db.ReadBlackboardCutRecord(ctx, tx, *r)
		if err != nil {
			return err
		}
		view, err := blackboard.FreezeCutSnapshot(cutRecord, current.ID, receipt, at)
		if err != nil {
			return err
		}
		frozen, err := json.Marshal(view)
		if err != nil {
			return err
		}
		rawReceipt, _ := json.Marshal(receipt)
		tag, err := tx.Exec(ctx, `INSERT INTO blackboard_cut_finalizations(milestone_id,blackboard_id,receipt_id,ask_id,frozen_at,receipt,frozen) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7) ON CONFLICT DO NOTHING`, current.ID, r.ID, receipt.ID, receipt.AskID, at, rawReceipt, frozen)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return boundFailure("cut_receipt_already_consumed")
		}
		if receipt.AskID != "" {
			if err := tx.QueryRow(ctx, `UPDATE blackboard_cut_executions SET status='executed',cause='',lease_until=NULL,receipt=$2 WHERE ask_id=$1 RETURNING coalesce(failure_message_id::text,'')`, receipt.AskID, rawReceipt).Scan(&resolvedFailure); err != nil {
				return err
			}
			if resolvedFailure != "" {
				if _, err := tx.Exec(ctx, `UPDATE messages SET status='answered',answer='',answered_at=clock_timestamp() WHERE id=$1 AND status='open'`, resolvedFailure); err != nil {
					return err
				}
			}
		}
		current.Frozen = frozen
		current.FrozenAt = &at
		current.ShippedAt = &receipt.ReleasedAt
		current.CutReceiptID = receipt.ID
		current.EvidenceIncomplete = false
		current.Stage = "Final"
		current.Version++
		payload, _ := json.Marshal(map[string]any{"cut_receipt_id": receipt.ID, "ask_id": receipt.AskID, "released_at": receipt.ReleasedAt, "runner_frozen_at": at, "revision": current.Revision})
		r.Events = append(r.Events, blackboard.Event{ID: blackboard.NewID(), MilestoneID: current.ID, AuthenticatedActor: blackboard.Actor{Kind: "system", ID: "runner"}, RecordedAt: at, Source: "derived", Kind: "final", Payload: payload})
		return nil
	})
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	a.broadcastBlackboardDraftChanged(&http.Request{}, blackboard.WorkspaceKey(record)) //nolint:contextcheck // committed cut refresh has independently bounded visibility
	if resolvedFailure != "" {
		a.waiters.signal(resolvedFailure)
		row, e := db.GetMessage(ctx, a.dbPool, resolvedFailure)
		if e != nil {
			cutDiagnostic("recovered_question_notice_unavailable")
		} else if row != nil {
			a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)}) //nolint:contextcheck // committed recovery has independently bounded delivery
		}
	}
	return nil
}

// Atomic across requests and runner replicas. Successful execution may bypass
// the interval once because it just acquired a new, immutable receipt.
func (a *API) claimCutReconciliation(ctx context.Context, mid string, force bool) bool {
	var id string
	err := a.dbPool.QueryRow(ctx, `INSERT INTO blackboard_cut_observations(milestone_id,cause,next_check_at) VALUES($1,'checking',clock_timestamp()+$2::interval) ON CONFLICT(milestone_id) DO UPDATE SET next_check_at=EXCLUDED.next_check_at WHERE $3 OR (blackboard_cut_observations.next_check_at<=clock_timestamp() AND blackboard_cut_observations.cause<>'released_without_receipt') RETURNING milestone_id`, mid, cutReconciliationInterval.String(), force).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		cutDiagnostic("reconciliation_store_unavailable")
	}
	return err == nil
}
func cutEvidenceFailure(cause string) bool {
	switch cause {
	case "conflicting_final_evidence", "missing_cut_authorization", "cut_authorization_mismatch", "missing_release_link_evidence", "link_after_cut", "cut_receipt_already_consumed":
		return true
	}
	return false
}

// Typed provenance and access refusals can wrap ErrConflict as an HTTP class.
// Preserve their cause; only untyped storage conflicts are eligible for retry.
func cutFinalizationCause(err error) string {
	if cause := cutFailureCause(err, ""); cause != "" {
		return cause
	}
	var pgErr *pgconn.PgError
	if errors.Is(err, blackboard.ErrConflict) || errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01" || pgErr.Code == "55P03") {
		return "finalization_conflict"
	}
	return "db_unavailable"
}
