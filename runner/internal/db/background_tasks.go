package db

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/livestate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidBackgroundTaskEvent = errors.New("invalid background task event")

// ErrBackgroundTaskAuthority refuses a write after a session changed daemon ownership.
var ErrBackgroundTaskAuthority = errors.New("background task daemon authority changed")

const backgroundTaskEventsSQL = `SELECT session_id,seq,client_event_id,kind,payload::text,ts FROM agent_events
 WHERE session_id=$1 AND kind IN ('background_task_started','background_task_done') ORDER BY seq`

func backgroundTaskEvents(ctx context.Context, q Querier, sid string) ([]livestate.Event, error) {
	rows, err := q.Query(ctx, backgroundTaskEventsSQL, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []livestate.Event
	for rows.Next() {
		var row AgentEventRow
		if err := rows.Scan(&row.SessionID, &row.Seq, &row.ClientEventID, &row.Kind, &row.Payload, &row.Ts); err != nil {
			return nil, err
		}
		events = append(events, livestate.Event{Seq: row.Seq, ID: row.ClientEventID, Kind: row.Kind, Payload: json.RawMessage(row.Payload), Ts: row.Ts})
	}
	return events, rows.Err()
}

func backgroundCount(events []livestate.Event, status string, ended *time.Time) int {
	if ended != nil || status == "ended" || status == "stopped" || status == "error" {
		return 0
	}
	return len(livestate.OpenBackgroundTasks(events))
}

// AppendBackgroundTaskEventAt atomically appends a task event and repairs the session's exact
// semantic open-task count. The session row lock serializes normal ingestion and reconciliation.
func AppendBackgroundTaskEventAt(ctx context.Context, pool *pgxpool.Pool, sid, eventID, kind, payload string, at time.Time, expectedDaemonID ...string) (int64, bool, int, error) {
	var p struct {
		TaskID       string `json:"task_id"`
		StartEventID string `json:"start_event_id"`
		OriginalSeq  int64  `json:"origin_start_seq"`
	}
	if (kind != "background_task_started" && kind != "background_task_done") || json.Unmarshal([]byte(payload), &p) != nil || p.TaskID == "" || p.StartEventID != "" || p.OriginalSeq != 0 {
		return 0, false, 0, ErrInvalidBackgroundTaskEvent
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, 0, err
	}
	defer rollbackBackgroundTaskTx(ctx, tx)
	var status, owner string
	var ended *time.Time
	if err := tx.QueryRow(ctx, `SELECT status,ended_at,daemon_id FROM sessions WHERE id=$1 FOR UPDATE`, sid).Scan(&status, &ended, &owner); err != nil {
		return 0, false, 0, err
	}
	if len(expectedDaemonID) > 0 && (len(expectedDaemonID) != 1 || expectedDaemonID[0] != owner) {
		return 0, false, 0, ErrBackgroundTaskAuthority
	}
	seq, fresh, err := AppendAgentEventTx(ctx, tx, sid, eventID, kind, payload, at)
	if err != nil {
		return 0, false, 0, err
	}
	events, err := backgroundTaskEvents(ctx, tx, sid)
	if err != nil {
		return 0, false, 0, err
	}
	count := backgroundCount(events, status, ended)
	if _, err := tx.Exec(ctx, `UPDATE sessions SET background_tasks=$2 WHERE id=$1`, sid, count); err != nil {
		return 0, false, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, 0, err
	}
	return seq, fresh, count, nil
}

// AppendSubagentEventAt shares the task reconciliation lock so a newer child incarnation
// cannot race a synthetic completion. The original replay/idempotency semantics are unchanged.
func AppendSubagentEventAt(ctx context.Context, pool *pgxpool.Pool, sid, eventID, kind, payload string, at time.Time, expectedDaemonID ...string) (int64, bool, error) {
	var p struct {
		ChildID      string `json:"child_id"`
		StartEventID string `json:"start_event_id"`
	}
	if (kind != "subagent_started" && kind != "subagent_update" && kind != "subagent_done") || json.Unmarshal([]byte(payload), &p) != nil || p.ChildID == "" || p.StartEventID != "" {
		return 0, false, ErrInvalidBackgroundTaskEvent
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer rollbackBackgroundTaskTx(ctx, tx)
	var owner string
	if err := tx.QueryRow(ctx, `SELECT daemon_id FROM sessions WHERE id=$1 FOR UPDATE`, sid).Scan(&owner); err != nil {
		return 0, false, err
	}
	if len(expectedDaemonID) > 0 && (len(expectedDaemonID) != 1 || expectedDaemonID[0] != owner) {
		return 0, false, ErrBackgroundTaskAuthority
	}
	seq, fresh, err := AppendAgentEventTx(ctx, tx, sid, eventID, kind, payload, at)
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, err
	}
	return seq, fresh, nil
}

// BackgroundTaskReconciliation contains only changes committed for one session. Events are
// normal durable transcript rows; the caller broadcasts them and Count after commit.
type BackgroundTaskReconciliation struct {
	SessionID string
	Events    []AgentEventRow
	Count     int
}

// ReconcileBackgroundTasks closes open tasks from ended daemon lifetimes, except IDs the current
// daemon explicitly reports alive. An absent lifetime/inventory is an old peer and does nothing.
// boundSessionID confines a session pod; daemon ownership is rechecked under each row lock.
func ReconcileBackgroundTasks(ctx context.Context, pool *pgxpool.Pool, daemonID, boundSessionID, lifetime string, inventory map[string][]string) ([]BackgroundTaskReconciliation, error) {
	if lifetime == "" || inventory == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx, `SELECT id FROM sessions WHERE daemon_id=$1 AND ($2='' OR id::text=$2)
 AND (background_tasks>0 OR (kind='agent' AND ended_at IS NULL AND status NOT IN ('ended','stopped','error'))) ORDER BY id`, daemonID, boundSessionID)
	if err != nil {
		return nil, err
	}
	var sessions []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return nil, err
		}
		sessions = append(sessions, sid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []BackgroundTaskReconciliation
	for _, sid := range sessions {
		changed, err := reconcileSessionBackgroundTasks(ctx, pool, daemonID, sid, lifetime, inventory[sid])
		if err != nil {
			return out, err
		}
		if changed != nil {
			out = append(out, *changed)
		}
	}
	return out, nil
}

