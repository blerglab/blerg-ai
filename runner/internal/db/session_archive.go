package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ArchiveChildSession never archives a running or reassigned session.
func ArchiveChildSession(ctx context.Context, pool *pgxpool.Pool, id, parent, account string) (time.Time, error) {
	var at time.Time
	err := pool.QueryRow(ctx, `UPDATE sessions SET archived_at=COALESCE(archived_at,clock_timestamp())
 WHERE id=$1 AND parent_session_id=$2 AND spawning_account_id=$3
 AND ended_at IS NOT NULL AND status IN ('ended','stopped','error','disconnected')
 RETURNING archived_at`, id, parent, account).Scan(&at)
	return at, err
}
