package db

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// BeginSessionDiscard commits the irreversible admission fence before stop delivery.
// Caller holds the account's delegation lock; parent row ordering also fences inputs
// and native recovery. Children inherit their own durable fence, including kept children.
func BeginSessionDiscard(ctx context.Context, pool *pgxpool.Pool, id, account string) (time.Time, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	row, err := LockDelegationParent(ctx, tx, id)
	if err != nil {
		return time.Time{}, err
	}
	if row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID != account {
		return time.Time{}, ErrDelegationNotFound
	}
	var foreign bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE parent_session_id=$1 AND spawning_account_id IS DISTINCT FROM $2)`, id, account).Scan(&foreign)
	if err != nil {
		return time.Time{}, err
	}
	if foreign {
		return time.Time{}, ErrDelegationNotFound
	}
	var at time.Time
	err = tx.QueryRow(ctx, `UPDATE sessions SET discard_requested_at=COALESCE(discard_requested_at,clock_timestamp()) WHERE id=$1 RETURNING discard_requested_at`, id).Scan(&at)
	if err != nil {
		return at, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET discard_requested_at=COALESCE(discard_requested_at,$3) WHERE parent_session_id=$1 AND spawning_account_id=$2`, id, account, at); err != nil {
		return at, err
	}
	// Discarding a parent ends its own and its children's proposal permissions with the fence.
	if _, err = tx.Exec(ctx, `UPDATE session_proposal_grants SET revoked_at=now(),revoked_reason='discarded' WHERE revoked_at IS NULL AND (session_id=$1 OR session_id IN (SELECT id FROM sessions WHERE parent_session_id=$1))`, id); err != nil {
		return at, err
	}
	if err = tx.Commit(ctx); err != nil {
		return at, err
	}
	return at, nil
}

func SessionDiscardMembers(ctx context.Context, pool *pgxpool.Pool, id, account string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT id::text FROM sessions WHERE spawning_account_id=$2 AND (id=$1 OR parent_session_id=$1) ORDER BY (id=$1),started_at,id`, id, account)
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

// ArchiveDiscardedSession requires an actual recorded end; intent alone never suffices.
func ArchiveDiscardedSession(ctx context.Context, pool *pgxpool.Pool, id, account string) (time.Time, error) {
	var at time.Time
	err := pool.QueryRow(ctx, `UPDATE sessions SET archived_at=COALESCE(archived_at,clock_timestamp()) WHERE id=$1 AND spawning_account_id=$2 AND discard_requested_at IS NOT NULL AND ended_at IS NOT NULL AND status IN ('ended','stopped','error','disconnected') AND NOT EXISTS(SELECT 1 FROM sessions child WHERE child.parent_session_id=sessions.id AND child.archived_at IS NULL) RETURNING archived_at`, id, account).Scan(&at)
	return at, err
}

func SessionDiscardBlocked(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	var blocked bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions s LEFT JOIN sessions p ON p.id=s.parent_session_id WHERE s.id=$1 AND (s.discard_requested_at IS NOT NULL OR p.discard_requested_at IS NOT NULL))`, id).Scan(&blocked)
	return blocked, err
}
