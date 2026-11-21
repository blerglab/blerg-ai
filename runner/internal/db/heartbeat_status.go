package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReconcileSessionStatus atomically compares the heartbeat's observation,
// updates the lifecycle, revokes credentials on a real stop, and records it.
func ReconcileSessionStatus(ctx context.Context, pool *pgxpool.Pool, pin DaemonAuthority, sid, expectedStatus string, expectedEnd *string, desired string, endedAt *time.Time, end SessionEnd, revoke bool) (bool, error) {
	return reconcilePinnedSessionStatus(ctx, pool, pin, sid, expectedStatus, expectedEnd, desired, endedAt, end, revoke, false, nil)
}

// PinnedFrameEffects are stored atomically with a status frame.
type PinnedFrameEffects struct {
	ErrorReason *string
	SetUnread   bool
}

// ApplyPinnedSessionFrame waits briefly for the row: one-shot status frames
// cannot be discarded merely because another transaction is using it.
func ApplyPinnedSessionFrame(ctx context.Context, pool *pgxpool.Pool, pin DaemonAuthority, sid, expectedStatus string, expectedEnd *string, desired string, endedAt *time.Time, end SessionEnd, revoke bool, effects *PinnedFrameEffects) (bool, error) {
	return reconcilePinnedSessionStatus(ctx, pool, pin, sid, expectedStatus, expectedEnd, desired, endedAt, end, revoke, true, effects)
}

