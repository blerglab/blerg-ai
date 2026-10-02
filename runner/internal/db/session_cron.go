package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// terminalSessionStatusesSQL are the statuses after which a session does no more work. It matches
// the runner's lifecycle mapping (server.runnerLifecycle): ended and stopped are "ended".
const terminalSessionStatusesSQL = `('ended', 'stopped', 'error')`

// SetSessionCronID records which cron started a session (migration 023). A session that does
// not exist is pgx.ErrNoRows.
func SetSessionCronID(ctx context.Context, conn *pgxpool.Pool, sessionID, cronID string) error {
	tag, err := conn.Exec(ctx, `UPDATE sessions SET cron_id = $2::uuid WHERE id = $1`, sessionID, cronID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// CountActiveCronSessions counts the account's cron-started sessions that have not ended: the
// number the scheduler holds against the per-account concurrent limit.
func CountActiveCronSessions(ctx context.Context, conn *pgxpool.Pool, account string) (int, error) {
	var n int
	err := conn.QueryRow(ctx, `
		SELECT count(*) FROM sessions
		 WHERE cron_id IS NOT NULL AND spawning_account_id = $1
		   AND status NOT IN `+terminalSessionStatusesSQL, account).Scan(&n)
	return n, err
}

// ActiveCronSession is a running cron-started session and what the watchdog needs to judge it.
type ActiveCronSession struct {
	SessionID string
	CronID    string
	Account   string
	StartedAt time.Time
	// MaxRuntimeSeconds is the cron's limit; nil when the cron no longer exists (the session is
	// then an orphan and is stopped regardless of age).
	MaxRuntimeSeconds *int
}

// ListActiveCronSessions lists the cron-started sessions that have not ended: those of one cron,
// or every cron's when cronID is empty.
func ListActiveCronSessions(ctx context.Context, conn *pgxpool.Pool, cronID string) ([]ActiveCronSession, error) {
	rows, err := conn.Query(ctx, `
		SELECT s.id::text, s.cron_id::text, COALESCE(s.spawning_account_id, ''), s.started_at, c.max_runtime_seconds
		  FROM sessions s
		  LEFT JOIN crons c ON c.id = s.cron_id
		 WHERE s.cron_id IS NOT NULL AND s.status NOT IN `+terminalSessionStatusesSQL+`
		   AND ($1 = '' OR s.cron_id::text = $1)
		 ORDER BY s.started_at`, cronID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveCronSession
	for rows.Next() {
		var a ActiveCronSession
		if err := rows.Scan(&a.SessionID, &a.CronID, &a.Account, &a.StartedAt, &a.MaxRuntimeSeconds); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// orphanGrantMinAge is the youngest a grant may be for the orphan sweep to delete it.
const orphanGrantMinAge = 10 * time.Minute

// DeleteOrphanMCPGrants removes the grants (and so the gateway tokens) of sessions that no longer
// exist or have ended. Grants are normally deleted when their session ends; this is the hourly
// backstop for a runner that died in between.
//
// A grant younger than orphanGrantMinAge is never swept: a resume issues its grants before the
// session's row is back to a live status, and an hourly sweep landing in that gap must not delete them.
func DeleteOrphanMCPGrants(ctx context.Context, conn *pgxpool.Pool) (int64, error) {
	tag, err := conn.Exec(ctx, `
		DELETE FROM session_mcp_grants g
		 WHERE g.created_at < now() - make_interval(secs => $1)
		   AND NOT EXISTS (
		         SELECT 1 FROM sessions s
		          WHERE s.id = g.session_id AND s.status NOT IN `+terminalSessionStatusesSQL+`)`, orphanGrantMinAge.Seconds())
	if err != nil {
		return 0, fmt.Errorf("DeleteOrphanMCPGrants: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListCronsWithMCP lists every cron that names at least one MCP connection, for the sweeper.
func ListCronsWithMCP(ctx context.Context, conn *pgxpool.Pool) ([]Cron, error) {
	rows, err := conn.Query(ctx, `SELECT `+cronColumns+` FROM crons WHERE mcp <> '[]'::jsonb ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cron
	for rows.Next() {
		c, err := scanCron(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}
