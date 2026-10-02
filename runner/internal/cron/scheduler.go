package cron

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCapacity is what a Starter returns (possibly wrapped) when the run cannot
// start for lack of capacity (cluster cap, no connected daemon). The run is
// `held` and retried each tick; it does not count as a failure.
var ErrCapacity = errors.New("no capacity to start the run")

// ErrCredential is what a Starter returns (possibly wrapped) when the run's
// credentials are unusable (the personal credential fetch failed, a connection
// is missing or needs sign-in). The run is `failed`.
var ErrCredential = errors.New("credential unavailable")

// ErrInvalid marks a rejected cron definition or a request that cannot apply to
// the cron's current state; the message says why and is safe to show a user.
var ErrInvalid = errors.New("invalid cron")

// ErrAlreadyRunning is returned by RunNow while the cron's previous run is
// still held or its session still active.
var ErrAlreadyRunning = errors.New("the cron's previous run is still active")

// ErrRunOpen is returned by a token swap (a renewal) that asked to be refused while a run of the
// cron is claimed: its start is in progress, or waiting for the reaper, and uses the current token.
var ErrRunOpen = errors.New("a run of this cron is being started")

// ErrConflict is returned by a compare-and-set edit whose expectation no longer holds (the cron's
// token was swapped by someone else meanwhile).
var ErrConflict = errors.New("the cron changed underneath this request")

// ErrNotRunnable is what a Starter returns (possibly wrapped) when the cron was deleted, paused or
// disabled between the claim and the start: the run is skipped, it is neither a failure nor held.
var ErrNotRunnable = errors.New("the cron can no longer run")

// Starter starts the agent session for one run (spec 7.5). It must be
// idempotent on run.IdempotencyKey(): a repeat call for the same run returns the
// session the first call started and never a second one. The idempotency scope
// it uses is Config.IdempotencyScope (default "cron:<cron id>": stable for the
// cron's life, so a renewed token never orphans an open run's key).
//
// A capacity problem must be reported BEFORE any session row or idempotency key
// is written, as an error wrapping ErrCapacity. Unusable credentials wrap
// ErrCredential. Any other error is an ordinary failure.
type Starter interface {
	Start(ctx context.Context, c *db.Cron, run *db.CronRun) (sessionID string, err error)
}

// Sessions is what the scheduler needs to know about running sessions.
type Sessions interface {
	// SessionActive reports whether a session is still running.
	SessionActive(ctx context.Context, sessionID string) bool
	// ConcurrentActive counts the account's active cron-started sessions.
	ConcurrentActive(ctx context.Context, account string) int
}

// TxSessions is optionally implemented by a Sessions that can answer on a given connection. The
// overlap check of a run-now runs on the transaction that holds the cron's row lock, and a Sessions
// that asked the pool for its own connection there would wait on that very transaction.
type TxSessions interface {
	// SessionActiveOn is SessionActive, read through q.
	SessionActiveOn(ctx context.Context, q db.Querier, sessionID string) bool
}

// Clock is the scheduler's time source, injectable for tests.
type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Defaults and limits (spec 7.1, 7.3, 11).
const (
	DefaultTickInterval         = 30 * time.Second
	DefaultMaxCronsPerAccount   = 20
	DefaultMaxConcurrentPerAcct = 2
	DefaultFailureThreshold     = 3
	DefaultRunRetention         = 90 * 24 * time.Hour
	DefaultGraceSeconds         = 3600
	DefaultMaxRuntimeSeconds    = 1800

	claimReapAfter = time.Minute // a claim older than this is re-driven
	// maxReaperRedrives is how many times the reaper re-drives one run whose start never finished
	// before the run is failed.
	maxReaperRedrives = 3
	claimMaxAge       = 23 * time.Hour   // ...unless older than this, then failed
	pruneEvery        = time.Hour        // how often the run history is pruned
	claimBatch        = 50               // crons claimed per tick
	startTimeout      = 45 * time.Second // shorter than claimReapAfter
	startWorkers      = 4                // runs started in parallel within a tick
	maxReasonLen      = 300
	defaultLockKey    = int64(0x626c657267637231) // "blergcr1"
)

