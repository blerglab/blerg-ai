package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionTurns derives complete and open turns from the same projected payloads
// as card timing. Results never include commands, outputs, or message bodies.
var errTurnEventBudget = errors.New("turn telemetry event budget exceeded")

// turnCatchUp bounds how many earlier unsaved turns one save recovers. A session
// that predates the table is not replayed whole.
const turnCatchUp = 8

func deriveTurnRange(ctx context.Context, pool *pgxpool.Pool, id string, now time.Time, after, through int64) ([]activity.Turn, error) {
	row, err := GetSession(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return []activity.Turn{}, nil
	}
	end := now
	if row.EndedAt != nil {
		end = *row.EndedAt
	}
	rows, err := pool.Query(ctx, `SELECT e.seq,e.ts,e.kind,`+timingPayloadSQL+` FROM agent_events e WHERE session_id=$1 AND kind IN ('status_changed','turn_done','tool_call','tool_result','turn_work','mark','background_task_started','background_task_done','process_activity') AND seq>$2 AND seq<=$3 ORDER BY seq LIMIT 8193`, id, after, through)
	if err != nil {
		return nil, err
	}
	var events []activity.Event
	for rows.Next() {
		var e activity.Event
		if err = rows.Scan(&e.Seq, &e.At, &e.Kind, &e.Payload); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	wr, err := pool.Query(ctx, `SELECT m.created_at,coalesce(m.answered_at,$2) FROM messages m WHERE m.session_id=$1 AND m.kind='ask' AND NOT m.no_wait AND (m.owner_only OR $3) AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=m.id)`, id, end, row.ParentSessionID == nil)
	if err != nil {
		return nil, err
	}
	var waits []activity.Interval
	for wr.Next() {
		var w activity.Interval
		if err = wr.Scan(&w.Start, &w.End); err != nil {
			wr.Close()
			return nil, err
		}
		waits = append(waits, w)
	}
	err = wr.Err()
	wr.Close()
	if err != nil {
		return nil, err
	}
	gr, err := pool.Query(ctx, `SELECT CASE WHEN data->>'state'='running' AND updated_at<$2::timestamptz-interval '1 hour' THEN data||jsonb_build_object('state','expired','ended_at',updated_at+interval '1 hour') ELSE data END FROM workspace_metric_runs WHERE session_id=$1 ORDER BY updated_at DESC LIMIT 32`, id, end)
	if err != nil {
		return nil, err
	}
	for gr.Next() {
		var raw []byte
		var run activity.Run
		if err = gr.Scan(&raw); err != nil {
			gr.Close()
			return nil, err
		}
		if err = json.Unmarshal(raw, &run); err != nil {
			gr.Close()
			return nil, err
		}
		stop := end
		if run.EndedAt != nil {
			stop = *run.EndedAt
		}
		p, _ := json.Marshal(map[string]time.Time{"started_at": run.StartedAt, "ended_at": stop})
		events = append(events, activity.Event{Kind: "gate_interval", Payload: p})
	}
	err = gr.Err()
	gr.Close()
	if err != nil {
		return nil, err
	}
	if len(events) > 8192 {
		return nil, errTurnEventBudget
	}
	turns := activity.Turns(row.StartedAt, end, events, waits)
	if row.EndedAt != nil && len(turns) > 0 && turns[len(turns)-1].Outcome == "running" {
		turns[len(turns)-1].Outcome = "session_ended"
	}
	return turns, nil
}

// SessionTurns reads compact stored rows. Only an uncached latest turn and the
// current tail can inspect canonical events, under their own small budget.
func SessionTurns(ctx context.Context, pool *pgxpool.Pool, id string, now time.Time) ([]activity.Turn, error) {
	budget, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if err := SaveTurnMetrics(budget, pool, id, now); err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	rows, err := pool.Query(ctx, `SELECT metrics FROM (SELECT turn_seq,metrics FROM session_turn_metrics WHERE session_id=$1 ORDER BY turn_seq DESC LIMIT 100) t ORDER BY turn_seq`, id)
	if err != nil {
		return nil, err
	}
	out := []activity.Turn{}
	for rows.Next() {
		var raw []byte
		var t activity.Turn
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(raw, &t); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var boundary int64
	if err = pool.QueryRow(ctx, `SELECT coalesce(max(seq),0) FROM agent_events WHERE session_id=$1 AND (kind='turn_done' OR kind='status_changed' AND payload->>'status'='idle')`, id).Scan(&boundary); err != nil {
		return nil, err
	}
	tail, err := deriveTurnRange(budget, pool, id, now, boundary, 1<<62)
	if err == nil {
		for _, turn := range tail {
			if turn.Seq == 0 {
				out = append(out, turn)
			}
		}
	}
	return out, nil
}

// SaveTurnMetrics writes a newly completed turn, plus recent turns whose own save
// was missed. Replays do no work, and older event history is never replayed to
// refresh an already stored row.
func SaveTurnMetrics(ctx context.Context, pool *pgxpool.Pool, id string, now time.Time) error {
	var target int64
	err := pool.QueryRow(ctx, `SELECT coalesce(max(seq),0) FROM agent_events WHERE session_id=$1 AND (kind='turn_done' OR kind='status_changed' AND payload->>'status'='idle')`, id).Scan(&target)
	if err != nil || target == 0 {
		return err
	}
	var exists bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_turn_metrics WHERE session_id=$1 AND turn_seq=$2)`, id, target).Scan(&exists); err != nil || exists {
		return err
	}
	var previous, latest int64
	if err = pool.QueryRow(ctx, `SELECT greatest(coalesce((SELECT max(turn_seq) FROM session_turn_metrics WHERE session_id=$1 AND turn_seq<$2),0),coalesce((SELECT seq FROM agent_events WHERE session_id=$1 AND kind='turn_done' AND seq<$2 ORDER BY seq DESC OFFSET $3 LIMIT 1),0)),coalesce((SELECT max(seq) FROM agent_events WHERE session_id=$1 AND kind='turn_done' AND seq<$2),0)`, id, target, turnCatchUp).Scan(&previous, &latest); err != nil {
		return err
	}
	turns, err := deriveTurnRange(ctx, pool, id, now, previous, target)
	if errors.Is(err, errTurnEventBudget) && latest > previous {
		// Recovery is best effort: a busy backlog must never cost the newest turn.
		previous = latest
		turns, err = deriveTurnRange(ctx, pool, id, now, previous, target)
	}
	if err != nil {
		return err
	}
	// An idle transition may close and store the current turn before its
	// delayed turn_done carries the outcome and usage. Finalize that one current
	// row atomically; never manufacture a second zero-duration turn.
	var provisional int64
	if len(turns) > 0 && turns[0].DurationSeconds == 0 && previous > 0 {
		var raw []byte
		err = pool.QueryRow(ctx, `SELECT m.metrics FROM session_turn_metrics m JOIN agent_events e ON e.session_id=m.session_id AND e.seq=m.turn_seq WHERE m.session_id=$1 AND m.turn_seq=$2 AND e.kind='status_changed' AND e.payload->>'status'='idle'`, id, previous).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			var prior activity.Turn
			if err = json.Unmarshal(raw, &prior); err != nil {
				return err
			}
			prior.Seq = turns[0].Seq
			prior.Outcome = turns[0].Outcome
			prior.Tokens = turns[0].Tokens
			turns[0] = prior
			provisional = previous
		}
	}
	batch := &pgx.Batch{}
	for _, turn := range turns {
		if turn.Seq == 0 {
			continue
		}
		raw, err := json.Marshal(turn)
		if err != nil {
			return err
		}
		batch.Queue(`INSERT INTO session_turn_metrics(session_id,turn_seq,metrics) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, turn.Seq, raw)
	}
	if provisional > 0 {
		batch.Queue(`DELETE FROM session_turn_metrics WHERE session_id=$1 AND turn_seq=$2`, id, provisional)
	}
	return pool.SendBatch(ctx, batch).Close()
}

// StoredTurnMetric reads one completed turn by its indexed terminal sequence,
// including turns older than the bounded detail window. No event replay occurs.
func StoredTurnMetric(ctx context.Context, pool *pgxpool.Pool, id string, seq int64) (activity.Turn, error) {
	var raw []byte
	var turn activity.Turn
	if err := pool.QueryRow(ctx, `SELECT metrics FROM session_turn_metrics WHERE session_id=$1 AND turn_seq=$2`, id, seq).Scan(&raw); err != nil {
		return turn, err
	}
	err := json.Unmarshal(raw, &turn)
	return turn, err
}