func reconcileSessionBackgroundTasks(ctx context.Context, pool *pgxpool.Pool, daemonID, sid, lifetime string, reported []string) (*BackgroundTaskReconciliation, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackBackgroundTaskTx(ctx, tx)
	var owner, status string
	var ended *time.Time
	var before int
	if err := tx.QueryRow(ctx, `SELECT daemon_id,status,ended_at,background_tasks FROM sessions WHERE id=$1 FOR UPDATE`, sid).Scan(&owner, &status, &ended, &before); errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if owner != daemonID {
		return nil, nil
	}
	events, err := backgroundTaskEvents(ctx, tx, sid)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(reported))
	for _, id := range reported {
		live[id] = true
	}
	result := &BackgroundTaskReconciliation{SessionID: sid}
	for _, open := range livestate.OpenBackgroundTasks(events) {
		if open.Lifetime == lifetime {
			continue
		}
		if live[open.Task.TaskID] {
			rebound, err := appendReboundBackgroundTask(ctx, tx, sid, lifetime, open)
			if err != nil {
				return nil, err
			}
			if rebound != nil {
				result.Events = append(result.Events, *rebound)
				events = append(events, livestate.Event{Seq: rebound.Seq, ID: rebound.ClientEventID, Kind: rebound.Kind, Payload: json.RawMessage(rebound.Payload), Ts: rebound.Ts})
			}
			continue
		}
		id := backgroundStoppedEventID(sid, open.StartEventID)
		payload, _ := json.Marshal(map[string]string{"task_id": open.Task.TaskID, "call_id": open.Task.CallID, "kind": open.Task.Kind, "description": open.Task.Description, "status": "stopped", "summary": "the daemon restarted", "start_event_id": open.StartEventID, "daemon_lifetime": open.Lifetime})
		at := time.Now().UTC()
		seq, fresh, err := AppendAgentEventTx(ctx, tx, sid, id, "background_task_done", string(payload), at)
		if err != nil {
			return nil, err
		}
		if fresh {
			row := AgentEventRow{SessionID: sid, Seq: seq, ClientEventID: id, Kind: "background_task_done", Payload: string(payload), Ts: at}
			result.Events = append(result.Events, row)
			events = append(events, livestate.Event{Seq: seq, ID: id, Kind: row.Kind, Payload: payload, Ts: at})
			if open.Task.Kind == "agent" {
				companion, err := appendStoppedBackgroundSubagent(ctx, tx, sid, open, at)
				if err != nil {
					return nil, err
				}
				if companion != nil {
					result.Events = append(result.Events, *companion)
				}
			}
		}
	}
	result.Count = backgroundCount(events, status, ended)
	if _, err := tx.Exec(ctx, `UPDATE sessions SET background_tasks=$2 WHERE id=$1`, sid, result.Count); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if len(result.Events) == 0 && result.Count == before {
		return nil, nil
	}
	return result, nil
}