// Config configures a Scheduler.
type Config struct {
	Pool     *pgxpool.Pool
	Starter  Starter
	Sessions Sessions
	Clock    Clock // default: the system clock

	TickInterval            time.Duration // default 30s
	MaxCronsPerAccount      int           // default 20
	MaxConcurrentPerAccount int           // default 2
	FailureThreshold        int           // default 3
	RunRetention            time.Duration // default 90 days
	LockKey                 int64         // advisory lock key; default a fixed constant
	StartTimeout            time.Duration // bound on one Starter call; default 45s
	Workers                 int           // runs started in parallel; default 4
	// IdempotencyScope names the runner_idempotency scope the Starter uses for
	// a cron's runs; the reaper looks keys up there. Default "cron:<cron id>",
	// which does not change when the cron's token is renewed.
	IdempotencyScope func(*db.Cron) string
	// OnPaused is called when the scheduler pauses a cron (three failures, an
	// expired token, an unusable schedule) so the caller can revoke its token
	// and stop its sessions (spec 7.6).
	OnPaused  func(ctx context.Context, c *db.Cron, reason string)
	Logf      func(format string, args ...any) // default log.Printf
	StartedAt time.Time                        // default: the clock's time at New
}

// StableIdempotencyScope is the runner_idempotency scope of a cron's runs (spec 7.3): per cron and
// independent of its token, so renewing the token between a start and the reaper cannot make the
// reaper look in a different place than the Starter wrote.
func StableIdempotencyScope(c *db.Cron) string { return "cron:" + c.ID }

// accountStarts serialises "check the account's concurrent sessions, then start" per account in
// this process, so two crons of one account cannot both pass the check for the last slot.
//
// It is PROCESS-LOCAL: with more than one runner replica, only the scheduler that holds the
// scheduling lock (an advisory lock, one leader at a time) starts scheduled runs, so the scheduled
// path is serial; a manual run-now on another replica is not covered by this lock, and can let the
// account briefly exceed its concurrent-session limit by one. The limit is a fairness bound, not a
// security boundary; the database-side guards (the cron row lock, the idempotency key) are what
// keep a run from starting twice.
var accountStarts = &keyedLocks{m: map[string]chan struct{}{}}

type keyedLocks struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

// lock waits for the key's lock or ctx; the returned func releases it.
func (k *keyedLocks) lock(ctx context.Context, key string) (func(), error) {
	k.mu.Lock()
	ch, ok := k.m[key]
	if !ok {
		ch = make(chan struct{}, 1)
		k.m[key] = ch
	}
	k.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Scheduler fires due crons. Only one Scheduler per database schedules at a
// time (a Postgres advisory lock); the rest stand by and take over if it goes.
type Scheduler struct {
	cfg       Config
	clock     Clock
	startedAt time.Time

	tickMu    sync.Mutex // serialises Tick
	lockMu    sync.Mutex
	lockConn  *pgxpool.Conn
	lastPrune time.Time
}

// New validates cfg and returns a Scheduler. Its start-up time (the reference
// for the held-run grace window) is the clock's time now.
func New(cfg Config) (*Scheduler, error) {
	if cfg.Pool == nil || cfg.Starter == nil || cfg.Sessions == nil {
		return nil, errors.New("cron scheduler needs a pool, a starter and a sessions dependency")
	}
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = DefaultTickInterval
	}
	if cfg.MaxCronsPerAccount <= 0 {
		cfg.MaxCronsPerAccount = DefaultMaxCronsPerAccount
	}
	if cfg.MaxConcurrentPerAccount <= 0 {
		cfg.MaxConcurrentPerAccount = DefaultMaxConcurrentPerAcct
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = DefaultFailureThreshold
	}
	if cfg.RunRetention <= 0 {
		cfg.RunRetention = DefaultRunRetention
	}
	if cfg.LockKey == 0 {
		cfg.LockKey = defaultLockKey
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = startTimeout
	}
	if cfg.Workers <= 0 {
		cfg.Workers = startWorkers
	}
	if cfg.IdempotencyScope == nil {
		cfg.IdempotencyScope = StableIdempotencyScope
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	started := cfg.StartedAt
	if started.IsZero() {
		started = cfg.Clock.Now()
	}
	return &Scheduler{cfg: cfg, clock: cfg.Clock, startedAt: started}, nil
}

// Run ticks until ctx is cancelled, then releases the scheduler lock.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.TickInterval)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.cfg.Logf("cron scheduler tick: %v", err)
		}
		select {
		case <-ctx.Done():
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			s.Release(rctx)
			cancel()
			return
		case <-t.C:
		}
	}
}