func reconcilePinnedSessionStatus(ctx context.Context, pool *pgxpool.Pool, pin DaemonAuthority, sid, expectedStatus string, expectedEnd *string, desired string, endedAt *time.Time, end SessionEnd, revoke, wait bool, effects *PinnedFrameEffects) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackBackgroundTaskTx(ctx, tx)
	if err := lockDaemonAuthorityTx(ctx, tx, pin); err != nil {
		return false, err
	}
	// Parent must be locked before child, as in withDelegationResumeGuard.
	var parent *string
	err = tx.QueryRow(ctx, `SELECT parent_session_id::text FROM sessions WHERE id=$1`, sid).Scan(&parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if parent != nil {
		if _, err := tx.Exec(ctx, `SELECT id FROM sessions WHERE id=$1 FOR SHARE`, *parent); err != nil {
			return false, err
		}
	}
	var currentParent, currentEnd, account *string
	var discard *time.Time
	var currentStatus, owner string
	lock := ` FOR UPDATE`
	if !wait {
		lock += ` SKIP LOCKED`
	}
	err = tx.QueryRow(ctx, `SELECT parent_session_id::text,status,end_reason,daemon_id::text,spawning_account_id,discard_requested_at FROM sessions WHERE id=$1`+lock, sid).Scan(&currentParent, &currentStatus, &currentEnd, &owner, &account, &discard)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	equal := func(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
	if !equal(parent, currentParent) || owner != pin.DaemonID || (!pin.Legacy && (account == nil || *account != pin.OwnerID)) || currentStatus != expectedStatus || !equal(currentEnd, expectedEnd) {
		return false, nil
	}
	if discard != nil {
		return false, nil
	}
	if end.Reason == EndReasonParentEnded {
		blocked, err := DelegationResumeBlocked(ctx, tx, sid)
		if err != nil {
			return false, err
		}
		if !blocked {
			return false, nil
		}
	}
	if desired != "stopped" && stoppedForGoodDB(currentStatus, currentEnd) {
		return false, nil
	}
	if end.Reason == "" {
		blocked, err := DelegationResumeBlocked(ctx, tx, sid)
		if err != nil {
			return false, err
		}
		if blocked {
			return false, ErrDelegationEnded
		}
		if err := updateSessionStatus(ctx, tx, sid, desired, endedAt); err != nil {
			return false, err
		}
	} else if err := endSessionStatus(ctx, tx, sid, desired, endedAt, end); err != nil {
		return false, err
	}
	if desired != "error" {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET error_reason=NULL,daemon_lost_at=NULL WHERE id=$1`, sid); err != nil {
			return false, err
		}
	}
	if effects != nil && (effects.ErrorReason != nil || effects.SetUnread) {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET error_reason=COALESCE($2,error_reason),unread=CASE WHEN $3 THEN true ELSE unread END WHERE id=$1`, sid, effects.ErrorReason, effects.SetUnread); err != nil {
			return false, err
		}
	}
	if revoke {
		if err := RevokeBoardTokensForSession(ctx, tx, sid); err != nil {
			return false, err
		}
		if _, err := DeleteMCPGrantsForSession(ctx, tx, sid); err != nil {
			return false, err
		}
		if _, err := RevokeSessionProposalGrants(ctx, tx, sid, "session_ended", false); err != nil {
			return false, err
		}
	}
	data, _ := json.Marshal(map[string]string{"status": desired})
	if _, err := tx.Exec(ctx, `INSERT INTO session_events (session_id,type,data,seq) VALUES ($1,'state_change',$2,0)`, sid, string(data)); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func stoppedForGoodDB(status string, reason *string) bool {
	if reason == nil || (status != "stopped" && status != "ended" && status != "error") {
		return false
	}
	switch *reason {
	case EndReasonStoppedByUser, EndReasonStoppedByAgent, EndReasonAutoStopped, EndReasonParentEnded:
		return true
	case EndReasonDaemonLost:
		return status == "stopped"
	case EndReasonProcessFailed:
		return status == "error"
	}
	return false
}

// ReconcilePinnedPendingEnd clears a stale pending stop, or revokes credentials
// for a discarded session, only while the pinned ownership still holds.
func ReconcilePinnedPendingEnd(ctx context.Context, pool *pgxpool.Pool, pin DaemonAuthority, sid, expectedStatus string, expectedEnd *string, discard bool) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackBackgroundTaskTx(ctx, tx)
	if err := lockDaemonAuthorityTx(ctx, tx, pin); err != nil {
		return false, err
	}
	var owner, status string
	var account, end *string
	var discarded *time.Time
	err = tx.QueryRow(ctx, `SELECT daemon_id::text,spawning_account_id,status,end_reason,discard_requested_at FROM sessions WHERE id=$1 FOR UPDATE SKIP LOCKED`, sid).Scan(&owner, &account, &status, &end, &discarded)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	equal := func(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
	if owner != pin.DaemonID || (!pin.Legacy && (account == nil || *account != pin.OwnerID)) || status != expectedStatus || !equal(end, expectedEnd) || (discard != (discarded != nil)) {
		return false, nil
	}
	var changed bool
	if discard {
		if err := RevokeBoardTokensForSession(ctx, tx, sid); err != nil {
			return false, err
		}
		if _, err := DeleteMCPGrantsForSession(ctx, tx, sid); err != nil {
			return false, err
		}
		if _, err := RevokeSessionProposalGrants(ctx, tx, sid, "session_ended", false); err != nil {
			return false, err
		}
		changed = true
	} else {
		tag, err := tx.Exec(ctx, `UPDATE sessions SET end_reason=NULL,ended_by_kind=NULL,ended_by_account=NULL,end_recorded_at=NULL WHERE id=$1 AND end_reason IS NOT NULL AND status NOT IN `+terminalStatusesSQL+` AND `+pendingEndStale, sid)
		if err != nil {
			return false, err
		}
		changed = tag.RowsAffected() > 0
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return changed, nil
}

// ConfirmPinnedStoppedSession only admits a kill when the listed stop still
// belongs to the same generation and the session row has not changed.
func ConfirmPinnedStoppedSession(ctx context.Context, pool *pgxpool.Pool, pin DaemonAuthority, sid, expectedStatus string, expectedEnd *string) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackBackgroundTaskTx(ctx, tx)
	if err := lockDaemonAuthorityTx(ctx, tx, pin); err != nil {
		return false, err
	}
	var owner, status string
	var account, end *string
	err = tx.QueryRow(ctx, `SELECT daemon_id::text,spawning_account_id,status,end_reason FROM sessions WHERE id=$1 FOR UPDATE SKIP LOCKED`, sid).Scan(&owner, &account, &status, &end)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	equal := (end == nil && expectedEnd == nil) || (end != nil && expectedEnd != nil && *end == *expectedEnd)
	if owner != pin.DaemonID || (!pin.Legacy && (account == nil || *account != pin.OwnerID)) || status != expectedStatus || !equal || !stoppedForGoodDB(status, end) {
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// ValidatePinnedDaemon commits a read-only authority check for effects that
// have no session row yet, such as a pre-start failure notification.
func ValidatePinnedDaemon(ctx context.Context, pool *pgxpool.Pool, pin DaemonAuthority) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackBackgroundTaskTx(ctx, tx)
	if err := lockDaemonAuthorityTx(ctx, tx, pin); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
