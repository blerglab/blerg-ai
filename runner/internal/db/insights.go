package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The loaders below feed the Insights page and the Prometheus endpoint. They only read; owner "" means every
// session, otherwise only sessions started by that account.

// InsightSession is one session as the aggregates need it.
type InsightSession struct {
	ID        string
	Title     string
	Runtime   string
	Kind      string
	Status    string
	EndReason string
	Account   string // spawning account id
	Private   bool
	StartedAt time.Time
	EndedAt   *time.Time
	CronID    string
}

// ListInsightSessions returns the sessions alive at any point in [from, to): started before its end and not
// ended before its start. Callers count "started" and "ended" themselves by date.
func ListInsightSessions(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, owner string) ([]InsightSession, error) {
	rows, err := pool.Query(ctx, `
		SELECT id::text, COALESCE(title, ''), COALESCE(runtime, ''), kind, status,
		       COALESCE(NULLIF(end_reason, ''), ''), COALESCE(spawning_account_id, ''), private,
		       started_at, ended_at, COALESCE(cron_id::text, '')
		  FROM sessions
		 WHERE started_at < $2 AND (ended_at IS NULL OR ended_at >= $1)
		   AND ($3 = '' OR spawning_account_id = $3)
		 ORDER BY started_at`, from, to, owner)
	if err != nil {
		return nil, fmt.Errorf("list insight sessions: %w", err)
	}
	defer rows.Close()
	var out []InsightSession
	for rows.Next() {
		var s InsightSession
		if err := rows.Scan(&s.ID, &s.Title, &s.Runtime, &s.Kind, &s.Status, &s.EndReason, &s.Account,
			&s.Private, &s.StartedAt, &s.EndedAt, &s.CronID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetInsightSessions returns the sessions with these ids, whatever their dates.
func GetInsightSessions(ctx context.Context, pool *pgxpool.Pool, ids []string) ([]InsightSession, error) {
	rows, err := pool.Query(ctx, `
		SELECT id::text, COALESCE(title, ''), COALESCE(runtime, ''), kind, status,
		       COALESCE(NULLIF(end_reason, ''), ''), COALESCE(spawning_account_id, ''), private,
		       started_at, ended_at, COALESCE(cron_id::text, '')
		  FROM sessions WHERE id::text = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("get insight sessions: %w", err)
	}
	defer rows.Close()
	var out []InsightSession
	for rows.Next() {
		var s InsightSession
		if err := rows.Scan(&s.ID, &s.Title, &s.Runtime, &s.Kind, &s.Status, &s.EndReason, &s.Account,
			&s.Private, &s.StartedAt, &s.EndedAt, &s.CronID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CountSessionsByStatus counts agent sessions by runtime and status, for the metrics endpoint.
func CountSessionsByStatus(ctx context.Context, pool *pgxpool.Pool) (map[[2]string]int64, error) {
	rows, err := pool.Query(ctx, `SELECT COALESCE(runtime, ''), status, count(*) FROM sessions WHERE kind = 'agent' GROUP BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("count sessions: %w", err)
	}
	defer rows.Close()
	out := map[[2]string]int64{}
	for rows.Next() {
		var runtime, status string
		var n int64
		if err := rows.Scan(&runtime, &status, &n); err != nil {
			return nil, err
		}
		out[[2]string{runtime, status}] = n
	}
	return out, rows.Err()
}

// InsightEvent is one transcript event, with its payload as JSON text.
type InsightEvent struct {
	SessionID string
	Seq       int64
	Kind      string
	Ts        time.Time
	Payload   string
}

// ListInsightEvents returns events of the given kinds in [from, to), ordered by session and sequence.
func ListInsightEvents(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, owner string, kinds []string) ([]InsightEvent, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.session_id::text, e.seq, e.kind, e.ts, e.payload::text
		  FROM agent_events e
		  JOIN sessions s ON s.id = e.session_id
		 WHERE e.kind = ANY($1) AND e.ts >= $2 AND e.ts < $3
		   AND ($4 = '' OR s.spawning_account_id = $4)
		 ORDER BY e.session_id, e.seq`, kinds, from, to, owner)
	if err != nil {
		return nil, fmt.Errorf("list insight events: %w", err)
	}
	defer rows.Close()
	var out []InsightEvent
	for rows.Next() {
		var e InsightEvent
		if err := rows.Scan(&e.SessionID, &e.Seq, &e.Kind, &e.Ts, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// TokenRow is the turn_done usage of one session, model and UTC day.
type TokenRow struct {
	SessionID  string
	Model      string
	Day        time.Time
	Turns      int64
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// ListTokenUsage sums turn_done usage per session, model and UTC day in [from, to).
func ListTokenUsage(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, owner string) ([]TokenRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.session_id::text, COALESCE(e.payload->>'model', ''),
		       date_trunc('day', e.ts AT TIME ZONE 'UTC'),
		       count(*),
		       COALESCE(sum(CASE WHEN e.payload->'usage'->>'input_tokens' ~ '^[0-9]{1,15}$' THEN (e.payload->'usage'->>'input_tokens')::bigint END), 0),
		       COALESCE(sum(CASE WHEN e.payload->'usage'->>'output_tokens' ~ '^[0-9]{1,15}$' THEN (e.payload->'usage'->>'output_tokens')::bigint END), 0),
		       COALESCE(sum(CASE WHEN e.payload->'usage'->>'cache_read_tokens' ~ '^[0-9]{1,15}$' THEN (e.payload->'usage'->>'cache_read_tokens')::bigint END), 0),
		       COALESCE(sum(CASE WHEN e.payload->'usage'->>'cache_write_tokens' ~ '^[0-9]{1,15}$' THEN (e.payload->'usage'->>'cache_write_tokens')::bigint END), 0)
		  FROM agent_events e
		  JOIN sessions s ON s.id = e.session_id
		 WHERE e.kind = 'turn_done' AND e.ts >= $1 AND e.ts < $2
		   AND ($3 = '' OR s.spawning_account_id = $3)
		 GROUP BY 1, 2, 3`, from, to, owner)
	if err != nil {
		return nil, fmt.Errorf("list token usage: %w", err)
	}
	defer rows.Close()
	var out []TokenRow
	for rows.Next() {
		var r TokenRow
		var day time.Time
		if err := rows.Scan(&r.SessionID, &r.Model, &day, &r.Turns, &r.Input, &r.Output, &r.CacheRead, &r.CacheWrite); err != nil {
			return nil, err
		}
		r.Day = day.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// CronRunRow is one cron firing with its session's lifetime, when it had a session.
type CronRunRow struct {
	Status    string
	Late      bool
	Manual    bool
	StartedAt *time.Time
	EndedAt   *time.Time
}

// ListInsightCronRuns returns the firings claimed in [from, to) for crons owned by owner ("" = all).
func ListInsightCronRuns(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, owner string) ([]CronRunRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT r.status, r.late, r.manual, s.started_at, s.ended_at
		  FROM cron_runs r
		  JOIN crons c ON c.id = r.cron_id
		  LEFT JOIN sessions s ON s.id = r.session_id
		 WHERE r.claimed_at >= $1 AND r.claimed_at < $2
		   AND ($3 = '' OR c.owner_account_id = $3)`, from, to, owner)
	if err != nil {
		return nil, fmt.Errorf("list cron runs: %w", err)
	}
	defer rows.Close()
	var out []CronRunRow
	for rows.Next() {
		var r CronRunRow
		if err := rows.Scan(&r.Status, &r.Late, &r.Manual, &r.StartedAt, &r.EndedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ModelPrice is what a model costs per million tokens, as entered by an administrator.
type ModelPrice struct {
	Model             string    `json:"model"`
	InputPerMTok      float64   `json:"input_per_mtok"`
	OutputPerMTok     float64   `json:"output_per_mtok"`
	CacheReadPerMTok  float64   `json:"cache_read_per_mtok"`
	CacheWritePerMTok float64   `json:"cache_write_per_mtok"`
	UpdatedBy         string    `json:"updated_by"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ListModelPrices returns every stored price.
func ListModelPrices(ctx context.Context, pool *pgxpool.Pool) ([]ModelPrice, error) {
	rows, err := pool.Query(ctx, `
		SELECT model, input_per_mtok, output_per_mtok, cache_read_per_mtok, cache_write_per_mtok, updated_by, updated_at
		  FROM model_prices ORDER BY model`)
	if err != nil {
		return nil, fmt.Errorf("list model prices: %w", err)
	}
	defer rows.Close()
	out := []ModelPrice{}
	for rows.Next() {
		var p ModelPrice
		if err := rows.Scan(&p.Model, &p.InputPerMTok, &p.OutputPerMTok, &p.CacheReadPerMTok, &p.CacheWritePerMTok, &p.UpdatedBy, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertModelPrice stores or replaces a model's price. The model name is trimmed and may not be empty.
func UpsertModelPrice(ctx context.Context, pool *pgxpool.Pool, p ModelPrice) error {
	p.Model = strings.TrimSpace(p.Model)
	if p.Model == "" {
		return fmt.Errorf("model required")
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO model_prices (model, input_per_mtok, output_per_mtok, cache_read_per_mtok, cache_write_per_mtok, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (model) DO UPDATE SET input_per_mtok = EXCLUDED.input_per_mtok,
		       output_per_mtok = EXCLUDED.output_per_mtok, cache_read_per_mtok = EXCLUDED.cache_read_per_mtok,
		       cache_write_per_mtok = EXCLUDED.cache_write_per_mtok, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		p.Model, p.InputPerMTok, p.OutputPerMTok, p.CacheReadPerMTok, p.CacheWritePerMTok, p.UpdatedBy)
	return err
}

// DeleteModelPrice removes a model's price; it reports whether there was one.
func DeleteModelPrice(ctx context.Context, pool *pgxpool.Pool, model string) (bool, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM model_prices WHERE model = $1`, model)
	return tag.RowsAffected() > 0, err
}