func backgroundStoppedEventID(sid, startID string) string {
	sum := sha256.Sum256([]byte("blerg/background-task-stopped/v1\x00" + sid + "\x00" + startID))
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func rollbackBackgroundTaskTx(ctx context.Context, tx pgx.Tx) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanup)
}

func appendStoppedBackgroundSubagent(ctx context.Context, tx pgx.Tx, sid string, open livestate.BackgroundTaskState, at time.Time) (*AgentEventRow, error) {
	child := open.Task.CallID
	if child == "" {
		child = open.Task.TaskID
	}
	var start AgentEventRow
	err := tx.QueryRow(ctx, `SELECT session_id,seq,client_event_id,kind,payload::text,ts FROM agent_events
 WHERE session_id=$1 AND kind='subagent_started' AND payload->>'child_id'=$2 ORDER BY seq DESC LIMIT 1`, sid, child).Scan(&start.SessionID, &start.Seq, &start.ClientEventID, &start.Kind, &start.Payload, &start.Ts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if start.Seq > open.OriginalSeq {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT payload::text FROM agent_events WHERE session_id=$1 AND kind='subagent_done' AND seq>$2 AND payload->>'child_id'=$3 ORDER BY seq DESC`, sid, start.Seq, child)
	if err != nil {
		return nil, err
	}
	final := false
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		var p struct {
			Background   bool   `json:"background"`
			StartEventID string `json:"start_event_id"`
		}
		if json.Unmarshal([]byte(raw), &p) != nil || (p.StartEventID != "" && p.StartEventID != start.ClientEventID) {
			continue
		}
		final = !p.Background
		break
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if final {
		return nil, nil
	}
	payload, _ := json.Marshal(map[string]any{"child_id": child, "background": false, "summary": "stopped: the daemon restarted", "start_event_id": start.ClientEventID})
	id := backgroundStoppedEventID(sid, "subagent:"+start.ClientEventID)
	seq, fresh, err := AppendAgentEventTx(ctx, tx, sid, id, "subagent_done", string(payload), at)
	if err != nil {
		return nil, err
	}
	if !fresh {
		return nil, nil
	}
	return &AgentEventRow{SessionID: sid, Seq: seq, ClientEventID: id, Kind: "subagent_done", Payload: string(payload), Ts: at}, nil
}

func appendReboundBackgroundTask(ctx context.Context, tx pgx.Tx, sid, lifetime string, open livestate.BackgroundTaskState) (*AgentEventRow, error) {
	payload, _ := json.Marshal(map[string]any{"task_id": open.Task.TaskID, "call_id": open.Task.CallID, "kind": open.Task.Kind, "description": open.Task.Description, "started_at": open.Task.StartedAt.UTC().Format(time.RFC3339Nano), "daemon_lifetime": lifetime, "origin_start_seq": open.OriginalSeq})
	id := backgroundStoppedEventID(sid, "adopt:"+open.StartEventID+"\x00"+lifetime)
	at := time.Now().UTC()
	seq, fresh, err := AppendAgentEventTx(ctx, tx, sid, id, "background_task_started", string(payload), at)
	if err != nil {
		return nil, err
	}
	if !fresh {
		return nil, nil
	}
	return &AgentEventRow{SessionID: sid, Seq: seq, ClientEventID: id, Kind: "background_task_started", Payload: string(payload), Ts: at}, nil
}
