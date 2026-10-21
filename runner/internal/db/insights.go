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

// TokenRow is the token usage of one session, model and UTC day: its turns' (turn_done) and its
// subagents' (subagent_done, under the subagent's own model). Turns counts turn_done only.
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

// usageSums adds up the four token counts of the usage events selected (as e), ignoring a figure that is
// not a plain number. usageEvents selects those events: every turn_done, and the subagent_done events that
// carry usage.
const (
	usageSums = `
		       COALESCE(sum(CASE WHEN u.usage->>'input_tokens' ~ '^[0-9]{1,15}$' THEN (u.usage->>'input_tokens')::bigint END), 0),
		       COALESCE(sum(CASE WHEN u.usage->>'output_tokens' ~ '^[0-9]{1,15}$' THEN (u.usage->>'output_tokens')::bigint END), 0),
		       COALESCE(sum(CASE WHEN u.usage->>'cache_read_tokens' ~ '^[0-9]{1,15}$' THEN (u.usage->>'cache_read_tokens')::bigint END), 0),
		       COALESCE(sum(CASE WHEN u.usage->>'cache_write_tokens' ~ '^[0-9]{1,15}$' THEN (u.usage->>'cache_write_tokens')::bigint END), 0)`
	usageEvents = `(e.kind = 'turn_done' OR (e.kind = 'subagent_done' AND (e.payload ? 'usage' OR e.payload ? 'usage_by_model')))`
	// A complete, bounded per-model partition replaces its aggregate. Stored
	// agent payloads are untrusted: malformed/inconsistent maps retain only
	// the legacy aggregate, explicitly under an unknown model. The single
	// turn_marker allocates a logical turn to one model row for accounting;
	// it does not claim one complete provider request for each model.
	usageRows = ` CROSS JOIN LATERAL (
 WITH candidate AS (
   SELECT CASE WHEN jsonb_typeof(e.payload->'usage_by_model')='object'
     AND octet_length((e.payload->'usage_by_model')::text)<=16384
     THEN e.payload->'usage_by_model' ELSE '{}'::jsonb END AS parts
 ), entries AS (
   SELECT item.key AS model,item.value AS usage,
     (item.key='' OR item.key ~ '^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,127}$')
     AND jsonb_typeof(item.value)='object' AND (NOT (item.value ? 'input_tokens') OR (jsonb_typeof(item.value->'input_tokens')='number' AND item.value->>'input_tokens' ~ '^[0-9]{1,15}$')) AND (NOT (item.value ? 'output_tokens') OR (jsonb_typeof(item.value->'output_tokens')='number' AND item.value->>'output_tokens' ~ '^[0-9]{1,15}$')) AND (NOT (item.value ? 'cache_read_tokens') OR (jsonb_typeof(item.value->'cache_read_tokens')='number' AND item.value->>'cache_read_tokens' ~ '^[0-9]{1,15}$')) AND (NOT (item.value ? 'cache_write_tokens') OR (jsonb_typeof(item.value->'cache_write_tokens')='number' AND item.value->>'cache_write_tokens' ~ '^[0-9]{1,15}$')) AS valid,
     CASE WHEN item.value->>'input_tokens' ~ '^[0-9]{1,15}$' THEN (item.value->>'input_tokens')::bigint ELSE 0 END AS n0,
     CASE WHEN item.value->>'output_tokens' ~ '^[0-9]{1,15}$' THEN (item.value->>'output_tokens')::bigint ELSE 0 END AS n1,
     CASE WHEN item.value->>'cache_read_tokens' ~ '^[0-9]{1,15}$' THEN (item.value->>'cache_read_tokens')::bigint ELSE 0 END AS n2,
     CASE WHEN item.value->>'cache_write_tokens' ~ '^[0-9]{1,15}$' THEN (item.value->>'cache_write_tokens')::bigint ELSE 0 END AS n3
   FROM candidate CROSS JOIN LATERAL jsonb_each(candidate.parts) item
 ), verdict AS (
   SELECT count(*) BETWEEN 1 AND 32 AND bool_and(COALESCE(valid,false))
     AND COALESCE(sum(n0),0)=(CASE WHEN e.payload->'usage'->>'input_tokens' ~ '^[0-9]{1,15}$' THEN (e.payload->'usage'->>'input_tokens')::bigint ELSE 0 END) AND COALESCE(sum(n1),0)=(CASE WHEN e.payload->'usage'->>'output_tokens' ~ '^[0-9]{1,15}$' THEN (e.payload->'usage'->>'output_tokens')::bigint ELSE 0 END) AND COALESCE(sum(n2),0)=(CASE WHEN e.payload->'usage'->>'cache_read_tokens' ~ '^[0-9]{1,15}$' THEN (e.payload->'usage'->>'cache_read_tokens')::bigint ELSE 0 END) AND COALESCE(sum(n3),0)=(CASE WHEN e.payload->'usage'->>'cache_write_tokens' ~ '^[0-9]{1,15}$' THEN (e.payload->'usage'->>'cache_write_tokens')::bigint ELSE 0 END) AS good FROM entries
 ), resolved AS (
   SELECT COALESCE(good,false) AS good FROM verdict
 )
 SELECT item.model,item.usage,
   item.model=CASE WHEN EXISTS(SELECT 1 FROM entries WHERE model=COALESCE(e.payload->>'model',''))
     THEN COALESCE(e.payload->>'model','') ELSE (SELECT min(model) FROM entries) END AS turn_marker
 FROM entries item CROSS JOIN resolved WHERE resolved.good
 UNION ALL
 SELECT CASE WHEN e.payload ? 'usage_by_model' THEN '' ELSE COALESCE(e.payload->>'model','') END,
   e.payload->'usage',true FROM resolved WHERE NOT resolved.good
 ) u `
)