// HoldsLock reports whether this scheduler currently holds the scheduling lock.
func (s *Scheduler) HoldsLock() bool {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	return s.lockConn != nil
}

// Release gives up the scheduling lock, if held, so a standby can take over.
func (s *Scheduler) Release(ctx context.Context) {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	s.releaseLocked(ctx)
}

func (s *Scheduler) releaseLocked(ctx context.Context) {
	if s.lockConn == nil {
		return
	}
	conn := s.lockConn
	s.lockConn = nil
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, s.cfg.LockKey); err != nil {
		// Never hand a connection that may still hold the lock back to the
		// pool: closing it ends the session and drops the lock.
		_ = conn.Conn().Close(ctx)
	}
	conn.Release()
}

// holdLock reports whether this scheduler holds the lock, taking it if it is
// free. The lock lives on a dedicated connection for as long as we hold it.
func (s *Scheduler) holdLock(ctx context.Context) (bool, error) {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	if s.lockConn != nil {
		if err := s.lockConn.Conn().Ping(ctx); err == nil {
			return true, nil
		}
		// The connection died, so the lock is gone with it.
		_ = s.lockConn.Conn().Close(ctx)
		s.lockConn.Release()
		s.lockConn = nil
	}
	conn, err := s.cfg.Pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, s.cfg.LockKey).Scan(&got); err != nil {
		_ = conn.Conn().Close(ctx)
		conn.Release()
		return false, err
	}
	if !got {
		conn.Release()
		return false, nil
	}
	s.lockConn = conn
	return true, nil
}

// Tick runs one scheduling pass (spec 7.3): claim and start due crons, reap
// stale claims, retry held runs, prune old history. It does nothing when
// another scheduler holds the lock.
func (s *Scheduler) Tick(ctx context.Context) error {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	ok, err := s.holdLock(ctx)
	if err != nil {
		return fmt.Errorf("scheduler lock: %w", err)
	}
	if !ok {
		return nil
	}
	now := s.clock.Now()
	var errs []error

	held, err := db.ListCronRunsByStatus(ctx, s.cfg.Pool, db.CronRunHeld)
	if err != nil {
		errs = append(errs, fmt.Errorf("list held runs: %w", err))
	}

	// Claims too old to re-drive are failed first, so an abandoned claim from
	// before a long outage cannot make the catch-up slot look like an overlap.
	if _, err := db.FailClaimedCronRunsBefore(ctx, s.cfg.Pool, now.Add(-claimMaxAge),
		"claim expired: the run was not started within 23 hours"); err != nil {
		errs = append(errs, fmt.Errorf("expire claims: %w", err))
	}

	claims, err := s.claimDue(ctx, now)
	if err != nil {
		errs = append(errs, fmt.Errorf("claim: %w", err))
	}
	tasks := make([]task, 0, len(claims))
	for i := range claims {
		tasks = append(tasks, task{claims[i].Cron.ID, func() { s.driveClaimed(ctx, &claims[i].Cron, &claims[i].Run, now) }})
	}
	s.runTasks(tasks)
	if err := s.reap(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("reap: %w", err))
	}
	s.retryHeld(ctx, held, now)
	if err := s.prune(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("prune: %w", err))
	}
	return errors.Join(errs...)
}

// task is one unit of a tick's start work; tasks of the same cron run in order, one after another.
type task struct {
	cronID string
	fn     func()
}

