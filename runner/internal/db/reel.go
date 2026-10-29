package db

// The reads and writes behind a session's highlight reel (server/topics.go): the reel's events, the
// three cheap reads its ETag is made of, and the serialised append of a topic event.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReelKindsSQL is the kind list of agent_events_reel_idx (migration 041), written out in the query so
// the planner can prove the query's predicate implies the index's. topics.Kinds is the same list.
const ReelKindsSQL = `('user_message', 'topic', 'artifact', 'subagent_started', 'subagent_update', 'subagent_done', 'turn_done', 'status_changed')`

// ReelEventsSQL reads a session's reel events up to a seq ($1 session, $2 through seq), in order.
const ReelEventsSQL = `SELECT session_id, seq, client_event_id, kind, payload::text, ts FROM agent_events
	WHERE session_id = $1 AND seq <= $2 AND kind IN ` + ReelKindsSQL + ` ORDER BY seq`

// ListReelEvents returns a session's events of the reel kinds with seq <= throughSeq, in seq order.
func ListReelEvents(ctx context.Context, pool *pgxpool.Pool, sessionID string, throughSeq int64) ([]AgentEventRow, error) {
	rows, err := pool.Query(ctx, ReelEventsSQL, sessionID, throughSeq)
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

// MaxAgentEventSeq is the highest seq a session's transcript holds (0 when it holds none). It reads
// agent_events_session_seq_idx only.
func MaxAgentEventSeq(ctx context.Context, pool *pgxpool.Pool, sessionID string) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx, `SELECT coalesce(max(seq), 0) FROM agent_events WHERE session_id = $1`, sessionID).Scan(&n)
	return n, err
}

// ArtifactStamp is how many files a session holds (every origin) and the newest one's created_at (the
// epoch when there is none), read from session_artifacts_session_created_idx: an addition or a
// deletion changes it.
func ArtifactStamp(ctx context.Context, pool *pgxpool.Pool, sessionID string) (int, time.Time, error) {
	var n int
	var newest time.Time
	err := pool.QueryRow(ctx, `SELECT count(*), coalesce(max(created_at), 'epoch'::timestamptz)
		FROM session_artifacts WHERE session_id = $1`, sessionID).Scan(&n, &newest)
	return n, newest, err
}

// LockTopics takes the session's topic lock for the rest of tx: one topic change at a time per
// session, whichever server process takes it.
func LockTopics(ctx context.Context, tx pgx.Tx, sessionID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "topics:"+sessionID)
	return err
}

