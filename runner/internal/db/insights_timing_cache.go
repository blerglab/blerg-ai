package db

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InsightActivityTimings never computes ended history on a person's request.
// Missing history is explicit and the background worker makes bounded progress.
func InsightActivityTimings(ctx context.Context, pool *pgxpool.Pool, owner string, ids []string, windows ...time.Time) ([]byte, error) {
	finishProfile := ObserveInsights(InsightsTiming)
	defer finishProfile()
	conn, release, err := insightsConnection(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer release()
	var raw []byte
	err = conn.QueryRow(ctx, `WITH RECURSIVE lineage AS (
 SELECT id AS original,id,parent_session_id FROM sessions WHERE spawning_account_id=$1 AND id=ANY($2::uuid[])
 UNION SELECT l.original,p.id,p.parent_session_id FROM lineage l JOIN sessions p ON p.id=l.parent_session_id WHERE p.spawning_account_id=$1 AND NOT EXISTS(SELECT 1 FROM session_card_links c WHERE c.session_id=l.id)
 ), membership AS (SELECT DISTINCT l.original,c.board_id||'#'||c.card_number AS key FROM lineage l JOIN session_card_links c ON c.session_id=l.id)
 SELECT coalesce(jsonb_agg(jsonb_build_object('id',s.id,'title',s.title,'parent_session_id',s.parent_session_id,'engine',coalesce(nullif(s.engine,''),'claude'),'started_at',s.started_at,'ended_at',s.ended_at,
 'native_active_time_unknown',EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=s.id AND e.kind='subagent_started'),
 'has_subagents',EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=s.id AND e.kind='subagent_started') OR EXISTS(SELECT 1 FROM sessions w WHERE w.parent_session_id=s.id AND w.spawning_account_id=$1 AND coalesce(w.delegation_via,'')<>'person'),
 'cards',coalesce((SELECT jsonb_agg(m.key ORDER BY m.key) FROM membership m WHERE m.original=s.id),'[]'::jsonb),'timing',CASE WHEN s.ended_at IS NOT NULL THEN c.timing ELSE NULL END) ORDER BY s.id),'[]'::jsonb)
 FROM sessions s LEFT JOIN session_insight_timings c ON c.session_id=s.id AND c.fingerprint_inputs->>'version'='5' WHERE s.spawning_account_id=$1 AND s.id=ANY($2::uuid[])`, owner, ids).Scan(&raw)
	if err != nil {
		return nil, err
	}
	release()
	var sessions []insightTimingSession
	if err = json.Unmarshal(raw, &sessions); err != nil {
		return nil, err
	}
	live := []string{}
	pending := 0
	for i := range sessions {
		s := &sessions[i]
		if s.End == nil {
			live = append(live, s.ID)
		} else if s.Timing == nil {
			pending++
		}
		s.Shared = len(s.Cards) > 1
		s.Timing = chartTiming(s.Timing, windows)
	}
	partial := pending > 0
	if len(live) > 0 {
		data, e := computeInsightActivityTimings(ctx, pool, owner, live, windows...)
		if e != nil {
			return nil, e
		}
		var result struct {
			Sessions []insightTimingSession
			Partial  bool
		}
		if e = json.Unmarshal(data, &result); e != nil {
			return nil, e
		}
		partial = partial || result.Partial
		values := map[string]insightTimingSession{}
		for _, s := range result.Sessions {
			values[s.ID] = s
		}
		for i := range sessions {
			if s, ok := values[sessions[i].ID]; ok {
				sessions[i] = s
			}
		}
	}
	return json.Marshal(struct {
		Sessions []insightTimingSession `json:"sessions"`
		Partial  bool                   `json:"partial"`
		Pending  int                    `json:"pending_sessions"`
	}{sessions, partial, pending})
}

// RefreshInsightTiming is a write-side materialization. A failure never undoes
// the persisted event/end; the missing row remains eligible for the backfill.
func RefreshInsightTiming(ctx context.Context, pool *pgxpool.Pool, id string) error {
	if pool == nil {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var owner string
	if err := tx.QueryRow(ctx, `SELECT coalesce(spawning_account_id,'') FROM sessions WHERE id=$1 FOR NO KEY UPDATE`, id).Scan(&owner); err != nil {
		return err
	}
	if owner == "" {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, "SET LOCAL jit=off"); err != nil {
		return err
	}
	forgetInsightTimings(pool, owner, []string{id})
	_, err = computeInsightActivityTimingsOn(ctx, pool, owner, []string{id}, tx)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Coalesce completions in a bounded queue. Event ingestion never waits for
// history computation or a session-row lock. Ended overflow remains pending in
// the database; running sessions are also computed live on Insights requests.
var insightQueue = struct {
	sync.Mutex
	jobs []insightTimingJob
	wake chan struct{}
}{wake: make(chan struct{}, 1)}

type insightTimingJob struct {
	pool *pgxpool.Pool
	id   string
}

func QueueInsightTiming(pool *pgxpool.Pool, id string) {
	if pool == nil {
		return
	}
	insightQueue.Lock()
	job := insightTimingJob{pool, id}
	for _, existing := range insightQueue.jobs {
		if existing == job {
			insightQueue.Unlock()
			return
		}
	}
	if len(insightQueue.jobs) >= 128 {
		insightQueue.Unlock()
		failurelog.Default.Event("insights_timing_queue_full", slog.LevelWarn, "operation", "insights_timing_queue_full")
		return
	}
	insightQueue.jobs = append(insightQueue.jobs, job)
	insightQueue.Unlock()
	select {
	case insightQueue.wake <- struct{}{}:
	default:
	}
}

// ProcessQueuedInsightTimings processes at most fifty completions, one at a
// time. The queue is removed before computation so a later turn can enqueue
// another refresh while this one is running.
func ProcessQueuedInsightTimings(ctx context.Context, pool *pgxpool.Pool) int {
	processed := 0
	for processed < 50 && ctx.Err() == nil {
		insightQueue.Lock()
		index := -1
		for i, job := range insightQueue.jobs {
			if job.pool == pool {
				index = i
				break
			}
		}
		if index < 0 {
			insightQueue.Unlock()
			break
		}
		job := insightQueue.jobs[index]
		copy(insightQueue.jobs[index:], insightQueue.jobs[index+1:])
		insightQueue.jobs[len(insightQueue.jobs)-1] = insightTimingJob{}
		insightQueue.jobs = insightQueue.jobs[:len(insightQueue.jobs)-1]
		insightQueue.Unlock()
		work, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := RefreshInsightTiming(work, pool, job.id)
		if err != nil {
			cancel()
			recordInsightAttempt(ctx, pool, job.id, err)
			processed++
			continue
		}
		// BLG-444: the session's timing just landed — re-check its cards' budgets now, not at
		// turn-end (which only enqueued this job and never waited for it).
		logCardTimeCrossing(CheckCardTimeCrossing(work, pool, job.id))
		cancel()
		processed++
	}
	return processed
}

func logInsightRefresh(err error) {
	if err != nil {
		failurelog.Default.Event("insights_timing_refresh", slog.LevelWarn, "operation", "insights_timing_refresh", "cause", failurelog.Cause(err))
	}
}

// BackfillInsightTimings claims at most 50 ended sessions for one owner. Row
// locks prevent competing workers from repeating the same historical batch.
func BackfillInsightTimings(ctx context.Context, pool *pgxpool.Pool, owner string) (int, error) {
	completed := 0
	for completed < 50 {
		found, err := backfillInsightTiming(ctx, pool, owner)
		if err != nil {
			return completed, err
		}
		if !found {
			break
		}
		completed++
	}
	return completed, nil
}

// Commit each completed row, so a deadline or one bad history cannot erase
// progress already made during this tick. Competing workers skip claimed rows.
func backfillInsightTiming(ctx context.Context, pool *pgxpool.Pool, owner string) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `SELECT s.id::text FROM sessions s WHERE s.spawning_account_id=$1 AND s.ended_at IS NOT NULL AND s.insight_pending AND s.insight_retry_at<=now() ORDER BY s.started_at DESC,s.id LIMIT 1 FOR NO KEY UPDATE OF s SKIP LOCKED`, owner).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "SET LOCAL jit=off"); err != nil {
		return false, err
	}
	forgetInsightTimings(pool, owner, []string{id})
	if _, err = computeInsightActivityTimingsOn(ctx, pool, owner, []string{id}, tx); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		recordInsightAttempt(ctx, pool, id, err)
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		recordInsightAttempt(ctx, pool, id, err)
		return false, err
	}
	// BLG-444: this ended session's timing just landed via backfill — re-check its cards' budgets
	// now, the other materialization point CheckCardTimeCrossing is driven from.
	logCardTimeCrossing(CheckCardTimeCrossing(ctx, pool, id))
	return true, nil
}

