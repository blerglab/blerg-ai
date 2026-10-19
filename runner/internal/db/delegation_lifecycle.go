package db

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrDelegationEnded means a native write lost permission to revive a child.
var ErrDelegationEnded = errors.New("the session's parent ended")

// The validated parent's epoch must still match when the child is inserted.
// Parent SHARE makes the insert precede the next end, or refuses an obsolete
// start even when the person has already resumed that same parent ID.
func insertDelegatedSessionAs(ctx context.Context, pool *pgxpool.Pool, id, daemonID, status, projectPath, repo, title, model string, origin SessionOrigin) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var valid bool
	err = tx.QueryRow(ctx, `SELECT delegation_ended_at IS NOT DISTINCT FROM $2::timestamptz
		AND NOT COALESCE((`+terminallyEndedSQL("sessions")+`),false)
		FROM sessions WHERE id=$1 FOR SHARE`, origin.ParentSessionID, origin.ParentEpoch).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !valid) {
		return ErrDelegationEnded
	}
	if err != nil {
		return err
	}
	if err := insertSessionAs(ctx, tx, id, daemonID, status, projectPath, repo, title, model, origin); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DelegationEndBoundary survives ordinary parent revival. Directly recorded
// legacy terminal states still supply their end timestamp until migrated.
func DelegationEndBoundary(ctx context.Context, pool *pgxpool.Pool, id string) (*time.Time, error) {
	var cutoff *time.Time
	err := pool.QueryRow(ctx, `SELECT COALESCE(delegation_ended_at,
		CASE WHEN `+terminallyEndedSQL("sessions")+` THEN ended_at END) FROM sessions WHERE id=$1`, id).Scan(&cutoff)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return cutoff, err
}

// ExpireParentDelegationsThrough cannot expire requests from a resumed turn
// when an earlier pause/end worker was delayed by another database operation.
func ExpireParentDelegationsThrough(ctx context.Context, pool *pgxpool.Pool, id string, cutoff time.Time) ([]DelegationRequest, error) {
	rows, err := pool.Query(ctx, `UPDATE delegation_requests SET status='expired'
		WHERE parent_session_id=$1 AND status='pending' AND created_at <= $2
		RETURNING `+delegationRequestCols, id, cutoff)
	if err != nil {
		return nil, err
	}
	return scanDelegationRequests(rows)
}

func RevokeParentDelegationGrantsThrough(ctx context.Context, pool *pgxpool.Pool, id string, cutoff time.Time) error {
	_, err := pool.Exec(ctx, `UPDATE delegation_grants SET revoked_at=now()
		WHERE parent_session_id=$1 AND revoked_at IS NULL AND created_at <= $2`, id, cutoff)
	return err
}

