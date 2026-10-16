package db

// The reads behind a session's live state (server/livestate.go): the live events, the tails of the
// subagents that may still be running, and the state of the session's ask rows.

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/runner/internal/livestate"
)

// LiveKindsSQL is the kind list of agent_events_live_idx (migration 042); livestate.Kinds is the same list.
const LiveKindsSQL = `('ask', 'user_message', 'turn_done', 'subagent_started', 'subagent_update', 'subagent_done', 'background_task_started', 'background_task_done')`

// LiveEventsSQL reads a session's live events but subagent_update ($1 session, $2 through seq), in
// order. Its kind list is a subset of the index's, which the planner proves; the updates (every few
// seconds while a subagent works) are read per running subagent instead (ListSubagentTails).
const LiveEventsSQL = `SELECT session_id, seq, client_event_id, kind, payload::text, ts FROM agent_events
	WHERE session_id = $1 AND seq <= $2
	  AND kind IN ('ask', 'user_message', 'turn_done', 'subagent_started', 'subagent_done', 'background_task_started', 'background_task_done')
	ORDER BY seq`

func scanEventRows(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) ([]AgentEventRow, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentEventRow
	for rows.Next() {
		var r AgentEventRow
		if err := rows.Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListLiveEvents returns a session's live events (LiveEventsSQL) with seq <= throughSeq, in seq order.
func ListLiveEvents(ctx context.Context, pool *pgxpool.Pool, sessionID string, throughSeq int64) ([]AgentEventRow, error) {
	return scanEventRows(ctx, pool, LiveEventsSQL, sessionID, throughSeq)
}

// Each tail key's two newest subagent_update events after its seq, newest first (one index probe per key).
const subagentUpdatesSQL = `SELECT e.session_id, e.seq, e.client_event_id, e.kind, e.payload::text, e.ts
	FROM unnest($2::text[], $3::bigint[]) AS k(key, since)
	CROSS JOIN LATERAL (
		SELECT session_id, seq, client_event_id, kind, payload, ts FROM agent_events
		 WHERE session_id = $1 AND seq > k.since AND seq <= $4 AND kind = 'subagent_update' AND payload->>'child_id' = k.key
		 ORDER BY seq DESC LIMIT 2) e`

// Each tail key's newest own row (a tool call or a message of the subagent) after its bound: a
// backwards walk of the session's seq index that stops at the first.
const subagentRowsSQL = `SELECT e.session_id, e.seq, e.client_event_id, e.kind, e.payload::text, e.ts
	FROM unnest($2::text[], $3::bigint[]) AS k(key, since)
	CROSS JOIN LATERAL (
		SELECT session_id, seq, client_event_id, kind, payload, ts FROM agent_events
		 WHERE session_id = $1 AND seq > k.since AND seq <= $4 AND kind IN ('tool_call', 'assistant_text') AND payload->>'parent_call_id' = k.key
		 ORDER BY seq DESC LIMIT 1) e`

// ListSubagentTails reads what the live state needs of each subagent that may still be running: its
// two newest subagent_update events, and its newest own row after the older of the two (its start
// when it has fewer). The driver sends an update for a subagent's first line and then at most every
// few seconds, so its newest row is never before its second-newest update. Rows come in seq order.
func ListSubagentTails(ctx context.Context, pool *pgxpool.Pool, sessionID string, throughSeq int64, tails []livestate.Tail) ([]AgentEventRow, error) {
	if len(tails) == 0 {
		return nil, nil
	}
	keys := make([]string, len(tails))
	since := make([]int64, len(tails))
	for i, t := range tails {
		keys[i], since[i] = t.Key, t.Seq
	}
	updates, err := scanEventRows(ctx, pool, subagentUpdatesSQL, sessionID, keys, since, throughSeq)
	if err != nil {
		return nil, err
	}
	// The bound: the older of the two newest updates (they come newest first, per key).
	seen := map[string]int{}
	bound := map[string]int64{}
	for _, u := range updates {
		key := childID(u.Payload)
		seen[key]++
		if seen[key] == 2 {
			bound[key] = u.Seq
		}
	}
	for i, k := range keys {
		if b, ok := bound[k]; ok {
			since[i] = b
		}
	}
	rows, err := scanEventRows(ctx, pool, subagentRowsSQL, sessionID, keys, since, throughSeq)
	if err != nil {
		return nil, err
	}
	out := make([]AgentEventRow, 0, len(updates)+len(rows))
	out = append(out, updates...)
	out = append(out, rows...)
	sortRowsBySeq(out)
	return out, nil
}

// AskRows is the state of a session's ask rows in the messages table: Closed holds the ids of
// the ones no longer open (answered, closed, expired); Open and Total count them (the live state's
// ETag carries both, so an ask closed without a transcript event still changes it).
type AskRows struct {
	Closed map[string]bool
	Open   int
	Total  int
}

// ReadAskRows reads AskRows on messages_session_id_idx.
func ReadAskRows(ctx context.Context, pool *pgxpool.Pool, sessionID string) (AskRows, error) {
	st := AskRows{Closed: map[string]bool{}}
	rows, err := pool.Query(ctx, `SELECT id::text, status FROM messages WHERE session_id = $1 AND kind = 'ask'`, sessionID)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return st, err
		}
		st.Total++
		if status == "open" {
			st.Open++
		} else {
			st.Closed[id] = true
		}
	}
	return st, rows.Err()
}

func childID(payload string) string {
	var p struct {
		ChildID string `json:"child_id"`
	}
	_ = json.Unmarshal([]byte(payload), &p)
	return p.ChildID
}

func sortRowsBySeq(rows []AgentEventRow) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].Seq < rows[j].Seq })
}
