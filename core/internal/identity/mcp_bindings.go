package identity

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"log/slog"
)

// Suspension is optional bookkeeping after irrevocable identity revocation. A
// nested transaction is a savepoint: SQL failure cannot poison the parent logout.
// Admission independently reads the revoked chain even if this hook cannot run.
func suspendMCPBindings(ctx context.Context, tx pgx.Tx, accountID, sid string) {
	logCause := func(stage string, err error) {
		cause := "database"
		var pg *pgconn.PgError
		switch {
		case errors.Is(err, context.Canceled):
			cause = "canceled"
		case errors.Is(err, context.DeadlineExceeded):
			cause = "deadline"
		case errors.As(err, &pg):
			switch pg.Code {
			case "55P03":
				cause = "database_lock_timeout"
			case "57014":
				cause = "database_statement_timeout"
			case "42P01":
				cause = "database_schema"
			}
		}
		failurelog.Default.Event("identity:mcp_suspend:"+stage+":"+cause, slog.LevelWarn, "component", "core", "operation", "identity_mcp_suspend_"+stage, "cause", cause)
	}

	child, err := tx.Begin(ctx)
	if err != nil {
		logCause("savepoint", err)
		return
	}
	defer func() {
		if rollbackErr := child.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			logCause("rollback", rollbackErr)
		}
	}()
	// Do not let a stuck MCP row hold logout's identity transaction open.
	var oldLock, oldStatement string
	if err = child.QueryRow(ctx, `SELECT current_setting('lock_timeout'),current_setting('statement_timeout')`).Scan(&oldLock, &oldStatement); err != nil {
		logCause("timeout_read", err)
		return
	}
	if _, err = child.Exec(ctx, `SELECT set_config('lock_timeout','100ms',true),set_config('statement_timeout','250ms',true)`); err != nil {
		logCause("timeout_set", err)
		return
	}
	_, err = child.Exec(ctx, `UPDATE mcp_connections SET credential_generation=credential_generation+1,lifecycle='suspended',
 suspended_at=statement_timestamp(),cleanup_after=statement_timestamp()+interval '168 hours',binding_lease_until=NULL,
 status=CASE WHEN session_refresh_lease_until IS NOT NULL THEN 'needs_auth' ELSE status END,
 session_refresh_lease_until=NULL,updated_at=statement_timestamp()
 WHERE account_id=$1 AND runner_session_id IS NOT NULL AND lifecycle='active' AND ($2::text='' OR bound_sid=nullif($2,'')::uuid)`, accountID, sid)
	if err != nil {
		logCause("binding_update", err)
		return
	}
	if _, err = child.Exec(ctx, `SELECT set_config('lock_timeout',$1,true),set_config('statement_timeout',$2,true)`, oldLock, oldStatement); err != nil {
		logCause("timeout_restore", err)
		return
	}
	if err = child.Commit(ctx); err != nil {
		logCause("savepoint_commit", err)
	}
}
