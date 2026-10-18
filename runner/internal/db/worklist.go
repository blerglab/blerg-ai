package db

// The reads and the lock behind a session's checklist, its pinned tickets and its engine plan
// (server/worklist.go, server/livestate.go), each on its own partial index (migration 055).

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/ticket"
)

// PlanToolsSQL is agent_events_plan_idx's predicate (migration 055), as the plan read repeats it so
// the planner proves the index; livestate.PlanTools is its tool list.
const PlanToolsSQL = `((kind = 'tool_call' AND payload->>'tool' IN ('TodoWrite', 'TaskCreate', 'TaskUpdate'))
       OR (kind = 'tool_result' AND payload->>'output' LIKE 'Task #%'))`

// LockSessionWork takes the session's advisory lock for one kind of agent write ("checklist",
// "tickets") until tx ends, so two quick writes read and append in turn and none is lost.
func LockSessionWork(ctx context.Context, tx pgx.Tx, what, sessionID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, what+":"+sessionID)
	return err
}

// CountSessionKind is how many events of kind (checklist or ticket) the session holds.
func CountSessionKind(ctx context.Context, q Querier, sessionID, kind string) (int, error) {
	var n int
	var err error
	switch kind {
	case "checklist":
		err = q.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id = $1 AND kind = 'checklist'`, sessionID).Scan(&n)
	case "ticket":
		err = q.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id = $1 AND kind = 'ticket'`, sessionID).Scan(&n)
	default:
		return 0, errors.New("CountSessionKind: " + kind)
	}
	return n, err
}

// LatestChecklist is the session's newest checklist event with seq <= through (nil when none).
func LatestChecklist(ctx context.Context, q Querier, sessionID string, through int64) (*AgentEventRow, error) {
	var r AgentEventRow
	err := q.QueryRow(ctx, `SELECT session_id, seq, client_event_id, kind, payload::text, ts FROM agent_events
		WHERE session_id = $1 AND kind = 'checklist' AND seq <= $2 ORDER BY seq DESC LIMIT 1`, sessionID, through).
		Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListTicketRows reads what ticket.Pinned folds of the session's ticket events with seq <= through:
// the summary fields only, never the description or the comments.
func ListTicketRows(ctx context.Context, q Querier, sessionID string, through int64) ([]ticket.Row, error) {
	rows, err := q.Query(ctx, `SELECT seq, client_event_id, ts,
			coalesce(payload->>'action', ''), coalesce(payload->'ticket'->>'source', ''), coalesce(payload->'ticket'->>'key', ''),
			coalesce(payload->'ticket'->>'title', ''), coalesce(payload->'ticket'->>'status', ''), coalesce(payload->'ticket'->>'url', '')
		FROM agent_events WHERE session_id = $1 AND kind = 'ticket' AND seq <= $2 ORDER BY seq`, sessionID, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ticket.Row
	for rows.Next() {
		var r ticket.Row
		var ts time.Time
		if err := rows.Scan(&r.Seq, &r.EventID, &ts, &r.Action, &r.Source, &r.Key, &r.Title, &r.Status, &r.URL); err != nil {
			return nil, err
		}
		r.Ts = ts
		out = append(out, r)
	}
	return out, rows.Err()
}

// TicketAt is the ticket a ticket event holds (its payload's "ticket"), by seq; nil when there is none.
func TicketAt(ctx context.Context, q Querier, sessionID string, seq int64) ([]byte, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT (payload->'ticket')::text FROM agent_events WHERE session_id = $1 AND kind = 'ticket' AND seq = $2`,
		sessionID, seq).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

// LatestPlanSnapshotSQL probes one newest native plan at a time. Keyset fallback skips malformed
// historical rows without streaming every prior snapshot; normal validated ingestion needs one probe.
const LatestPlanSnapshotSQL = `SELECT session_id, seq, client_event_id, kind, payload::text, ts FROM agent_events
 WHERE session_id = $1 AND kind = 'plan_updated' AND COALESCE(NULLIF(payload->>'child_id',''),payload->>'parent_call_id','') = '' AND seq <= $2 ORDER BY seq DESC LIMIT 1`

func latestValidPlanSnapshot(ctx context.Context, q Querier, sessionID string, through int64) (*AgentEventRow, error) {
	for through > 0 {
		var r AgentEventRow
		err := q.QueryRow(ctx, LatestPlanSnapshotSQL, sessionID, through).Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if p, valid := protocol.DecodePlanUpdated([]byte(r.Payload)); valid && p.ParentCallID == "" && p.ChildID == "" {
			return &r, nil
		}
		through = r.Seq - 1
	}
	return nil, nil
}

// ListPlanEvents returns the latest valid native snapshot and only the Claude deltas after it.
// Explicit empty snapshots clear older plans; malformed historical rows never hide a valid plan.
func ListPlanEvents(ctx context.Context, q Querier, sessionID string, through int64) ([]AgentEventRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot, err := latestValidPlanSnapshot(ctx, q, sessionID, through)
	if err != nil {
		return nil, err
	}
	baseline := int64(0)
	if snapshot != nil {
		baseline = snapshot.Seq
	}
	rows, err := q.Query(ctx, `SELECT session_id, seq, client_event_id, kind, payload::text, ts FROM agent_events
  WHERE session_id = $1 AND seq <= $2 AND seq > $3 AND `+PlanToolsSQL+` ORDER BY seq`, sessionID, through, baseline)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentEventRow
	if snapshot != nil {
		out = append(out, *snapshot)
	}
	for rows.Next() {
		var r AgentEventRow
		if err := rows.Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