// runTasks runs the tasks on a bounded pool of workers, so one slow or hung start delays only its
// own cron (and, once every worker is busy, waits for a free one) rather than every cron behind it.
// It returns when all are done. Each start is bounded by StartTimeout.
func (s *Scheduler) runTasks(tasks []task) {
	var order []string
	groups := map[string][]func(){}
	for _, t := range tasks {
		if _, ok := groups[t.cronID]; !ok {
			order = append(order, t.cronID)
		}
		groups[t.cronID] = append(groups[t.cronID], t.fn)
	}
	sem := make(chan struct{}, s.cfg.Workers)
	var wg sync.WaitGroup
	for _, id := range order {
		fns := groups[id]
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			for _, fn := range fns {
				fn()
			}
		}()
	}
	wg.Wait()
}

// claimDue is step 1: one transaction that claims every due cron, advancing it
// to the first slot strictly after now, and creates its `claimed` run.
func (s *Scheduler) claimDue(ctx context.Context, now time.Time) ([]db.CronClaim, error) {
	type paused struct {
		c      db.Cron
		reason string
	}
	var pausedNow []paused
	claims, err := db.ClaimDueCrons(ctx, s.cfg.Pool, now, claimBatch, func(c *db.Cron) db.ClaimPlan {
		pause := func(reason string) db.ClaimPlan {
			pausedNow = append(pausedNow, paused{*c, reason})
			return db.ClaimPlan{PauseReason: reason}
		}
		if !c.TokenExpiresAt.After(now) {
			return pause("access token expired; renew it to resume")
		}
		sch, err := ParseSchedule(c.Schedule, c.Timezone)
		if err != nil {
			return pause("invalid schedule: " + err.Error())
		}
		next, ok := sch.Next(now)
		if !ok {
			return pause("schedule never runs")
		}
		return db.ClaimPlan{Next: next}
	})
	if err != nil {
		return nil, err
	}
	for _, p := range pausedNow {
		s.notifyPaused(ctx, &p.c, p.reason)
	}
	return claims, nil
}

func (s *Scheduler) notifyPaused(ctx context.Context, c *db.Cron, reason string) {
	if s.cfg.OnPaused != nil {
		s.cfg.OnPaused(ctx, c, reason)
	}
}

// driveClaimed handles a freshly claimed slot: skip it when the cron's previous
// run is still held or its session still active, otherwise start it.
func (s *Scheduler) driveClaimed(ctx context.Context, c *db.Cron, run *db.CronRun, now time.Time) {
	busy, err := s.previousRunActive(ctx, s.cfg.Pool, c.ID, run.ID, run.ScheduledFor)
	if err != nil {
		s.cfg.Logf("cron %s: overlap check: %v", c.ID, err)
		return // left `claimed`; the reaper picks it up
	}
	if busy {
		s.skip(ctx, run, "previous run still active", now)
		return
	}
	s.attemptStart(ctx, c, run, now)
}

// previousRunActive: an earlier run of this cron is still claimed or held, or the
// latest started run's session is still active.
//
// It reads through q (the pool, or the transaction of a run-now: see TxSessions).
func (s *Scheduler) previousRunActive(ctx context.Context, q db.Querier, cronID, runID string, scheduledFor time.Time) (bool, error) {
	open, err := db.HasEarlierOpenCronRun(ctx, q, cronID, runID, scheduledFor)
	if err != nil || open {
		return open, err
	}
	last, err := db.LatestStartedCronRun(ctx, q, cronID, runID)
	if err != nil || last == nil || last.SessionID == nil {
		return false, err
	}
	if ts, ok := s.cfg.Sessions.(TxSessions); ok {
		return ts.SessionActiveOn(ctx, q, *last.SessionID), nil
	}
	return s.cfg.Sessions.SessionActive(ctx, *last.SessionID), nil
}

