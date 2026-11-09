package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkspaceMetrics is limited to an owned session tree. An explicitly linked
// external run is added by its registration, never by enumerating host processes.
func WorkspaceMetrics(ctx context.Context, pool *pgxpool.Pool, id string, now time.Time, limits ...int) (activity.Workspace, error) {
	w := activity.Workspace{SessionID: id, GeneratedAt: now, Workers: []activity.Worker{}}
	rows, err := pool.Query(ctx, `WITH RECURSIVE tree AS (
 SELECT id,spawning_account_id FROM sessions WHERE id=$1
 UNION ALL SELECT s.id,s.spawning_account_id FROM sessions s JOIN tree t ON s.parent_session_id=t.id WHERE s.spawning_account_id=t.spawning_account_id
 ) SELECT s.id,CASE WHEN EXISTS(SELECT 1 FROM messages m WHERE m.session_id=s.id AND m.kind='ask' AND m.status='open' AND NOT m.no_wait AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=m.id)) OR EXISTS(SELECT 1 FROM review_requests r WHERE r.session_id=s.id AND r.state='pending') THEN 'blocked' ELSE s.status END,coalesce((SELECT e.ts FROM agent_events e WHERE e.session_id=s.id AND e.kind IN ('status_changed','turn_done') ORDER BY e.seq DESC LIMIT 1),s.started_at) FROM sessions s JOIN tree t ON s.id=t.id ORDER BY s.started_at,s.id`, id)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var worker activity.Worker
		var changed time.Time
		if err = rows.Scan(&worker.SessionID, &worker.Status, &changed); err != nil {
			rows.Close()
			return w, err
		}
		worker.AgeSeconds = max(0, now.Sub(changed).Seconds())
		w.Workers = append(w.Workers, worker)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return w, err
	}
	ids := make([]string, 0, len(w.Workers))
	positions := map[string]int{}
	for i, v := range w.Workers {
		ids = append(ids, v.SessionID)
		positions[v.SessionID] = i
	}
	detail := 100
	if len(limits) > 0 {
		detail = max(0, min(100, limits[0]))
	}
	// One round trip per data class, independent of the number of descendants.
	stored, err := pool.Query(ctx, `SELECT session_id,count(*),coalesce(sum((metrics->>'duration_seconds')::double precision),0),coalesce(sum((metrics->'timing'->'phases'->>'gates')::double precision),0),coalesce(jsonb_agg(metrics ORDER BY turn_seq) FILTER(WHERE rank<=$2),'[]'::jsonb) FROM (SELECT session_id,turn_seq,metrics,row_number() OVER(PARTITION BY session_id ORDER BY turn_seq DESC) AS rank FROM session_turn_metrics WHERE session_id=ANY($1::uuid[])) t GROUP BY session_id`, ids, detail)
	if err != nil {
		return w, err
	}
	for stored.Next() {
		var id string
		var count int
		var total, gates float64
		var raw []byte
		if err = stored.Scan(&id, &count, &total, &gates, &raw); err != nil {
			stored.Close()
			return w, err
		}
		worker := &w.Workers[positions[id]]
		worker.TurnCount = count
		if err = json.Unmarshal(raw, &worker.Turns); err != nil {
			stored.Close()
			return w, err
		}
		worker.TurnsTruncated = count > len(worker.Turns)
		if total > 0 {
			worker.GateFraction = gates / total
		}
		if len(worker.Turns) > 0 && worker.Status != "running" && worker.Turns[len(worker.Turns)-1].Outcome == "failed" {
			worker.Status = "failed"
		}
	}
	err = stored.Err()
	stored.Close()
	if err != nil {
		return w, err
	}
	notices, err := pool.Query(ctx, `SELECT c.child_id,c.turn_seq,greatest(0,extract(epoch FROM $2::timestamptz-e.ts)) FROM child_turn_notices c JOIN agent_events e ON e.session_id=c.child_id AND e.seq=c.event_seq WHERE c.child_id=ANY($1::uuid[]) AND NOT c.delivered ORDER BY c.event_seq LIMIT 100`, ids, now)
	if err != nil {
		return w, err
	}
	for notices.Next() {
		var n activity.Notice
		if err = notices.Scan(&n.SessionID, &n.TurnSeq, &n.AgeSeconds); err != nil {
			notices.Close()
			return w, err
		}
		w.Undelivered = append(w.Undelivered, n)
	}
	err = notices.Err()
	notices.Close()
	if err != nil {
		return w, err
	}
	runs, err := pool.Query(ctx, `SELECT CASE WHEN data->>'state'='running' AND updated_at<$2::timestamptz-interval '1 hour' THEN data||jsonb_build_object('state','expired','ended_at',updated_at+interval '1 hour') ELSE data END FROM workspace_metric_runs WHERE session_id=ANY($1::uuid[]) ORDER BY updated_at DESC,run_id LIMIT 3200`, ids, now)
	if err != nil {
		return w, err
	}
	for runs.Next() {
		var raw []byte
		var run activity.Run
		if err = runs.Scan(&raw); err != nil {
			runs.Close()
			return w, err
		}
		if err = json.Unmarshal(raw, &run); err != nil {
			runs.Close()
			return w, err
		}
		w.Runs = append(w.Runs, run)
	}
	err = runs.Err()
	runs.Close()
	if err != nil {
		return w, err
	}
	var owner string
	if err = pool.QueryRow(ctx, "SELECT coalesce(spawning_account_id,'') FROM sessions WHERE id=$1", id).Scan(&owner); err != nil {
		return w, err
	}
	skillReport := AvailableSkillTelemetry(ctx, pool, owner, id, time.Time{}, now)
	for i := range w.Workers {
		w.Workers[i].Skills.Unavailable = skillReport.Unavailable
	}
	for _, load := range skillReport.Loads {
		if i, ok := positions[load.SessionID]; ok {
			w.Workers[i].Skills.Loads = append(w.Workers[i].Skills.Loads, load)
		}
	}
	for _, load := range skillReport.Reporting {
		if i, ok := positions[load.SessionID]; ok {
			w.Workers[i].Skills.Reporting = append(w.Workers[i].Skills.Reporting, load)
		}
	}
	for _, e := range skillReport.Facts {
		if i, ok := positions[e.SessionID]; ok {
			w.Workers[i].Skills.Facts = append(w.Workers[i].Skills.Facts, e)
		}
	}
	for _, e := range skillReport.Applicability {
		if i, ok := positions[e.SessionID]; ok {
			w.Workers[i].Skills.Applicability = append(w.Workers[i].Skills.Applicability, e)
		}
	}
	for _, e := range skillReport.Evidence {
		if i, ok := positions[e.SessionID]; ok {
			w.Workers[i].Skills.Evidence = append(w.Workers[i].Skills.Evidence, e)
		}
	}
	for i := range w.Workers {
		w.Workers[i].Skills.Truncated = skillReport.Truncated
	}
	var machineRaw []byte
	_ = pool.QueryRow(ctx, `SELECT payload->'machine' FROM agent_events WHERE session_id=$1 AND kind='turn_work' AND payload->'machine' IS NOT NULL ORDER BY seq DESC LIMIT 1`, id).Scan(&machineRaw)
	if len(machineRaw) > 0 {
		_ = json.Unmarshal(machineRaw, &w.Machine)
	}
	return w, nil
}