func recordInsightAttempt(ctx context.Context, pool *pgxpool.Pool, id string, cause error) {
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	var attempts int
	err := pool.QueryRow(work, `UPDATE sessions SET insight_attempts=insight_attempts+1,insight_retry_at=now()+interval '1 minute'*power(2,least(insight_attempts,8)),insight_pending=(insight_attempts+1<3) WHERE id=$1 RETURNING insight_attempts`, id).Scan(&attempts)
	logInsightRefresh(cause)
	logInsightRefresh(err)
	if err == nil && attempts >= 3 {
		failurelog.Default.Event("insights_timing_backfill_skipped", slog.LevelWarn, "operation", "insights_timing_backfill_skipped", "attempts", attempts)
	}
}

func forgetInsightTimings(pool *pgxpool.Pool, owner string, ids []string) {
	timingMemory.Lock()
	defer timingMemory.Unlock()
	for _, id := range ids {
		key := timingMemoryKey{pool, owner, id}
		if old, ok := timingMemory.entries[key]; ok {
			timingMemory.hours -= len(old.timing.Hours) + len(old.timing.GateRuns)
			delete(timingMemory.entries, key)
		}
	}
}

func RunInsightTimingBackfill(ctx context.Context, pool *pgxpool.Pool) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-insightQueue.wake:
			ProcessQueuedInsightTimings(ctx, pool)
		case <-tick.C:
			ProcessQueuedInsightTimings(ctx, pool)
			// Rotate owners rather than allowing one large history to monopolize work.
			rows, err := pool.Query(ctx, `SELECT DISTINCT s.spawning_account_id FROM sessions s WHERE s.spawning_account_id IS NOT NULL AND s.ended_at IS NOT NULL AND s.insight_pending AND s.insight_retry_at<=now() ORDER BY s.spawning_account_id`)
			if err != nil {
				logInsightRefresh(err)
				continue
			}
			owners := []string{}
			for rows.Next() {
				var owner string
				if err = rows.Scan(&owner); err != nil {
					break
				}
				owners = append(owners, owner)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			logInsightRefresh(err)
			for _, owner := range owners {
				if ctx.Err() != nil {
					return
				}
				batchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_, err = BackfillInsightTimings(batchCtx, pool, owner)
				cancel()
				logInsightRefresh(err)
			}
		}
	}
}