// attemptStart tries to start a claimed or held run and records the outcome.
func (s *Scheduler) attemptStart(ctx context.Context, c *db.Cron, run *db.CronRun, now time.Time) {
	// The limit is checked and then used under one per-account lock, so two crons of an account
	// cannot both pass the check for its last slot: the second sees the first's session.
	unlock, err := accountStarts.lock(ctx, c.OwnerAccountID)
	if err != nil {
		return // shutting down
	}
	defer unlock()
	if n := s.cfg.Sessions.ConcurrentActive(ctx, c.OwnerAccountID); n >= s.cfg.MaxConcurrentPerAccount {
		s.holdOrSkip(ctx, c, run, "account is at its concurrent cron session limit", now)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, s.cfg.StartTimeout)
	sid, err := s.cfg.Starter.Start(sctx, c, run)
	cancel()
	switch {
	case err == nil && sid != "":
		s.markStarted(ctx, c, run, sid, now)
	case err == nil:
		s.fail(ctx, c, run, "start returned no session id", now)
	case ctx.Err() != nil:
		// Shutting down: leave the run as it is for the reaper.
	case errors.Is(err, ErrNotRunnable):
		s.skip(ctx, run, "the cron was deleted, paused or disabled before the run started: "+trunc(err.Error()), now)
	case errors.Is(err, ErrCapacity):
		s.holdOrSkip(ctx, c, run, err.Error(), now)
	case errors.Is(err, ErrCredential):
		s.fail(ctx, c, run, "credential: "+err.Error(), now)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), s.keyMayExist(ctx, c, run):
		// The start ran out of time, or hit an error after it had written its idempotency key: the
		// session may exist or come to exist. Neither failing the run nor counting it is honest, so
		// it is left as it is; the reaper adopts the session (or releases the key and retries).
		s.cfg.Logf("cron %s run %s: start did not finish cleanly, left for the reaper: %v", c.ID, run.ID, err)
	default:
		s.fail(ctx, c, run, err.Error(), now)
	}
}

// keyMayExist reports whether the run's idempotency key exists, or cannot be looked up: either way
// a session may have been started for the run.
func (s *Scheduler) keyMayExist(ctx context.Context, c *db.Cron, run *db.CronRun) bool {
	row, err := db.GetIdempotencyKey(context.WithoutCancel(ctx), s.cfg.Pool, s.cfg.IdempotencyScope(c), run.IdempotencyKey())
	return err != nil || row != nil
}

func (s *Scheduler) markStarted(ctx context.Context, c *db.Cron, run *db.CronRun, sid string, now time.Time) {
	ok, err := db.TransitionCronRun(ctx, s.cfg.Pool, run.ID, []string{db.CronRunClaimed, db.CronRunHeld}, db.CronRunStarted, "", sid, now)
	if err != nil {
		s.cfg.Logf("cron %s run %s: mark started: %v", c.ID, run.ID, err)
		return
	}
	if !ok {
		s.cfg.Logf("cron %s run %s: started session %s but the run had already changed state", c.ID, run.ID, sid)
		return
	}
	if err := db.ResetCronFailures(ctx, s.cfg.Pool, c.ID); err != nil {
		s.cfg.Logf("cron %s: reset failures: %v", c.ID, err)
	}
}

// graceDeadline is when a held run gives up: the grace window is measured from
// the later of the slot and the scheduler's start-up, so a machine that was off
// longer than the window still gets its one catch-up run.
func (s *Scheduler) graceDeadline(c *db.Cron, run *db.CronRun) time.Time {
	from := run.ScheduledFor
	if s.startedAt.After(from) {
		from = s.startedAt
	}
	return from.Add(time.Duration(c.GraceSeconds) * time.Second)
}

func (s *Scheduler) holdOrSkip(ctx context.Context, c *db.Cron, run *db.CronRun, reason string, now time.Time) {
	if now.After(s.graceDeadline(c, run)) {
		s.skip(ctx, run, "grace window ended while waiting: "+trunc(reason), now)
		return
	}
	if _, err := db.TransitionCronRun(ctx, s.cfg.Pool, run.ID, []string{db.CronRunClaimed, db.CronRunHeld},
		db.CronRunHeld, trunc(reason), "", now); err != nil {
		s.cfg.Logf("cron %s run %s: hold: %v", c.ID, run.ID, err)
	}
}