func ChildrenToStopThrough(ctx context.Context, pool *pgxpool.Pool, id string, cutoff time.Time) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT c.id::text FROM sessions c
		JOIN sessions p ON p.id=c.parent_session_id
		WHERE c.parent_session_id=$1 AND NOT c.keep_running
		AND (c.started_at <= $2 OR COALESCE((`+terminallyEndedSQL("p")+`),false))
		AND NOT COALESCE((`+terminallyEndedSQL("c")+`),false) ORDER BY c.started_at,c.id`, id, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var child string
		if err := rows.Scan(&child); err != nil {
			return nil, err
		}
		ids = append(ids, child)
	}
	return ids, rows.Err()
}

// WithDelegationRecoveryGuard authorizes a link's recovery-ready enqueue under
// stable parent locks. A parent ending during hello's token/ask lookups cannot
// slip between policy and ready. All writes use the same transaction, including
// newly orphaned children's terminal state; callbacks only enqueue frames.
func WithDelegationRecoveryGuard(ctx context.Context, pool *pgxpool.Pool, ids []string, enqueue func([]string) error, required ...[]string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, err := tx.Query(ctx, `SELECT p.id FROM sessions p WHERE p.id IN
		(SELECT c.parent_session_id FROM sessions c WHERE c.id=ANY($1::uuid[]) AND NOT c.keep_running)
		ORDER BY p.id FOR SHARE`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		// Consume before issuing the policy query on this same connection.
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	blockedByParent := delegationResumeBlockedSQL("c", "p")
	rows, err = tx.Query(ctx, `SELECT c.id::text,
		(`+blockedByParent+`) AND NOT COALESCE((`+terminallyEndedSQL("c")+`),false)
		FROM sessions c LEFT JOIN sessions p ON p.id=c.parent_session_id
		WHERE c.id=ANY($1::uuid[]) AND ((`+blockedByParent+`) OR
		(c.status IN ('ended','stopped','error') AND (c.end_reason IN
		('stopped_by_user','stopped_by_agent','auto_stopped','parent_ended')
		OR (c.end_reason='daemon_lost' AND c.status='stopped')))) ORDER BY c.id`, ids)
	if err != nil {
		return err
	}
	var stopped, finalize []string
	for rows.Next() {
		var id string
		var needsEnd bool
		if err := rows.Scan(&id, &needsEnd); err != nil {
			rows.Close()
			return err
		}
		stopped = append(stopped, id)
		if needsEnd {
			finalize = append(finalize, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, marked := range required {
		missing, err := tx.Query(ctx, `SELECT required.id::text FROM unnest($1::uuid[]) AS required(id)
			WHERE NOT EXISTS(SELECT 1 FROM sessions s WHERE s.id=required.id) ORDER BY required.id`, marked)
		if err != nil {
			return err
		}
		for missing.Next() {
			var id string
			if err := missing.Scan(&id); err != nil {
				missing.Close()
				return err
			}
			stopped = append(stopped, id)
		}
		err = missing.Err()
		missing.Close()
		if err != nil {
			return err
		}
	}
	slices.Sort(finalize)
	now := time.Now()
	for _, id := range finalize {
		if err := endSessionStatus(ctx, tx, id, "ended", &now, SessionEnd{Reason: EndReasonParentEnded}); err != nil {
			return err
		}
	}
	if err := enqueue(stopped); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type sessionWriteExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// WithDelegationInputGuardForPool keeps both callbacks' main pool and their
// action-lock connection independent of the guard transaction. Cancellation
// writes must commit before a callback rotates the child's runtime authority.
func WithDelegationInputGuardForPool(ctx context.Context, pool *pgxpool.Pool, id string, enqueue func() error) error {
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(cleanup)
	}()
	return WithDelegationInputGuard(ctx, conn, id, enqueue)
}

// WithDelegationInputGuard linearizes enqueue with the parent's true end.
// source must be independent of connections a callback writes through. Only
// the parent is locked: callbacks may write child data or rotate its tokens.
func WithDelegationInputGuard(ctx context.Context, source interface {
	Begin(context.Context) (pgx.Tx, error)
}, id string, enqueue func() error) error {
	tx, err := source.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var parent *string
	var keep bool
	err = tx.QueryRow(ctx, `SELECT parent_session_id::text,keep_running FROM sessions WHERE id=$1`, id).Scan(&parent, &keep)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if parent != nil && !keep {
		if _, err := tx.Exec(ctx, `SELECT id FROM sessions WHERE id=$1 FOR SHARE`, *parent); err != nil {
			return err
		}
	}
	blocked, err := DelegationResumeBlocked(ctx, tx, id)
	if err != nil {
		return err
	}
	if blocked {
		return ErrDelegationEnded
	}
	if err := enqueue(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// withDelegationResumeGuard locks parent before child. A real parent end either
// commits before the policy read and refuses this write, or commits afterward
// and its cleanup sees the now-live child. Querying before a blocked UPDATE is
// insufficient: the parent can end during the row-lock wait.
func withDelegationResumeGuard(ctx context.Context, pool *pgxpool.Pool, id string, write func(sessionWriteExecutor) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var parent *string
	var keep bool
	err = tx.QueryRow(ctx, `SELECT parent_session_id::text,keep_running FROM sessions WHERE id=$1`, id).Scan(&parent, &keep)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if parent != nil && !keep {
		if _, err := tx.Exec(ctx, `SELECT id FROM sessions WHERE id=$1 FOR SHARE`, *parent); err != nil {
			return err
		}
	}
	var currentParent *string
	var currentKeep bool
	err = tx.QueryRow(ctx, `SELECT parent_session_id::text,keep_running FROM sessions WHERE id=$1 FOR UPDATE`, id).Scan(&currentParent, &currentKeep)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// The relation is immutable during ordinary recovery. A concurrent delete
	// clears its FK; the marker still refuses non-kept children below.
	if !currentKeep && currentParent != nil && (parent == nil || *currentParent != *parent) {
		return ErrDelegationEnded
	}
	blocked, err := DelegationResumeBlocked(ctx, tx, id)
	if err != nil {
		return err
	}
	if blocked {
		return ErrDelegationEnded
	}
	if err := write(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReviveSessionAs is native session_started admission, with the parent's
// durable lifecycle fenced through the actual upsert.
func ReviveSessionAs(ctx context.Context, pool *pgxpool.Pool, id, daemonID, status, projectPath, repo, title, model string, origin SessionOrigin) error {
	return withDelegationResumeGuard(ctx, pool, id, func(conn sessionWriteExecutor) error {
		return insertSessionAs(ctx, conn, id, daemonID, status, projectPath, repo, title, model, origin)
	})
}

// UpdateSessionStatusGuarded fences a daemon/reconcile live-status write too.
func UpdateSessionStatusGuarded(ctx context.Context, pool *pgxpool.Pool, id, status string, endedAt *time.Time) error {
	return withDelegationResumeGuard(ctx, pool, id, func(conn sessionWriteExecutor) error {
		return updateSessionStatus(ctx, conn, id, status, endedAt)
	})
}

// terminallyEndedSQL is TerminallyEnded's SQL counterpart. alias is an internal
// table alias, never caller input. Keep real ends distinct from observations of
// a disconnected daemon, even when those observations carry ended_at.
func terminallyEndedSQL(alias string) string {
	return alias + `.ended_at IS NOT NULL AND (` + alias + `.end_reason IN
		('stopped_by_user','stopped_by_agent','auto_stopped','process_exited',
		 'process_failed','start_failed','parent_ended') OR (` + alias + `.end_reason='daemon_lost' AND ` + alias + `.status='stopped'))`
}

func delegationResumeBlockedSQL(child, parent string) string {
	return `(` + child + `.archived_at IS NOT NULL) OR COALESCE(` + child + `.end_reason='parent_ended',false) OR (NOT ` + child + `.keep_running AND (
		COALESCE((` + terminallyEndedSQL(parent) + `),false) OR
		COALESCE(` + child + `.started_at <= ` + parent + `.delegation_ended_at,false) OR
		(` + parent + `.id IS NULL AND (COALESCE(` + child + `.delegation_via,'')<>'' OR COALESCE(` + child + `.started_by_kind,'')='session'))))`
}

// DelegationResumeBlocked applies the same child policy to native adoption,
// manual input, automatic continuation and retained-history recovery. A deleted
// parent cannot erase durable child lineage. Missing sessions are left to the
// caller's ordinary existence check; database errors must fail closed there.
func DelegationResumeBlocked(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) (bool, error) {
	var blocked bool
	err := pool.QueryRow(ctx, `SELECT `+delegationResumeBlockedSQL("c", "p")+`
		FROM sessions c LEFT JOIN sessions p ON p.id=c.parent_session_id WHERE c.id=$1`, id).Scan(&blocked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return blocked, err
}

// PendingDelegationParentsForDaemon includes parents absent from a hello's
// active inventory: their old CLI may have died mid-turn before reporting pause.
func PendingDelegationParentsForDaemon(ctx context.Context, pool *pgxpool.Pool, daemonID string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT DISTINCT r.parent_session_id::text
		FROM delegation_requests r JOIN sessions s ON s.id=r.parent_session_id
		WHERE s.daemon_id=$1 AND r.status='pending'`, daemonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
