package db

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"time"
)

const progressScopeSQL = `COALESCE(NULLIF(payload->>'child_id',''),payload->>'parent_call_id','')`

// ListScopeProgressEvents reads bounded native snapshots and subsequent task deltas for exactly
// one scope and incarnation. Root plans retain the established latest-root snapshot query.
func ListScopeProgressEvents(ctx context.Context, q Querier, sid string, through int64, key string, since int64) ([]AgentEventRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out []AgentEventRow
	baseline := since
	cursor := through
	for cursor > since {
		var r AgentEventRow
		err := q.QueryRow(ctx, `SELECT session_id,seq,client_event_id,kind,payload::text,ts FROM agent_events WHERE session_id=$1 AND kind='plan_updated' AND `+progressScopeSQL+`=$3 AND seq<=$2 AND seq>$4 ORDER BY seq DESC LIMIT 1`, sid, cursor, key, since).Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if p, ok := protocol.DecodePlanUpdated([]byte(r.Payload)); ok {
			scope, _ := protocol.ProgressScope(p.ParentCallID, p.ChildID)
			if scope == key {
				out = append(out, r)
				baseline = r.Seq
				break
			}
		}
		cursor = r.Seq - 1
	}
	rows, err := q.Query(ctx, `SELECT session_id,seq,client_event_id,kind,payload::text,ts FROM agent_events WHERE session_id=$1 AND seq<=$2 AND seq>$3 AND `+PlanToolsSQL+` AND `+progressScopeSQL+`=$4 ORDER BY seq`, sid, through, baseline, key)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r AgentEventRow
		if err := rows.Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	cursor = through
	for cursor > since {
		var r AgentEventRow
		err := q.QueryRow(ctx, `SELECT session_id,seq,client_event_id,kind,payload::text,ts FROM agent_events WHERE session_id=$1 AND kind='progress_updated' AND `+progressScopeSQL+`=$3 AND seq<=$2 AND seq>$4 ORDER BY seq DESC LIMIT 1`, sid, cursor, key, since).Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if p, ok := protocol.DecodeProgressUpdated([]byte(r.Payload)); ok {
			scope, _ := protocol.ProgressScope(p.ParentCallID, p.ChildID)
			if scope == key {
				out = append(out, r)
				break
			}
		}
		cursor = r.Seq - 1
	}
	return out, nil
}