func (s *Scheduler) skip(ctx context.Context, run *db.CronRun, reason string, now time.Time) {
	if _, err := db.TransitionCronRun(ctx, s.cfg.Pool, run.ID, []string{db.CronRunClaimed, db.CronRunHeld},
		db.CronRunSkipped, trunc(reason), "", now); err != nil {
		s.cfg.Logf("cron run %s: skip: %v", run.ID, err)
	}
}

func (s *Scheduler) fail(ctx context.Context, c *db.Cron, run *db.CronRun, reason string, now time.Time) {
	ok, err := db.TransitionCronRun(ctx, s.cfg.Pool, run.ID, []string{db.CronRunClaimed, db.CronRunHeld},
		db.CronRunFailed, trunc(reason), "", now)
	if err != nil {
		s.cfg.Logf("cron %s run %s: mark failed: %v", c.ID, run.ID, err)
		return
	}
	if !ok || run.Manual {
		// A manual run's failure is shown on the run and told to the person who clicked; it never
		// counts toward the automatic pause (the person is watching, and a click cannot be a strike).
		return
	}
	pauseReason := fmt.Sprintf("paused after %d failed runs in a row; last error: %s", s.cfg.FailureThreshold, trunc(reason))
	_, paused, err := db.RecordCronFailure(ctx, s.cfg.Pool, c.ID, s.cfg.FailureThreshold, pauseReason)
	if err != nil {
		s.cfg.Logf("cron %s: record failure: %v", c.ID, err)
		return
	}
	if paused {
		s.notifyPaused(ctx, c, pauseReason)
	}
}

// reap re-drives `claimed` runs that were never started (a crash between the
// claim and the start): older than a minute and younger than 23 hours (older
// ones were failed at the start of the tick). If the idempotency key already produced a session the run
// adopts it; if the key exists without a session it is released and the start
// retried once.
func (s *Scheduler) reap(ctx context.Context, now time.Time) error {
	// A run whose lease has not lapsed (a run-now still starting on a request goroutine) is not a
	// candidate: it is in progress, not abandoned.
	cands, err := db.ListReapableClaimedCronRuns(ctx, s.cfg.Pool, now.Add(-claimMaxAge), now.Add(-claimReapAfter), now)
	if err != nil {
		return err
	}
	tasks := make([]task, 0, len(cands))
	for i := range cands {
		run := &cands[i]
		tasks = append(tasks, task{run.CronID, func() { s.reapOne(ctx, run, now) }})
	}
	s.runTasks(tasks)
	return nil
}

func (s *Scheduler) reapOne(ctx context.Context, run *db.CronRun, now time.Time) {
	c, err := db.GetCron(ctx, s.cfg.Pool, run.CronID)
	if err != nil {
		return // deleted meanwhile: its runs go with it
	}
	if !c.Enabled && !run.Manual || c.PausedReason != nil {
		s.skip(ctx, run, "cron disabled or paused before the run started", now)
		return
	}
	adopted, err := s.recoverKey(ctx, c, run, now)
	if err != nil {
		s.cfg.Logf("cron %s run %s: reaper: %v", c.ID, run.ID, err)
		return
	}
	if adopted {
		return
	}
	// A start that never finishes (it always times out, say) must not be retried every minute for
	// the next 23 hours: count the re-drive, and fail the run once it has had its few.
	n, err := db.BumpCronRunRedrives(ctx, s.cfg.Pool, run.ID)
	if err != nil {
		s.cfg.Logf("cron %s run %s: reaper: count re-drive: %v", c.ID, run.ID, err)
		return
	}
	if n > maxReaperRedrives {
		s.fail(ctx, c, run, fmt.Sprintf("the start did not finish after %d attempts", maxReaperRedrives), now)
		return
	}
	s.attemptStart(ctx, c, run, now)
}

