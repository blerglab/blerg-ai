package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StandingAgent is bound to exactly one column (a column may have several
// agents) and is triggered when a card enters it. See the design spec's
// "Standing column agents" section.
type StandingAgent struct {
	ID             string `json:"id"`
	BoardID        string `json:"board_id"`
	ColumnID       string `json:"column_id"`
	Name           string `json:"name"`
	Runner         string `json:"runner"`
	Prompt         string `json:"prompt"`
	TokenLineageID string `json:"token_lineage_id"`
	SessionMode    string `json:"session_mode"` // per_card | persistent
	Enabled        bool   `json:"enabled"`
	CreatedAt      string `json:"created_at"`
}

const standingAgentCols = `id, board_id, column_id, name, runner, prompt, token_lineage_id,
	session_mode, enabled, to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`

func scanStandingAgent(row pgx.Row) (StandingAgent, error) {
	var s StandingAgent
	err := row.Scan(&s.ID, &s.BoardID, &s.ColumnID, &s.Name, &s.Runner, &s.Prompt,
		&s.TokenLineageID, &s.SessionMode, &s.Enabled, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

// CreateStandingAgent mints the agent a fresh token lineage (referenced, not
// a token row, so credential rotation cannot orphan it) and inserts the row.
func CreateStandingAgent(ctx context.Context, pool *pgxpool.Pool, boardID, columnID, name, runnerName, prompt, sessionMode string) (StandingAgent, error) {
	if sessionMode == "" {
		sessionMode = "per_card"
	}
	if sessionMode != "per_card" && sessionMode != "persistent" {
		return StandingAgent{}, fmt.Errorf("invalid session_mode %q (per_card|persistent)", sessionMode)
	}
	if name == "" {
		return StandingAgent{}, fmt.Errorf("name required")
	}
	if prompt == "" {
		return StandingAgent{}, fmt.Errorf("prompt required")
	}
	return scanStandingAgent(pool.QueryRow(ctx, `
		INSERT INTO standing_agents (board_id, column_id, name, runner, prompt, token_lineage_id, session_mode)
		VALUES ($1,$2,$3,$4,$5, gen_random_uuid(), $6)
		RETURNING `+standingAgentCols,
		boardID, columnID, name, runnerName, prompt, sessionMode))
}

func GetStandingAgent(ctx context.Context, pool *pgxpool.Pool, id string) (StandingAgent, error) {
	return scanStandingAgent(pool.QueryRow(ctx,
		`SELECT `+standingAgentCols+` FROM standing_agents WHERE id = $1`, id))
}

func ListStandingAgents(ctx context.Context, pool *pgxpool.Pool, boardID string) ([]StandingAgent, error) {
	rows, err := pool.Query(ctx,
		`SELECT `+standingAgentCols+` FROM standing_agents WHERE board_id = $1 ORDER BY created_at`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StandingAgent
	for rows.Next() {
		s, err := scanStandingAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StandingAgentParams patches a standing agent; nil fields are unchanged.
type StandingAgentParams struct {
	Name        *string `json:"name"`
	Prompt      *string `json:"prompt"`
	SessionMode *string `json:"session_mode"`
	Enabled     *bool   `json:"enabled"`
}

func UpdateStandingAgent(ctx context.Context, pool *pgxpool.Pool, id string, p StandingAgentParams) (StandingAgent, error) {
	if p.SessionMode != nil && *p.SessionMode != "per_card" && *p.SessionMode != "persistent" {
		return StandingAgent{}, fmt.Errorf("invalid session_mode %q (per_card|persistent)", *p.SessionMode)
	}
	set, args := []string{}, []any{}
	add := func(col string, v any) {
		args = append(args, v)
		set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if p.Name != nil {
		add("name", *p.Name)
	}
	if p.Prompt != nil {
		add("prompt", *p.Prompt)
	}
	if p.SessionMode != nil {
		add("session_mode", *p.SessionMode)
	}
	if p.Enabled != nil {
		add("enabled", *p.Enabled)
	}
	if len(set) == 0 {
		return GetStandingAgent(ctx, pool, id)
	}
	args = append(args, id)
	return scanStandingAgent(pool.QueryRow(ctx,
		`UPDATE standing_agents SET `+joinSet(set)+fmt.Sprintf(` WHERE id = $%d RETURNING `, len(args))+standingAgentCols,
		args...))
}

func DeleteStandingAgent(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM standing_agents WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// enqueueStandingAgents runs inside the caller's card-mutation transaction so
// the enqueue can never be lost relative to the write that triggered it (nor
// enqueued without it — a rollback undoes both together). It fires for every
// enabled agent bound to the destination column, skipping an agent whose own
// lineage produced the triggering write — the re-entrancy guard: without it,
// an agent that moves its own cards would re-trigger itself forever. A nil
// actorTokenID (human or service writes) never matches any lineage, so it
// always enqueues. The partial unique index on (standing_agent_id, card_id)
// WHERE state IN ('pending','running') gives coalescing for free.
func enqueueStandingAgents(ctx context.Context, tx pgx.Tx, columnID, cardID string, actorTokenID *string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO standing_agent_queue (standing_agent_id, card_id)
		SELECT sa.id, $1
		FROM standing_agents sa
		WHERE sa.column_id = $2 AND sa.enabled
		  AND NOT EXISTS (
		    SELECT 1 FROM tokens t
		    WHERE t.id = $3 AND t.lineage_id = sa.token_lineage_id
		  )
		ON CONFLICT (standing_agent_id, card_id) WHERE state IN ('pending','running') DO NOTHING`,
		cardID, columnID, actorTokenID)
	return err
}

// ── queue drain ──────────────────────────────────────────────────────────────

type QueueItem struct {
	ID              int64
	StandingAgentID string
	CardID          string
	Attempts        int
}

// runningLease bounds how long a claimed item may sit at state='running'
// before ReapStaleStandingQueue treats the worker that claimed it as dead and
// makes it eligible for reclaim — the restart-durability guarantee: a crash
// mid-processing does not strand the card forever.
const runningLease = 5 * time.Minute

// ClaimStandingQueueItem claims the oldest due pending item (SKIP LOCKED, so
// concurrent workers never double-process a row) and marks it running.
func ClaimStandingQueueItem(ctx context.Context, pool *pgxpool.Pool) (QueueItem, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return QueueItem{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	var it QueueItem
	err = tx.QueryRow(ctx, `
		SELECT id, standing_agent_id, card_id, attempts FROM standing_agent_queue
		WHERE state = 'pending' AND next_attempt_at <= now()
		ORDER BY enqueued_at
		FOR UPDATE SKIP LOCKED LIMIT 1`).
		Scan(&it.ID, &it.StandingAgentID, &it.CardID, &it.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return QueueItem{}, false, nil
	}
	if err != nil {
		return QueueItem{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE standing_agent_queue SET state = 'running', next_attempt_at = now() + $2
		WHERE id = $1`, it.ID, runningLease.String()); err != nil {
		return QueueItem{}, false, err
	}
	return it, true, tx.Commit(ctx)
}

// ReapStaleStandingQueue reclaims 'running' items whose lease has expired —
// the worker that claimed them crashed or the process restarted mid-flight —
// so they become visible to ClaimStandingQueueItem again.
func ReapStaleStandingQueue(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		UPDATE standing_agent_queue SET state = 'pending'
		WHERE state = 'running' AND next_attempt_at <= now()`)
	return err
}

func CompleteStandingQueueItem(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	_, err := pool.Exec(ctx,
		`UPDATE standing_agent_queue SET state = 'done' WHERE id = $1`, id)
	return err
}

// standingMaxAttempts bounds retries: past this, a queue item stops
// retrying and settles at 'failed' rather than backing off forever.
const standingMaxAttempts = 6

func standingBackoff(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 0; i < attempts; i++ {
		d *= 2
		if d > 30*time.Minute {
			return 30 * time.Minute
		}
	}
	return d
}

// FailStandingQueueItem records a failed attempt: backs off and retries, or
// past standingMaxAttempts settles at 'failed'. Returns whether the item is
// now permanently failed (so the caller can decide whether to surface it).
func FailStandingQueueItem(ctx context.Context, pool *pgxpool.Pool, id int64, priorAttempts int, errMsg string) (permanent bool, err error) {
	attempts := priorAttempts + 1
	if attempts >= standingMaxAttempts {
		_, err = pool.Exec(ctx, `
			UPDATE standing_agent_queue SET state = 'failed', attempts = $2, last_error = $3
			WHERE id = $1`, id, attempts, errMsg)
		return true, err
	}
	_, err = pool.Exec(ctx, `
		UPDATE standing_agent_queue SET state = 'pending', attempts = $2,
			next_attempt_at = now() + $3, last_error = $4
		WHERE id = $1`, id, attempts, standingBackoff(attempts).String(), errMsg)
	return false, err
}
