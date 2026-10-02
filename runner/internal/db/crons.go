package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCronNotFound is returned when a cron or cron run does not exist, or (for the
// owner-scoped helpers) belongs to another account: the two are deliberately
// indistinguishable.
var ErrCronNotFound = errors.New("cron not found")

// Querier is the part of a pool or a transaction the cron queries use, so the same check can run on
// either: on the connection a transaction already holds (and under its locks) without asking the pool
// for a second one.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrCronRunOpen is returned by InsertManualCronRunGuarded when the cron already has a run that is
// claimed or held: a run-now (or a double click) must not start a second one beside it.
var ErrCronRunOpen = errors.New("cron already has an open run")

// ErrCronLimit is returned by InsertCron when the account already has the
// maximum number of crons.
var ErrCronLimit = errors.New("cron limit reached")

// Cron run statuses (cron_runs.status).
const (
	CronRunClaimed = "claimed"
	CronRunStarted = "started"
	CronRunHeld    = "held"
	CronRunSkipped = "skipped"
	CronRunFailed  = "failed"
)

// Cron is a row of crons (migration 021).
type Cron struct {
	ID                  string
	OwnerAccountID      string
	Name                string
	Enabled             bool
	Schedule            string
	Timezone            string
	Prompt              string
	Engine              string
	Model               *string
	Effort              *string
	Runtime             string
	DaemonID            *string
	BoardID             *string
	MCP                 json.RawMessage
	TokenID             string
	TokenExpiresAt      time.Time
	GraceSeconds        int
	MaxRuntimeSeconds   int
	NextRunAt           time.Time
	LastRunAt           *time.Time
	ConsecutiveFailures int
	PausedReason        *string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// CronRun is a row of cron_runs: one firing of a cron.
type CronRun struct {
	ID           string
	CronID       string
	ScheduledFor time.Time
	ClaimedAt    time.Time
	StartedAt    *time.Time
	SessionID    *string
	Status       string
	Reason       *string
	Late         bool
	Manual       bool
}

// IdempotencyKey is the key a start of this run must carry, so a retry of the
// same run (the reaper, a held retry) cannot start a second session. A manual
// run carries the manual form of the key spec 7.3 names; the run id is the
// uuid in it.
func (r CronRun) IdempotencyKey() string {
	if r.Manual {
		return fmt.Sprintf("cron:%s:manual:%s", r.CronID, r.ID)
	}
	return fmt.Sprintf("cron:%s:%s", r.CronID, r.ID)
}

const cronColumns = `id::text, owner_account_id, name, enabled, schedule, timezone, prompt, engine,
	model, effort, runtime, daemon_id::text, board_id, mcp, token_id, token_expires_at,
	grace_seconds, max_runtime_seconds, next_run_at, last_run_at, consecutive_failures,
	paused_reason, created_at, updated_at`

func scanCron(row pgx.Row) (*Cron, error) {
	var c Cron
	var mcp []byte
	if err := row.Scan(&c.ID, &c.OwnerAccountID, &c.Name, &c.Enabled, &c.Schedule, &c.Timezone, &c.Prompt,
		&c.Engine, &c.Model, &c.Effort, &c.Runtime, &c.DaemonID, &c.BoardID, &mcp, &c.TokenID,
		&c.TokenExpiresAt, &c.GraceSeconds, &c.MaxRuntimeSeconds, &c.NextRunAt, &c.LastRunAt,
		&c.ConsecutiveFailures, &c.PausedReason, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.MCP = json.RawMessage(mcp)
	return &c, nil
}

const cronRunColumns = `id::text, cron_id::text, scheduled_for, claimed_at, started_at, session_id::text,
	status, reason, late, manual`

func scanCronRun(row pgx.Row) (*CronRun, error) {
	var r CronRun
	if err := row.Scan(&r.ID, &r.CronID, &r.ScheduledFor, &r.ClaimedAt, &r.StartedAt, &r.SessionID,
		&r.Status, &r.Reason, &r.Late, &r.Manual); err != nil {
		return nil, err
	}
	return &r, nil
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// InsertCron creates a cron, refusing (ErrCronLimit) when the owner already has
// maxPerAccount. The check and the insert share a per-account advisory lock, so
// concurrent creates cannot overshoot the limit. The caller supplies NextRunAt
// and the token id and expiry.
func InsertCron(ctx context.Context, conn *pgxpool.Pool, c Cron, maxPerAccount int) (*Cron, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; the original error is what gets returned

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "blerg_cron_limit:"+c.OwnerAccountID); err != nil {
		return nil, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM crons WHERE owner_account_id = $1`, c.OwnerAccountID).Scan(&n); err != nil {
		return nil, err
	}
	if n >= maxPerAccount {
		return nil, ErrCronLimit
	}
	mcp := []byte(c.MCP)
	if len(mcp) == 0 {
		mcp = []byte("[]")
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO crons (owner_account_id, name, enabled, schedule, timezone, prompt, model, effort,
		                   runtime, daemon_id, board_id, mcp, token_id, token_expires_at,
		                   grace_seconds, max_runtime_seconds, next_run_at, paused_reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::text::uuid, $11, $12::jsonb, $13, $14, $15, $16, $17, $18)
		RETURNING `+cronColumns,
		c.OwnerAccountID, c.Name, c.Enabled, c.Schedule, c.Timezone, c.Prompt, c.Model, c.Effort,
		c.Runtime, c.DaemonID, c.BoardID, mcp, c.TokenID, c.TokenExpiresAt,
		c.GraceSeconds, c.MaxRuntimeSeconds, c.NextRunAt, c.PausedReason)
	out, err := scanCron(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// GetCron reads one cron by id, whoever owns it. ErrCronNotFound when absent.
func GetCron(ctx context.Context, conn *pgxpool.Pool, id string) (*Cron, error) {
	if !uuidRe.MatchString(id) {
		return nil, ErrCronNotFound
	}
	c, err := scanCron(conn.QueryRow(ctx, `SELECT `+cronColumns+` FROM crons WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCronNotFound
	}
	return c, err
}

// GetOwnedCron is GetCron limited to one account: another account's cron is
// ErrCronNotFound.
func GetOwnedCron(ctx context.Context, conn *pgxpool.Pool, owner, id string) (*Cron, error) {
	if !uuidRe.MatchString(id) {
		return nil, ErrCronNotFound
	}
	c, err := scanCron(conn.QueryRow(ctx,
		`SELECT `+cronColumns+` FROM crons WHERE id = $1 AND owner_account_id = $2`, id, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCronNotFound
	}
	return c, err
}

// ListCrons returns an account's crons, oldest first.
func ListCrons(ctx context.Context, conn *pgxpool.Pool, owner string) ([]Cron, error) {
	rows, err := conn.Query(ctx,
		`SELECT `+cronColumns+` FROM crons WHERE owner_account_id = $1 ORDER BY created_at, id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cron
	for rows.Next() {
		c, err := scanCron(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// UpdateCron reads one owned cron under a row lock, lets mutate change it and
// writes every mutable column back. A mutate error aborts and is returned
// unchanged. Callers put the "recompute next_run_at from now" rule in mutate.
func UpdateCron(ctx context.Context, conn *pgxpool.Pool, owner, id string, mutate func(*Cron) error) (*Cron, error) {
	return UpdateCronTx(ctx, conn, owner, id, func(_ context.Context, _ pgx.Tx, c *Cron) error { return mutate(c) })
}

// HasClaimedCronRun reports whether the cron has a run in status `claimed`: a start that is in
// progress or waiting for the reaper, both of which use the cron's current token.
func HasClaimedCronRun(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, cronID string) (bool, error) {
	var has bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cron_runs WHERE cron_id = $1 AND status = 'claimed')`, cronID).Scan(&has)
	return has, err
}

// UpdateCronTx is UpdateCron whose mutate also gets the transaction, so a compare-and-set can read
// other tables (the cron's runs) under the same row lock the scheduler's claim takes.
func UpdateCronTx(ctx context.Context, conn *pgxpool.Pool, owner, id string, mutate func(context.Context, pgx.Tx, *Cron) error) (*Cron, error) {
	if !uuidRe.MatchString(id) {
		return nil, ErrCronNotFound
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; the original error is what gets returned

	c, err := scanCron(tx.QueryRow(ctx,
		`SELECT `+cronColumns+` FROM crons WHERE id = $1 AND owner_account_id = $2 FOR UPDATE`, id, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCronNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := mutate(ctx, tx, c); err != nil {
		return nil, err
	}
	mcp := []byte(c.MCP)
	if len(mcp) == 0 {
		mcp = []byte("[]")
	}
	out, err := scanCron(tx.QueryRow(ctx, `
		UPDATE crons SET name = $2, enabled = $3, schedule = $4, timezone = $5, prompt = $6, model = $7,
		       effort = $8, runtime = $9, daemon_id = $10::text::uuid, board_id = $11, mcp = $12::jsonb,
		       token_id = $13, token_expires_at = $14, grace_seconds = $15, max_runtime_seconds = $16,
		       next_run_at = $17, consecutive_failures = $18, paused_reason = $19, updated_at = now()
		 WHERE id = $1
		RETURNING `+cronColumns,
		id, c.Name, c.Enabled, c.Schedule, c.Timezone, c.Prompt, c.Model, c.Effort, c.Runtime, c.DaemonID,
		c.BoardID, mcp, c.TokenID, c.TokenExpiresAt, c.GraceSeconds, c.MaxRuntimeSeconds, c.NextRunAt,
		c.ConsecutiveFailures, c.PausedReason))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteCron removes an owned cron (its runs cascade) and returns the deleted
// row, so the caller can revoke its token and stop its sessions. The row's CURRENT token is
// recorded as owed a revocation in the same transaction that deletes it (the delete holds the row's
// lock, and a renewal swaps the token under the same lock), so a token minted by a renewal that
// landed after the caller read the row is never orphaned for its full lifetime.
func DeleteCron(ctx context.Context, conn *pgxpool.Pool, owner, id string) (*Cron, error) {
	if !uuidRe.MatchString(id) {
		return nil, ErrCronNotFound
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; the original error is what gets returned
	c, err := scanCron(tx.QueryRow(ctx,
		`DELETE FROM crons WHERE id = $1 AND owner_account_id = $2 RETURNING `+cronColumns, id, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCronNotFound
	}
	if err != nil {
		return nil, err
	}
	if c.TokenID != "" {
		if _, err := tx.Exec(ctx,
			`INSERT INTO cron_token_revocations (token_id, account_id) VALUES ($1, $2) ON CONFLICT (token_id) DO NOTHING`,
			c.TokenID, c.OwnerAccountID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// ClaimPlan is what ClaimDueCrons's callback decides for one due cron: the next
// slot strictly after now, or a reason to pause it instead of firing (an
// unparseable schedule, an expired token).
type ClaimPlan struct {
	Next        time.Time
	PauseReason string
}

// CronClaim is one claimed slot: the cron (with next_run_at and last_run_at as
// just written) and the `claimed` run row created for it.
type CronClaim struct {
	Cron Cron
	Run  CronRun
}

// lateAfter is how long after its slot a claim must land to be marked late.
const lateAfter = time.Minute

// ClaimDueCrons is the scheduler's claim step (spec 7.3, step 1). In ONE
// transaction it locks up to limit due crons (enabled, not paused, next_run_at
// <= now) with FOR UPDATE SKIP LOCKED, advances each to the slot plan returns
// (so a cron that missed many slots yields one run) and inserts a `claimed`
// run, `late` when now is more than a minute past the slot. `now` is the
// caller's clock, not the database's, so the scheduler is testable.
func ClaimDueCrons(ctx context.Context, conn *pgxpool.Pool, now time.Time, limit int, plan func(*Cron) ClaimPlan) ([]CronClaim, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; the original error is what gets returned

	rows, err := tx.Query(ctx, `
		SELECT `+cronColumns+` FROM crons
		 WHERE enabled AND paused_reason IS NULL AND next_run_at <= $1
		 ORDER BY next_run_at, id
		 LIMIT $2
		 FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, err
	}
	var due []Cron
	for rows.Next() {
		c, err := scanCron(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, *c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var claims []CronClaim
	for i := range due {
		c := due[i]
		p := plan(&c)
		if p.PauseReason != "" {
			if _, err := tx.Exec(ctx,
				`UPDATE crons SET paused_reason = $2, updated_at = now() WHERE id = $1`, c.ID, p.PauseReason); err != nil {
				return nil, err
			}
			continue
		}
		if p.Next.IsZero() || !p.Next.After(now) {
			return nil, fmt.Errorf("claim plan for cron %s: next slot %v is not after now", c.ID, p.Next)
		}
		slot := c.NextRunAt
		if _, err := tx.Exec(ctx,
			`UPDATE crons SET next_run_at = $2, last_run_at = $3, updated_at = now() WHERE id = $1`,
			c.ID, p.Next, now); err != nil {
			return nil, err
		}
		run, err := scanCronRun(tx.QueryRow(ctx, `
			INSERT INTO cron_runs (cron_id, scheduled_for, claimed_at, status, late)
			VALUES ($1, $2, $3, 'claimed', $4)
			RETURNING `+cronRunColumns, c.ID, slot, now, now.Sub(slot) > lateAfter))
		if err != nil {
			return nil, err
		}
		c.NextRunAt = p.Next
		c.LastRunAt = &now
		claims = append(claims, CronClaim{Cron: c, Run: *run})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claims, nil
}

// InsertManualCronRun records a run-now: a `claimed` run flagged manual,
// scheduled for now.
func InsertManualCronRun(ctx context.Context, conn *pgxpool.Pool, cronID string, now time.Time) (*CronRun, error) {
	return scanCronRun(conn.QueryRow(ctx, `
		INSERT INTO cron_runs (cron_id, scheduled_for, claimed_at, status, manual)
		VALUES ($1, $2, $2, 'claimed', true)
		RETURNING `+cronRunColumns, cronID, now))
}

// InsertManualCronRunGuarded records a run-now like InsertManualCronRun, but atomically refuses
// (ErrCronRunOpen) while the cron has another run that is claimed or held, under the same row lock
// the scheduler's claim and a token swap take, so two clicks race to exactly one run. The run
// carries a lease until leaseUntil: the reaper leaves it alone while a start on the caller's own
// goroutine may still be running. busy, when set, runs under the lock, ON THE TRANSACTION (it is given
// the transaction as its Querier: a callback that asked the pool for a connection would wait on the
// very transaction that is waiting for it), and can refuse too.
func InsertManualCronRunGuarded(ctx context.Context, conn *pgxpool.Pool, cronID string, now, leaseUntil time.Time,
	busy func(context.Context, Querier) (bool, error)) (*CronRun, error) {
	if !uuidRe.MatchString(cronID) {
		return nil, ErrCronNotFound
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; the original error is what gets returned

	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM crons WHERE id = $1 FOR UPDATE`, cronID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCronNotFound
		}
		return nil, err
	}
	var open bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM cron_runs WHERE cron_id = $1 AND status IN ('claimed','held'))`, cronID).Scan(&open); err != nil {
		return nil, err
	}
	if open {
		return nil, ErrCronRunOpen
	}
	if busy != nil { // e.g. the previous run's session is still active
		b, err := busy(ctx, tx)
		if err != nil {
			return nil, err
		}
		if b {
			return nil, ErrCronRunOpen
		}
	}
	run, err := scanCronRun(tx.QueryRow(ctx, `
		INSERT INTO cron_runs (cron_id, scheduled_for, claimed_at, status, manual, lease_until)
		VALUES ($1, $2, $2, 'claimed', true, $3)
		RETURNING `+cronRunColumns, cronID, now, leaseUntil))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return run, nil
}

// GetCronRun reads one run. ErrCronNotFound when absent.
func GetCronRun(ctx context.Context, conn *pgxpool.Pool, id string) (*CronRun, error) {
	if !uuidRe.MatchString(id) {
		return nil, ErrCronNotFound
	}
	r, err := scanCronRun(conn.QueryRow(ctx, `SELECT `+cronRunColumns+` FROM cron_runs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCronNotFound
	}
	return r, err
}

// ListCronRuns returns a cron's runs, newest slot first, capped at limit.
func ListCronRuns(ctx context.Context, conn *pgxpool.Pool, cronID string, limit int) ([]CronRun, error) {
	if !uuidRe.MatchString(cronID) {
		return nil, nil
	}
	return queryCronRuns(ctx, conn, `SELECT `+cronRunColumns+` FROM cron_runs WHERE cron_id = $1
		ORDER BY scheduled_for DESC, claimed_at DESC LIMIT $2`, cronID, limit)
}

// ListCronRunsByStatus returns every run in one status, oldest slot first.
func ListCronRunsByStatus(ctx context.Context, conn *pgxpool.Pool, status string) ([]CronRun, error) {
	return queryCronRuns(ctx, conn, `SELECT `+cronRunColumns+` FROM cron_runs WHERE status = $1
		ORDER BY scheduled_for, id`, status)
}

// ListClaimedCronRunsBetween returns `claimed` runs with after < claimed_at <=
// notAfter: the reaper's candidates (older than a minute, younger than 23 hours).
func ListClaimedCronRunsBetween(ctx context.Context, conn *pgxpool.Pool, after, notAfter time.Time) ([]CronRun, error) {
	return queryCronRuns(ctx, conn, `SELECT `+cronRunColumns+` FROM cron_runs
		WHERE status = 'claimed' AND claimed_at > $1 AND claimed_at <= $2
		ORDER BY claimed_at, id`, after, notAfter)
}

// ListReapableClaimedCronRuns is ListClaimedCronRunsBetween without the runs whose lease (a start
// in progress on a request goroutine) has not lapsed at now.
func ListReapableClaimedCronRuns(ctx context.Context, conn *pgxpool.Pool, after, notAfter, now time.Time) ([]CronRun, error) {
	return queryCronRuns(ctx, conn, `SELECT `+cronRunColumns+` FROM cron_runs
		WHERE status = 'claimed' AND claimed_at > $1 AND claimed_at <= $2
		  AND (lease_until IS NULL OR lease_until <= $3)
		ORDER BY claimed_at, id`, after, notAfter, now)
}

func queryCronRuns(ctx context.Context, conn *pgxpool.Pool, sql string, args ...any) ([]CronRun, error) {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronRun
	for rows.Next() {
		r, err := scanCronRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// FailClaimedCronRunsBefore marks `claimed` runs claimed at or before the
// cutoff as failed: a claim that old was never started and is too old to
// start now. It returns how many.
func FailClaimedCronRunsBefore(ctx context.Context, conn *pgxpool.Pool, cutoff time.Time, reason string) (int64, error) {
	tag, err := conn.Exec(ctx, `
		UPDATE cron_runs SET status = 'failed', reason = $2
		 WHERE status = 'claimed' AND claimed_at <= $1`, cutoff, reason)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// BumpCronRunRedrives counts one more reaper re-drive of a run and returns the new count.
func BumpCronRunRedrives(ctx context.Context, conn *pgxpool.Pool, runID string) (int, error) {
	if !uuidRe.MatchString(runID) {
		return 0, ErrCronNotFound
	}
	var n int
	err := conn.QueryRow(ctx, `UPDATE cron_runs SET redrives = redrives + 1 WHERE id = $1 RETURNING redrives`, runID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrCronNotFound
	}
	return n, err
}

// SessionIsActive reports whether a session exists and has not ended, on q (so it can run under a
// transaction's locks). A missing session is not active.
func SessionIsActive(ctx context.Context, q Querier, sessionID string) (bool, error) {
	var active bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sessions WHERE id = $1 AND status NOT IN `+terminalSessionStatusesSQL+`)`,
		sessionID).Scan(&active)
	return active, err
}

// TransitionCronRun moves a run to a new status only if it is currently in one
// of the from statuses (a compare-and-set, so a late writer cannot resurrect a
// finished run). reason and sessionID are stored when non-empty; reason is
// cleared otherwise. started_at is set (to now) when the new status is
// `started`. It reports whether the transition applied.
func TransitionCronRun(ctx context.Context, conn *pgxpool.Pool, id string, from []string, to, reason, sessionID string, now time.Time) (bool, error) {
	if !uuidRe.MatchString(id) {
		return false, nil
	}
	tag, err := conn.Exec(ctx, `
		UPDATE cron_runs
		   SET status = $3,
		       reason = NULLIF($4::text, ''),
		       session_id = COALESCE(NULLIF($5::text, '')::uuid, session_id),
		       started_at = CASE WHEN $3::text = 'started' THEN $6::timestamptz ELSE started_at END
		 WHERE id = $1 AND status = ANY($2)`,
		id, from, to, reason, sessionID, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// HasEarlierOpenCronRun reports whether the cron has another run, scheduled
// before scheduledFor, still `claimed` or `held`. An open MANUAL run counts whatever its slot time
// is: a run-now stamps "now" as its slot, which can be later than a scheduled slot that is claimed
// just after it, and the two must not run at once.
func HasEarlierOpenCronRun(ctx context.Context, conn Querier, cronID, runID string, scheduledFor time.Time) (bool, error) {
	var open bool
	err := conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM cron_runs
		                WHERE cron_id = $1 AND id <> $2 AND status IN ('claimed','held')
		                  AND (scheduled_for < $3 OR manual))`,
		cronID, runID, scheduledFor).Scan(&open)
	return open, err
}

// LatestStartedCronRun returns the cron's most recent `started` run that has a
// session, other than exceptRunID (may be empty), or nil.
func LatestStartedCronRun(ctx context.Context, conn Querier, cronID, exceptRunID string) (*CronRun, error) {
	except := exceptRunID
	if except == "" {
		except = "00000000-0000-0000-0000-000000000000"
	}
	r, err := scanCronRun(conn.QueryRow(ctx, `
		SELECT `+cronRunColumns+` FROM cron_runs
		 WHERE cron_id = $1 AND id <> $2 AND status = 'started' AND session_id IS NOT NULL
		 ORDER BY scheduled_for DESC, claimed_at DESC LIMIT 1`, cronID, except))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// RecordCronFailure counts one more failed run for the cron. When the count
// reaches threshold and the cron is not already paused it sets paused_reason
// and reports paused=true (once).
func RecordCronFailure(ctx context.Context, conn *pgxpool.Pool, cronID string, threshold int, pauseReason string) (failures int, paused bool, err error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; the original error is what gets returned

	var already *string
	if err := tx.QueryRow(ctx, `SELECT paused_reason FROM crons WHERE id = $1 FOR UPDATE`, cronID).Scan(&already); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, ErrCronNotFound
		}
		return 0, false, err
	}
	if err := tx.QueryRow(ctx,
		`UPDATE crons SET consecutive_failures = consecutive_failures + 1, updated_at = now()
		  WHERE id = $1 RETURNING consecutive_failures`, cronID).Scan(&failures); err != nil {
		return 0, false, err
	}
	if already == nil && failures >= threshold {
		if _, err := tx.Exec(ctx, `UPDATE crons SET paused_reason = $2 WHERE id = $1`, cronID, pauseReason); err != nil {
			return 0, false, err
		}
		paused = true
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, err
	}
	return failures, paused, nil
}

// ResetCronFailures clears the consecutive-failure count after a good run.
func ResetCronFailures(ctx context.Context, conn *pgxpool.Pool, cronID string) error {
	_, err := conn.Exec(ctx,
		`UPDATE crons SET consecutive_failures = 0 WHERE id = $1 AND consecutive_failures <> 0`, cronID)
	return err
}

// PauseCron sets paused_reason on a cron that is not already paused. It reports
// whether it paused it (false: missing or already paused).
func PauseCron(ctx context.Context, conn *pgxpool.Pool, cronID, reason string) (bool, error) {
	tag, err := conn.Exec(ctx,
		`UPDATE crons SET paused_reason = $2, updated_at = now() WHERE id = $1 AND paused_reason IS NULL`,
		cronID, reason)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// PauseCronForToken is PauseCron as a compare-and-set on the token the caller checked: a cron
// whose token was renewed meanwhile is left alone, so a stale "this token is revoked" verdict
// cannot pause a cron that already holds a live new one.
func PauseCronForToken(ctx context.Context, conn *pgxpool.Pool, cronID, tokenID, reason string) (bool, error) {
	tag, err := conn.Exec(ctx,
		`UPDATE crons SET paused_reason = $3, updated_at = now()
		  WHERE id = $1 AND token_id = $2 AND paused_reason IS NULL`,
		cronID, tokenID, reason)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// TokenRevocation is a cron token whose revocation at core is still owed.
type TokenRevocation struct {
	TokenID   string
	AccountID string
}

// EnqueueTokenRevocation records that a token must be revoked at core. Idempotent.
func EnqueueTokenRevocation(ctx context.Context, conn *pgxpool.Pool, accountID, tokenID string) error {
	_, err := conn.Exec(ctx,
		`INSERT INTO cron_token_revocations (token_id, account_id) VALUES ($1, $2) ON CONFLICT (token_id) DO NOTHING`,
		tokenID, accountID)
	return err
}

// ListTokenRevocations returns up to limit owed revocations, oldest first, backed off or not.
func ListTokenRevocations(ctx context.Context, conn *pgxpool.Pool, limit int) ([]TokenRevocation, error) {
	return queryTokenRevocations(ctx, conn, `SELECT token_id, account_id FROM cron_token_revocations ORDER BY created_at, token_id LIMIT $1`, limit)
}

// ListDueTokenRevocations returns up to limit owed revocations whose retry time has come, the ones
// that have waited longest first. A row core keeps refusing is pushed back by
// RecordTokenRevocationFailure, so one poisoned row cannot hold the head of the queue.
func ListDueTokenRevocations(ctx context.Context, conn *pgxpool.Pool, now time.Time, limit int) ([]TokenRevocation, error) {
	return queryTokenRevocations(ctx, conn, `SELECT token_id, account_id FROM cron_token_revocations
		WHERE next_attempt_at <= $1 ORDER BY next_attempt_at, created_at, token_id LIMIT $2`, now, limit)
}

func queryTokenRevocations(ctx context.Context, conn *pgxpool.Pool, sql string, args ...any) ([]TokenRevocation, error) {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenRevocation
	for rows.Next() {
		var r TokenRevocation
		if err := rows.Scan(&r.TokenID, &r.AccountID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// tokenRevocationBackoff is how long a revocation waits after its nth failure: 30 s, doubling, at
// most an hour.
func tokenRevocationBackoff(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempts && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// RecordTokenRevocationFailure notes that core did not acknowledge a revocation: the retry is pushed
// back by an increasing delay, and once the row has failed maxAttempts times it is dropped (dropped
// = true; the caller says so in its log). The token then lives until it expires on its own.
func RecordTokenRevocationFailure(ctx context.Context, conn *pgxpool.Pool, tokenID string, now time.Time, maxAttempts int) (attempts int, dropped bool, err error) {
	if err := conn.QueryRow(ctx, `UPDATE cron_token_revocations SET attempts = attempts + 1 WHERE token_id = $1 RETURNING attempts`,
		tokenID).Scan(&attempts); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil // already gone
		}
		return 0, false, err
	}
	if attempts >= maxAttempts {
		_, err := conn.Exec(ctx, `DELETE FROM cron_token_revocations WHERE token_id = $1`, tokenID)
		return attempts, true, err
	}
	_, err = conn.Exec(ctx, `UPDATE cron_token_revocations SET next_attempt_at = $2 WHERE token_id = $1`,
		tokenID, now.Add(tokenRevocationBackoff(attempts)))
	return attempts, false, err
}

// DeleteTokenRevocation removes a revocation core has acknowledged.
func DeleteTokenRevocation(ctx context.Context, conn *pgxpool.Pool, tokenID string) error {
	_, err := conn.Exec(ctx, `DELETE FROM cron_token_revocations WHERE token_id = $1`, tokenID)
	return err
}

// PruneCronRuns deletes runs claimed before the cutoff (retention: 90 days).
func PruneCronRuns(ctx context.Context, conn *pgxpool.Pool, cutoff time.Time) (int64, error) {
	tag, err := conn.Exec(ctx, `DELETE FROM cron_runs WHERE claimed_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GetIdempotencyKey reads the runner_idempotency row for (scope, key), or nil.
// The scheduler's reaper uses it to tell a crash between claim and start (no
// row, or a row with no session) from a crash after the start (a session).
func GetIdempotencyKey(ctx context.Context, conn *pgxpool.Pool, scope, key string) (*IdempotencyRow, error) {
	var r IdempotencyRow
	err := conn.QueryRow(ctx, `
		SELECT scope, key, request_hash, session_id::text, created_at
		  FROM runner_idempotency WHERE scope = $1 AND key = $2`, scope, key).
		Scan(&r.Scope, &r.Key, &r.RequestHash, &r.SessionID, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