// recoverKey looks at the run's idempotency key. A key with a session means the
// crash came after the start: adopt the session. A key with no session row means
// the crash came in between: release the key so the retry can claim it again.
func (s *Scheduler) recoverKey(ctx context.Context, c *db.Cron, run *db.CronRun, now time.Time) (adopted bool, err error) {
	scope, key := s.cfg.IdempotencyScope(c), run.IdempotencyKey()
	row, err := db.GetIdempotencyKey(ctx, s.cfg.Pool, scope, key)
	if err != nil || row == nil {
		return false, err
	}
	sess, err := db.GetSession(ctx, s.cfg.Pool, row.SessionID)
	if err != nil {
		return false, err
	}
	if sess != nil {
		s.markStarted(ctx, c, run, row.SessionID, now)
		return true, nil
	}
	return false, db.ReleaseIdempotencyKey(ctx, s.cfg.Pool, scope, key, row.SessionID)
}

// retryHeld retries the runs that were held when the tick began (a run held
// during this tick waits for the next one).
func (s *Scheduler) retryHeld(ctx context.Context, held []db.CronRun, now time.Time) {
	tasks := make([]task, 0, len(held))
	for i := range held {
		id, cronID := held[i].ID, held[i].CronID
		tasks = append(tasks, task{cronID, func() { s.retryOne(ctx, id, now) }})
	}
	s.runTasks(tasks)
}

func (s *Scheduler) retryOne(ctx context.Context, runID string, now time.Time) {
	run, err := db.GetCronRun(ctx, s.cfg.Pool, runID)
	if err != nil || run.Status != db.CronRunHeld {
		return
	}
	c, err := db.GetCron(ctx, s.cfg.Pool, run.CronID)
	if err != nil {
		return
	}
	switch {
	case !c.Enabled && !run.Manual, c.PausedReason != nil:
		s.skip(ctx, run, "cron disabled or paused while the run was held", now)
	case now.After(s.graceDeadline(c, run)):
		why := "capacity"
		if run.Reason != nil {
			why = *run.Reason
		}
		s.skip(ctx, run, "grace window ended while waiting: "+trunc(why), now)
	default:
		s.attemptStart(ctx, c, run, now)
	}
}

func (s *Scheduler) prune(ctx context.Context, now time.Time) error {
	if !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < pruneEvery {
		return nil
	}
	s.lastPrune = now
	_, err := db.PruneCronRuns(ctx, s.cfg.Pool, now.Add(-s.cfg.RunRetention))
	return err
}

// RunNow starts a manual run of an owned cron immediately: a `manual` run whose
// idempotency key is cron:<id>:manual:<run id>. It does not move the schedule.
// The returned run is the outcome (started, held, or failed).
func (s *Scheduler) RunNow(ctx context.Context, owner, cronID string) (*db.CronRun, error) {
	c, err := db.GetOwnedCron(ctx, s.cfg.Pool, owner, cronID)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	if c.PausedReason != nil {
		return nil, fmt.Errorf("%w: the cron is paused (%s); resume it first", ErrInvalid, *c.PausedReason)
	}
	if !c.TokenExpiresAt.After(now) {
		return nil, fmt.Errorf("%w: the cron's access token has expired; renew it first", ErrInvalid)
	}
	// The overlap check and the insert happen under the cron's row lock, so a double click (or two
	// tabs) makes one run and the other is told the cron is already running. The run holds a lease
	// while its start runs on this goroutine: the reaper must not re-drive it, or a start that is
	// merely slow would be started a second time.
	lease := now.Add(s.cfg.StartTimeout + 2*claimReapAfter)
	run, err := db.InsertManualCronRunGuarded(ctx, s.cfg.Pool, c.ID, now, lease, func(ctx context.Context, q db.Querier) (bool, error) {
		return s.previousRunActive(ctx, q, c.ID, "00000000-0000-0000-0000-000000000000", now.Add(time.Second))
	})
	if errors.Is(err, db.ErrCronRunOpen) {
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, err
	}
	s.attemptStart(ctx, c, run, now)
	return db.GetCronRun(ctx, s.cfg.Pool, run.ID)
}

func trunc(s string) string {
	r := []rune(s)
	if len(r) <= maxReasonLen {
		return s
	}
	return string(r[:maxReasonLen]) + "..."
}
