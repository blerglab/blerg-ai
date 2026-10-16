package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ListAgentSubagentContext returns at most three lifecycle rows per referenced
// child, through the page's upper fence. Each indexed lookup reads one row;
// neither transcript length nor the number of progress updates expands replay.
func ListAgentSubagentContext(ctx context.Context, pool *pgxpool.Pool, sessionID string, childIDs []string, throughSeq int64) ([]AgentEventRow, error) {
	if len(childIDs) == 0 || throughSeq <= 0 {
		return nil, nil
	}
	if len(childIDs) > 500 {
		childIDs = childIDs[:500]
	}
	rows, err := pool.Query(ctx, `
	 SELECT event.session_id,event.seq,event.client_event_id,event.kind,event.payload::text,event.ts
	 FROM unnest($2::text[]) AS child(child_id)
	 CROSS JOIN unnest(ARRAY['subagent_started','subagent_update','subagent_done']) AS lifecycle(kind)
	 CROSS JOIN LATERAL (
	   SELECT session_id,seq,client_event_id,kind,payload,ts FROM agent_events
	   WHERE session_id=$1 AND payload->>'child_id'=child.child_id
	     AND kind=lifecycle.kind AND seq<=$3
	     AND kind IN ('subagent_started','subagent_update','subagent_done')
	   ORDER BY seq DESC LIMIT 1
	 ) AS event ORDER BY event.seq`, sessionID, childIDs, throughSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentEventRow
	for rows.Next() {
		var row AgentEventRow
		if err := rows.Scan(&row.SessionID, &row.Seq, &row.ClientEventID, &row.Kind, &row.Payload, &row.Ts); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