// ListTokenUsage sums turn_done and subagent_done usage per session, model and UTC day in
// [from, to). A subagent_done without usage (a background launch, the native loop's) adds nothing.
func ListTokenUsage(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, owner string) ([]TokenRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.session_id::text, u.model,
		       date_trunc('day', e.ts AT TIME ZONE 'UTC'),
		       count(*) FILTER (WHERE e.kind = 'turn_done' AND u.turn_marker), `+usageSums+`
		  FROM agent_events e
		  JOIN sessions s ON s.id = e.session_id `+usageRows+`
		 WHERE `+usageEvents+`
		   AND e.ts >= $1 AND e.ts < $2
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

// ─── one session ──────────────────────────────────────────────────────────────
// The loaders below feed GET /api/sessions/{id}/insights: one session's figures, read through the
// (session_id, seq) index of agent_events and the per-session indexes of the other tables.

// SessionTokenRow is one session's token usage under one model, either its own turns' (turn_done) or its
// subagents' (subagent_done with usage). Turns counts turn_done only.
type SessionTokenRow struct {
	Subagent   bool
	Model      string
	Turns      int64
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// ListSessionTokenUsage sums a session's usage by source (main agent or subagents) and model, under the
// same rules as ListTokenUsage.
func ListSessionTokenUsage(ctx context.Context, pool *pgxpool.Pool, sessionID string) ([]SessionTokenRow, error) {
	return ListSessionTokenUsageThrough(ctx, pool, sessionID, 1<<63-1)
}

// ListSessionTokenUsageThrough uses the same immutable event boundary as live
// snapshots, so a concurrent completion cannot be combined with its own progress.
func ListSessionTokenUsageThrough(ctx context.Context, pool *pgxpool.Pool, sessionID string, through int64) ([]SessionTokenRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.kind = 'subagent_done', u.model,
		       count(*) FILTER (WHERE e.kind = 'turn_done' AND u.turn_marker), `+usageSums+`
		  FROM agent_events e `+usageRows+`
		 WHERE e.session_id = $1 AND e.seq <= $2 AND `+usageEvents+`
		 GROUP BY 1, 2`, sessionID, through)
	if err != nil {
		return nil, fmt.Errorf("list session token usage: %w", err)
	}
	defer rows.Close()
	var out []SessionTokenRow
	for rows.Next() {
		var r SessionTokenRow
		if err := rows.Scan(&r.Subagent, &r.Model, &r.Turns, &r.Input, &r.Output, &r.CacheRead, &r.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionToolCount is how many tool calls a session made with one tool, by its main agent or by its
// subagents (a call with a parent_call_id).
type SessionToolCount struct {
	Tool     string
	Subagent bool
	Count    int
}

// SessionActivity is a session's tool calls by tool and the messages a person sent it.
type SessionActivity struct {
	ToolCalls          []SessionToolCount
	MessagesFromPerson int
}

// GetSessionActivity counts a session's tool_call events by tool and by whether a subagent made them,
// and its user_message events that a person sent: every source but "system" (text the engine or the
// runner put in the user's place, such as a subagent's completion notice), "runner" (a message sent
// through the runner contract or MCP send_message, which may come from another agent), what a board,
// a parent session or a cron wrote ("blerg-board", "session", "cron") and the decisions Blerg relays
// ("review", "proposal", "child_turn").
func GetSessionActivity(ctx context.Context, pool *pgxpool.Pool, sessionID string) (SessionActivity, error) {
	var out SessionActivity
	rows, err := pool.Query(ctx, `
		SELECT e.kind, COALESCE(e.payload->>'tool', ''), COALESCE(e.payload->>'parent_call_id', '') <> '', count(*)
		  FROM agent_events e
		 WHERE e.session_id = $1
		   AND (e.kind = 'tool_call' OR (e.kind = 'user_message' AND COALESCE(e.payload->>'source', '') NOT IN ('system', 'runner', 'blerg-board', 'session', 'cron', 'review', 'proposal', 'child_turn')))
		 GROUP BY 1, 2, 3`, sessionID)
	if err != nil {
		return out, fmt.Errorf("session activity: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var c SessionToolCount
		if err := rows.Scan(&kind, &c.Tool, &c.Subagent, &c.Count); err != nil {
			return out, err
		}
		if kind == "user_message" {
			out.MessagesFromPerson += c.Count
			continue
		}
		out.ToolCalls = append(out.ToolCalls, c)
	}
	return out, rows.Err()
}

// ListSessionTimeline returns a session's status_changed, subagent_started and subagent_done events in
// order.
func ListSessionTimeline(ctx context.Context, pool *pgxpool.Pool, sessionID string) ([]InsightEvent, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.session_id::text, e.seq, e.kind, e.ts, e.payload::text
		  FROM agent_events e
		 WHERE e.session_id = $1 AND e.kind IN ('status_changed', 'subagent_started', 'subagent_update', 'subagent_done')
		 ORDER BY e.seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session timeline: %w", err)
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

// SessionSideCounts is what a session left outside its transcript: the files it published, its MCP
// proposals by state, and its MCP tool calls by outcome.
type SessionSideCounts struct {
	Artifacts   int
	Proposals   map[string]int
	MCPOutcomes map[string]int
}

// GetSessionSideCounts counts a session's published files (origin agent; a person's uploads are not
// counted), its MCP proposals by state, and its logged MCP gateway calls by outcome, tool listings left out.
func GetSessionSideCounts(ctx context.Context, pool *pgxpool.Pool, sessionID string) (SessionSideCounts, error) {
	out := SessionSideCounts{Proposals: map[string]int{}, MCPOutcomes: map[string]int{}}
	rows, err := pool.Query(ctx, `
		SELECT 'artifact', '', count(*) FROM session_artifacts WHERE session_id = $1 AND origin = 'agent'
		UNION ALL
		SELECT 'proposal', state, count(*) FROM mcp_proposals WHERE session_id = $1 GROUP BY state
		UNION ALL
		SELECT 'mcp', outcome, count(*) FROM mcp_call_log WHERE session_id = $1 AND mode <> 'list' GROUP BY outcome`, sessionID)
	if err != nil {
		return out, fmt.Errorf("session side counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var what, key string
		var n int
		if err := rows.Scan(&what, &key, &n); err != nil {
			return out, err
		}
		switch what {
		case "artifact":
			out.Artifacts = n
		case "proposal":
			out.Proposals[key] = n
		case "mcp":
			out.MCPOutcomes[key] = n
		}
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