var ErrMetricRunLimit = errors.New("a session can register at most 32 metric runs")

// RegisterMetricRun changes progress time only when progress advances or state
// changes. Repeated heartbeats cannot hide an unproductive background run.
func RegisterMetricRun(ctx context.Context, pool *pgxpool.Pool, session string, run activity.Run) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, session).Scan(&locked); err != nil {
		return err
	}
	var full bool
	if err = tx.QueryRow(ctx, `SELECT count(*)>=32 AND NOT EXISTS(SELECT 1 FROM workspace_metric_runs WHERE session_id=$1 AND run_id=$2) FROM workspace_metric_runs WHERE session_id=$1`, session, run.ID).Scan(&full); err != nil {
		return err
	}
	if full {
		return ErrMetricRunLimit
	}
	run.SessionID = session
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	if run.State != "running" && run.EndedAt == nil {
		end := time.Now().UTC()
		run.EndedAt = &end
	}
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO workspace_metric_runs(session_id,run_id,data) VALUES($1,$2,$3::jsonb || jsonb_build_object('progress_at',now()))
 ON CONFLICT(session_id,run_id) DO UPDATE SET data=excluded.data || jsonb_build_object('progress',greatest((excluded.data->>'progress')::bigint,(workspace_metric_runs.data->>'progress')::bigint),'started_at',workspace_metric_runs.data->'started_at','progress_at',CASE WHEN (excluded.data->>'progress')::bigint>(workspace_metric_runs.data->>'progress')::bigint OR excluded.data->>'state'<>workspace_metric_runs.data->>'state' THEN to_jsonb(now()) ELSE workspace_metric_runs.data->'progress_at' END),updated_at=now()`, session, run.ID, raw)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