// TopicState is the session's newest topic event (nil when there is none) and how many it holds.
func TopicState(ctx context.Context, tx pgx.Tx, sessionID string) (*AgentEventRow, int, error) {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id = $1 AND kind = 'topic'`, sessionID).Scan(&n); err != nil {
		return nil, 0, err
	}
	if n == 0 {
		return nil, 0, nil
	}
	var r AgentEventRow
	err := tx.QueryRow(ctx, `SELECT session_id, seq, client_event_id, kind, payload::text, ts FROM agent_events
		WHERE session_id = $1 AND kind = 'topic' ORDER BY seq DESC LIMIT 1`, sessionID).
		Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts)
	if err != nil {
		return nil, 0, err
	}
	return &r, n, nil
}

// AppendAgentEventTx is AppendAgentEventAt inside the caller's transaction: the same insert, duplicate
// lookup and retry (appendAgentEvent), each attempt under a savepoint so a failed one does not abort tx.
func AppendAgentEventTx(ctx context.Context, tx pgx.Tx, sessionID, clientEventID, kind, payload string, ts time.Time) (int64, bool, error) {
	return appendAgentEvent(ctx, tx, sessionID, clientEventID, kind, payload, ts)
}

// ListProposalDecidedEvents returns a session's proposal_decided events with seq <= throughSeq, in
// seq order: the History tab's card chips (migration 067's index).
func ListProposalDecidedEvents(ctx context.Context, pool *pgxpool.Pool, sessionID string, throughSeq int64) ([]AgentEventRow, error) {
	return listKindEvents(ctx, pool, `SELECT session_id, seq, client_event_id, kind, payload::text, ts FROM agent_events
		WHERE session_id = $1 AND seq <= $2 AND kind = 'proposal_decided' ORDER BY seq`, sessionID, throughSeq)
}

// ListSessionRenamedEvents returns a session's session_renamed events with seq <= throughSeq, in seq
// order: the History tab's rename rows (agent_events_session_renamed_idx, EnsureConcurrentIndexes).
func ListSessionRenamedEvents(ctx context.Context, pool *pgxpool.Pool, sessionID string, throughSeq int64) ([]AgentEventRow, error) {
	return listKindEvents(ctx, pool, `SELECT session_id, seq, client_event_id, kind, payload::text, ts FROM agent_events
		WHERE session_id = $1 AND seq <= $2 AND kind = 'session_renamed' ORDER BY seq`, sessionID, throughSeq)
}

func listKindEvents(ctx context.Context, pool *pgxpool.Pool, query, sessionID string, throughSeq int64) ([]AgentEventRow, error) {
	rows, err := pool.Query(ctx, query, sessionID, throughSeq)
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

// StrokeKindsSQL is the kind list of agent_events_strokes_idx (EnsureConcurrentIndexes), written out
// in the query so the planner can prove the query's predicate implies the index's. topics.StrokeKinds
// is the same list.
const StrokeKindsSQL = `('mark', 'ask', 'proposal_request', 'review_requested', 'review_decided', 'delegation_request', 'delegation_decided')`

// ListStrokeEvents returns a session's events of the stroke kinds with seq <= throughSeq, in seq
// order: the History feed's marks, asks, approvals, proposals and reviews.
func ListStrokeEvents(ctx context.Context, pool *pgxpool.Pool, sessionID string, throughSeq int64) ([]AgentEventRow, error) {
	return listKindEvents(ctx, pool, `SELECT session_id, seq, client_event_id, kind, payload::text, ts FROM agent_events
		WHERE session_id = $1 AND seq <= $2 AND kind IN `+StrokeKindsSQL+` ORDER BY seq`, sessionID, throughSeq)
}

// AskStates reads a session's asks (the messages rows): the ids of those closed without an answer
// (answered with an empty answer: `blerg-runner ask --resolve`, the session's end), and how many are
// still open (a part of the History's ETag: a close writes no event).
func AskStates(ctx context.Context, pool *pgxpool.Pool, sessionID string) (closed, dismissed map[string]bool, open int, err error) {
	rows, err := pool.Query(ctx, `SELECT id::text, status, coalesce(answer, '') = '', dismissed FROM messages WHERE session_id = $1 AND kind = 'ask'`, sessionID)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	closed, dismissed = map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var id, status string
		var empty, wasDismissed bool
		if err := rows.Scan(&id, &status, &empty, &wasDismissed); err != nil {
			return nil, nil, 0, err
		}
		switch {
		case status == "open":
			open++
		case empty:
			closed[id] = true
			if wasDismissed {
				dismissed[id] = true
			}
		}
	}
	return closed, dismissed, open, rows.Err()
}

// LockMarks takes the session's mark lock for the rest of tx: the count of its marks and the append
// of new ones are one step per session, whichever server process takes it.
func LockMarks(ctx context.Context, tx pgx.Tx, sessionID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "marks:"+sessionID)
	return err
}

// CountMarks is how many marks a session holds.
func CountMarks(ctx context.Context, tx pgx.Tx, sessionID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id = $1 AND kind = 'mark'`, sessionID).Scan(&n)
	return n, err
}

// ClaimMarkRequest shares the append transaction, so failures leave no retry claim behind.
// The existing idempotency sweep bounds claim retention; deterministic event IDs also prevent
// an identical retry from appending a second event after a claim has been swept.
func ClaimMarkRequest(ctx context.Context, tx pgx.Tx, scope, key, hash, sessionID string) (bool, error) {
	var held string
	err := tx.QueryRow(ctx, `INSERT INTO runner_idempotency (scope, key, request_hash, session_id)
 VALUES ($1, $2, $3, $4) ON CONFLICT (scope, key) DO UPDATE SET key = EXCLUDED.key
 RETURNING request_hash`, scope, key, hash, sessionID).Scan(&held)
	return held == hash, err
}
