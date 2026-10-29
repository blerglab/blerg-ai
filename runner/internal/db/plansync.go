package db

// The facts behind a session's honest plan (server/plansync.go, migration 075): whether Blerg
// nudges the agent about drift, which drift it already told, what a turn did, the session's
// children, and when a parent last read a child's result.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PlanNudgesOn reports whether the session's drift nudges are on (the default; false for an
// unknown session).
func PlanNudgesOn(ctx context.Context, q Querier, sessionID string) (bool, error) {
	var on bool
	err := q.QueryRow(ctx, `SELECT plan_nudges FROM sessions WHERE id = $1`, sessionID).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// SetPlanNudges turns the session's drift nudges on or off.
func SetPlanNudges(ctx context.Context, q Querier, sessionID string, on bool) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET plan_nudges = $2 WHERE id = $1`, sessionID, on)
	return err
}

// AddPlanFact records drift found outside a turn's end (text is what the next nudge says of it).
// A fact already recorded, told or not, is left as it is.
func AddPlanFact(ctx context.Context, q Querier, sessionID, key, text string) error {
	_, err := q.Exec(ctx, `INSERT INTO session_plan_nudges (session_id, key, pending) VALUES ($1, $2, $3)
		ON CONFLICT (session_id, key) DO NOTHING`, sessionID, key, text)
	return err
}

// PlanFact is one recorded fact not yet told.
type PlanFact struct {
	Key  string
	Text string
}

// PendingPlanFacts are the recorded facts no nudge has carried yet, oldest first.
func PendingPlanFacts(ctx context.Context, q Querier, sessionID string) ([]PlanFact, error) {
	rows, err := q.Query(ctx, `SELECT key, pending FROM session_plan_nudges
		WHERE session_id = $1 AND nudged_at IS NULL AND pending IS NOT NULL ORDER BY created_at, key LIMIT 20`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanFact
	for rows.Next() {
		var f PlanFact
		if err := rows.Scan(&f.Key, &f.Text); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// NudgedPlanKeys is the subset of keys a nudge already carried.
func NudgedPlanKeys(ctx context.Context, q Querier, sessionID string, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT key FROM session_plan_nudges WHERE session_id = $1 AND key = ANY($2) AND nudged_at IS NOT NULL`, sessionID, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// MarkPlanNudged records that a nudge carried keys.
func MarkPlanNudged(ctx context.Context, q Querier, sessionID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO session_plan_nudges (session_id, key, nudged_at)
		SELECT $1, k, now() FROM unnest($2::text[]) AS k
		ON CONFLICT (session_id, key) DO UPDATE SET nudged_at = COALESCE(session_plan_nudges.nudged_at, now())`, sessionID, keys)
	return err
}

// TurnEvent is what the drift check reads of one event of a turn.
type TurnEvent struct {
	Seq        int64
	Kind       string
	Source     string // a user_message's source
	Tool       string // a tool_call's tool
	Command    string // a tool_call's input.command, its first 64 characters
	ChildID    string // a subagent event's child_id
	Background bool   // a subagent event's background flag
	Ts         time.Time
}

// turnEventLimit bounds one turn's read (a turn of more events is judged on its first ones).
const turnEventLimit = 4000

// TurnEvents is the turn that the turn_done at doneSeq ended: the events after the turn_done before
// it, through doneSeq, of the kinds the drift check reads, and when the turn began (the first
// event's time; zero for an empty turn).
func TurnEvents(ctx context.Context, q Querier, sessionID string, doneSeq int64) ([]TurnEvent, error) {
	var prev int64
	err := q.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM agent_events WHERE session_id = $1 AND kind = 'turn_done' AND seq < $2`,
		sessionID, doneSeq).Scan(&prev)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT seq, kind, COALESCE(payload->>'source', ''), COALESCE(payload->>'tool', ''),
			left(COALESCE(payload->'input'->>'command', ''), 64), COALESCE(payload->>'child_id', ''),
			COALESCE(payload->>'background', '') = 'true', ts
		FROM agent_events WHERE session_id = $1 AND seq > $2 AND seq <= $3
		  AND kind IN ('user_message', 'tool_call', 'subagent_started', 'subagent_done')
		ORDER BY seq LIMIT $4`, sessionID, prev, doneSeq, turnEventLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TurnEvent
	for rows.Next() {
		var e TurnEvent
		if err := rows.Scan(&e.Seq, &e.Kind, &e.Source, &e.Tool, &e.Command, &e.ChildID, &e.Background, &e.Ts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// TurnStart is when the turn ended by the turn_done at doneSeq began: the time of the turn_done
// before it (the session's start for its first turn).
func TurnStart(ctx context.Context, q Querier, sessionID string, doneSeq int64) (time.Time, error) {
	var at time.Time
	err := q.QueryRow(ctx, `SELECT COALESCE(
			(SELECT ts FROM agent_events WHERE session_id = $1 AND kind = 'turn_done' AND seq < $2 ORDER BY seq DESC LIMIT 1),
			(SELECT started_at FROM sessions WHERE id = $1))`, sessionID, doneSeq).Scan(&at)
	return at, err
}

// PlanChild is one child session as the drift check reads it.
type PlanChild struct {
	ID        string
	Title     string
	Status    string
	StartedAt time.Time
	// LastSeq is the child's newest event; ReadSeq the newest when its parent last read its result
	// (-1 when it never did).
	Blocked bool
	LastSeq int64
	ReadSeq int64
}

// PlanChildren are the parent's children that are neither archived nor being discarded, at most 100,
// newest first.
func PlanChildren(ctx context.Context, q Querier, parentID string) ([]PlanChild, error) {
	rows, err := q.Query(ctx, `SELECT s.id::text, COALESCE(s.title, ''), s.status, s.started_at,
			COALESCE((SELECT MAX(seq) FROM agent_events e WHERE e.session_id = s.id), 0),
			COALESCE((SELECT r.seq FROM child_result_reads r WHERE r.parent_session_id = $1 AND r.child_session_id = s.id), -1),
 EXISTS(SELECT 1 FROM messages m WHERE m.session_id=s.id AND m.kind='ask' AND m.status='open') OR EXISTS(SELECT 1 FROM review_requests r WHERE r.session_id=s.id AND r.state='pending')
		FROM sessions s
		WHERE s.parent_session_id = $1 AND s.archived_at IS NULL AND s.discard_requested_at IS NULL
		ORDER BY s.started_at DESC LIMIT 100`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanChild
	for rows.Next() {
		var c PlanChild
		if err := rows.Scan(&c.ID, &c.Title, &c.Status, &c.StartedAt, &c.LastSeq, &c.ReadSeq, &c.Blocked); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordChildResultRead notes that the parent read the child's result now, with the child's newest
// event at this moment.
func RecordChildResultRead(ctx context.Context, q Querier, parentID, childID string) error {
	_, err := q.Exec(ctx, `INSERT INTO child_result_reads (parent_session_id, child_session_id, seq)
		SELECT $1, $2, COALESCE((SELECT MAX(seq) FROM agent_events WHERE session_id = $2), 0)
		ON CONFLICT (parent_session_id, child_session_id) DO UPDATE SET seq = EXCLUDED.seq, read_at = now()`, parentID, childID)
	return err
}
