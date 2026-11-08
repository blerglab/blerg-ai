package db

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"time"
)

type MCPBindingWork struct {
	ControlGeneration        int64
	ID, ConnectionID, Action string
	Scope                    MCPSessionScope
	Revision                 int64
}

// ListMCPBindingWork reconciles only durable session state. Missing authority
// suspends; only an explicit terminal record closes. No inference from outages.
func ListMCPBindingWork(ctx context.Context, pool *pgxpool.Pool) ([]MCPBindingWork, error) {
	if err := promoteTerminalMCPBindings(ctx, pool); err != nil {
		return nil, err
	}
	var err error
	_, err = pool.Exec(ctx, `UPDATE mcp_session_bindings b SET desired_action='suspend',delivered=false,revision=revision+1,updated_at=clock_timestamp()
 WHERE desired_action='active' AND NOT EXISTS(SELECT 1 FROM sessions s JOIN session_mcp_grants g ON g.session_id=s.id
 WHERE s.id=b.session_id AND g.connection_id=b.connection_id AND g.session_scope=b.scope
 AND s.spawning_account_id=b.account_id AND s.mcp_launch_generation=(b.scope->>'launch_generation')::bigint
 AND s.status IN ('running','idle','waiting') AND s.ended_at IS NULL AND s.discard_requested_at IS NULL
 AND s.delegation_ended_at IS NULL AND coalesce(s.paused_reason,'')='')`)
	if err != nil {
		return nil, err
	}
	if _, err = pool.Exec(ctx, `DELETE FROM mcp_session_bindings b WHERE desired_action IN ('done','superseded') AND updated_at<clock_timestamp()-interval '168 hours'
 AND NOT EXISTS(SELECT 1 FROM sessions s WHERE s.id=b.session_id)`); err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `SELECT id::text,connection_id::text,scope,desired_action,revision,control_generation FROM mcp_session_bindings
 WHERE (desired_action='active' OR (desired_action IN ('suspend','close') AND NOT delivered)) AND terminal_record IS NULL ORDER BY updated_at,id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPBindingWork
	for rows.Next() {
		var item MCPBindingWork
		var scope []byte
		if err = rows.Scan(&item.ID, &item.ConnectionID, &scope, &item.Action, &item.Revision, &item.ControlGeneration); err != nil {
			return nil, err
		}
		if json.Unmarshal(scope, &item.Scope) != nil || !item.Scope.Valid() {
			return nil, ErrMCPSessionScope
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// An ack cannot acknowledge a different intent that committed during HTTP I/O.
func CompleteMCPBindingWork(ctx context.Context, pool *pgxpool.Pool, item MCPBindingWork, superseded bool, generation int64) error {
	next := item.Action
	if superseded {
		next = "superseded"
	} else if item.Action == "close" {
		next = "done"
	}
	_, err := pool.Exec(ctx, `UPDATE mcp_session_bindings SET desired_action=$4,delivered=true,control_generation=$5,updated_at=clock_timestamp()
 WHERE id=$1 AND revision=$2 AND desired_action=$3 AND control_generation=$6`, item.ID, item.Revision, item.Action, next, generation, item.ControlGeneration)
	return err
}

func SuspendMCPBindingWork(ctx context.Context, pool *pgxpool.Pool, item MCPBindingWork) error {
	_, err := pool.Exec(ctx, `UPDATE mcp_session_bindings SET desired_action='suspend',delivered=false,revision=revision+1,updated_at=clock_timestamp()
 WHERE id=$1 AND revision=$2 AND desired_action='active'`, item.ID, item.Revision)
	return err
}

// Runtime triggers retain facts; this is the one canonical Go terminality rule.
func promoteTerminalMCPBindings(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `SELECT id::text,terminal_record,revision FROM mcp_session_bindings
 WHERE desired_action IN ('active','suspend','superseded') AND terminal_record IS NOT NULL ORDER BY updated_at,id LIMIT 100`)
	if err != nil {
		return err
	}
	type fact struct {
		id       string
		raw      []byte
		revision int64
	}
	var facts []fact
	for rows.Next() {
		var item fact
		if err = rows.Scan(&item.id, &item.raw, &item.revision); err != nil {
			break
		}
		facts = append(facts, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range facts {
		var records []struct {
			Status    string     `json:"status"`
			EndedAt   *time.Time `json:"ended_at"`
			EndReason *string    `json:"end_reason"`
		}
		cause := ""
		if json.Unmarshal(item.raw, &records) != nil {
			cause = "terminal_facts_decode"
		} else if len(records) > 32 {
			cause = "terminal_facts_limit"
		}
		if cause == "" {
			if len(records) == 0 {
				cause = "terminal_facts_shape"
			}
			for _, record := range records {
				if record.EndedAt == nil {
					cause = "terminal_facts_shape"
					break
				}
			}
		}
		if cause != "" {
			failurelog.Default.Event("mcp_binding_row:"+item.id+":"+cause, slog.LevelWarn, "component", "runner", "operation", "mcp_binding_reconcile", "binding_id", item.id, "cause", cause)
			// Keep unreadable facts out of dispatch work, and move this row to
			// the back of the next bounded batch so it cannot starve valid rows.
			if _, deferErr := pool.Exec(ctx, `UPDATE mcp_session_bindings SET updated_at=clock_timestamp() WHERE id=$1 AND revision=$2 AND terminal_record=$3::jsonb`, item.id, item.revision, item.raw); deferErr != nil {
				failurelog.Default.Event("mcp_binding_defer:"+item.id, slog.LevelWarn, "component", "runner", "operation", "mcp_binding_reconcile", "binding_id", item.id, "cause", failurelog.Cause(deferErr))
			}
			continue
		}
		terminal := false
		for _, record := range records {
			terminal = terminal || TerminallyEnded(&SessionRow{Status: record.Status, EndedAt: record.EndedAt, EndReason: record.EndReason})
		}
		if terminal {
			_, err = pool.Exec(ctx, `UPDATE mcp_session_bindings SET desired_action='close',terminal_record=NULL,delivered=false,revision=revision+1,updated_at=clock_timestamp()
 WHERE id=$1 AND revision=$2 AND terminal_record=$3::jsonb AND desired_action IN ('active','suspend','superseded')`, item.id, item.revision, item.raw)
		} else {
			_, err = pool.Exec(ctx, `UPDATE mcp_session_bindings SET terminal_record=NULL WHERE id=$1 AND revision=$2 AND terminal_record=$3::jsonb`, item.id, item.revision, item.raw)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
