package db

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type ChildTelemetry struct {
	Events      []AgentEventRow
	ToolCalls   int64
	ToolPending bool
	ResetSeq    int64
	ResetAt     *time.Time
}

// Read only the newest event of each relevant kind, not a child's full transcript.
// The caller's through boundary also bounds completed usage and the parent ETag.
func ReadChildTelemetry(ctx context.Context, pool *pgxpool.Pool, id string, through int64) (ChildTelemetry, error) {
	var out ChildTelemetry
	rows, err := scanEventRows(ctx, pool, `SELECT e.session_id,e.seq,e.client_event_id,e.kind,e.payload::text,e.ts
 FROM unnest($3::text[]) k(kind) CROSS JOIN LATERAL (
 SELECT session_id,seq,client_event_id,kind,payload,ts FROM agent_events
 WHERE session_id=$1 AND seq<=$2 AND kind=k.kind ORDER BY seq DESC LIMIT 1) e`, id, through, []string{"tool_call", "tool_result", "turn_done", "usage_updated", "status_changed"})
	if err != nil {
		return out, err
	}
	sortRowsBySeq(rows)
	out.Events = rows
	var resetAt time.Time
	err = pool.QueryRow(ctx, `SELECT seq,ts FROM agent_events WHERE session_id=$1 AND seq<=$2 AND kind='usage_updated' AND payload->'usage'='null'::jsonb ORDER BY seq DESC LIMIT 1`, id, through).Scan(&out.ResetSeq, &resetAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if err == nil {
		out.ResetAt = &resetAt
	}

	if err = pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND seq<=$2 AND kind='tool_call'`, id, through).Scan(&out.ToolCalls); err != nil {
		return out, err
	}
	for _, e := range rows {
		if e.Kind != "tool_call" {
			continue
		}
		err = pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM agent_events WHERE session_id=$1 AND seq>$2 AND seq<=$3 AND kind='tool_result' AND payload->>'call_id'=(SELECT payload->>'call_id' FROM agent_events WHERE session_id=$1 AND seq=$2))`, id, e.Seq, through).Scan(&out.ToolPending)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
