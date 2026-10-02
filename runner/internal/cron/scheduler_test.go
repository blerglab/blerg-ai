package cron

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── fakes ────────────────────────────────────────────────────────────────────

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *fakeClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

// fakeStarter behaves like the real start path where it matters to the
// scheduler: it is idempotent on the run's key (claims it, creates a session
// row, replays the session on a repeat) and can be told to fail.
type fakeStarter struct {
	pool *pgxpool.Pool

	mu    sync.Mutex
	calls []string // run ids, in order
	err   error    // returned by every call while set
	byRun map[string]error
}

func (f *fakeStarter) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeStarter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeStarter) Start(ctx context.Context, c *db.Cron, run *db.CronRun) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, run.ID)
	err := f.err
	if e, ok := f.byRun[run.ID]; ok {
		err = e
	}
	f.mu.Unlock()
	if err != nil {
		return "", err
	}
	var sid string
	if e := f.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&sid); e != nil {
		return "", e
	}
	existing, claimed, e := db.ClaimIdempotencyKey(ctx, f.pool, "cron:"+c.ID, run.IdempotencyKey(), "h", sid, 24*time.Hour)
	if e != nil {
		return "", e
	}
	if !claimed {
		return existing.SessionID, nil // replay: same key, same session
	}
	const daemon = "00000000-0000-0000-0000-0000000000d1"
	if e := db.UpsertDaemon(ctx, f.pool, daemon, "d", "local", "/r"); e != nil {
		return "", e
	}
	if e := db.InsertSession(ctx, f.pool, sid, daemon, "running", "/r/p", "p", "cron", ""); e != nil {
		return "", e
	}
	return sid, nil
}

type fakeSessions struct {
	mu         sync.Mutex
	active     map[string]bool
	concurrent map[string]int
}

func (f *fakeSessions) SessionActive(_ context.Context, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[id]
}

func (f *fakeSessions) ConcurrentActive(_ context.Context, account string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.concurrent[account]
}

func (f *fakeSessions) setActive(id string, v bool) {
	f.mu.Lock()
	f.active[id] = v
	f.mu.Unlock()
}

func (f *fakeSessions) setConcurrent(account string, n int) {
	f.mu.Lock()
	f.concurrent[account] = n
	f.mu.Unlock()
}

// ── environment ─────────────────────────────────────────────────────────────

var start0 = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

type env struct {
	t        *testing.T
	pool     *pgxpool.Pool
	clock    *fakeClock
	starter  *fakeStarter
	sessions *fakeSessions
	paused   chan string
}

// newEnv gives every test its own schema with all migrations applied, a fake
// clock at start0, a fake Starter and fake session dependency.
func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	ctx := context.Background()
	const schema = "t22_cron"
	boot, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema),
		fmt.Sprintf("CREATE SCHEMA %s", schema),
	} {
		if _, err := boot.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 12
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = boot.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
		boot.Close()
	})
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return &env{
		t: t, pool: pool, clock: &fakeClock{t: start0},
		starter:  &fakeStarter{pool: pool, byRun: map[string]error{}},
		sessions: &fakeSessions{active: map[string]bool{}, concurrent: map[string]int{}},
		paused:   make(chan string, 16),
	}
}

// sched builds a scheduler whose start-up time is the clock's current value.
func (e *env) sched(opts ...func(*Config)) *Scheduler {
	e.t.Helper()
	cfg := Config{
		Pool: e.pool, Starter: e.starter, Sessions: e.sessions, Clock: e.clock,
		OnPaused: func(_ context.Context, c *db.Cron, reason string) { e.paused <- c.ID + ": " + reason },
	}
	for _, o := range opts {
		o(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		e.t.Fatalf("New: %v", err)
	}
	e.t.Cleanup(func() { s.Release(context.Background()) })
	return s
}

func (e *env) addCron(expr, tz string, mut ...func(*NewCron)) *db.Cron {
	e.t.Helper()
	in := NewCron{
		Owner: "acct-1", Name: "focus", Schedule: expr, Timezone: tz, Prompt: "do the thing",
		TokenID: "tok-" + fmt.Sprint(time.Now().UnixNano()), TokenExpiresAt: e.clock.Now().Add(300 * 24 * time.Hour),
		Enabled: true,
	}
	for _, m := range mut {
		m(&in)
	}
	c, err := e.sched().CreateCron(context.Background(), in)
	if err != nil {
		e.t.Fatalf("CreateCron: %v", err)
	}
	return c
}

func (e *env) runs(cronID string) []db.CronRun {
	e.t.Helper()
	r, err := db.ListCronRuns(context.Background(), e.pool, cronID, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) count(table string) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) cron(id string) *db.Cron {
	e.t.Helper()
	c, err := db.GetCron(context.Background(), e.pool, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func tick(t *testing.T, s *Scheduler) {
	t.Helper()
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
}

func at(y int, mo time.Month, d, h, mi, sec int) time.Time {
	return time.Date(y, mo, d, h, mi, sec, 0, time.UTC)
}

// ── tests ───────────────────────────────────────────────────────────────────

// The machine was off for two days: overdue crons fire ONCE, flagged late, not
// once per missed slot, and the next slot is the first one after now.
func TestMachineOffTwoDaysExactlyOneLateRun(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC") // next slot 13:00 on 1 June
	e.clock.Set(start0.Add(48*time.Hour + 10*time.Minute))
	s := e.sched()

	tick(t, s)
	runs := e.runs(c.ID)
	if len(runs) != 1 {
		t.Fatalf("%d runs after two days off, want exactly 1", len(runs))
	}
	r := runs[0]
	if r.Status != db.CronRunStarted || !r.Late || r.SessionID == nil || !r.ScheduledFor.Equal(at(2026, 6, 1, 13, 0, 0)) {
		t.Fatalf("run = %+v, want a started late run for the first missed slot", r)
	}
	if got := e.cron(c.ID).NextRunAt; !got.Equal(at(2026, 6, 3, 13, 0, 0)) {
		t.Fatalf("next_run_at = %v, want the first slot strictly after now (2026-06-03 13:00)", got)
	}
	e.clock.Advance(30 * time.Second)
	tick(t, s)
	if n := len(e.runs(c.ID)); n != 1 {
		t.Fatalf("a later tick made %d runs total, want still 1", n)
	}
	if e.starter.callCount() != 1 {
		t.Fatalf("starter called %d times", e.starter.callCount())
	}
}

func TestOnTimeRunIsNotLate(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	e.clock.Set(at(2026, 6, 1, 13, 0, 20))
	tick(t, s)
	runs := e.runs(c.ID)
	if len(runs) != 1 || runs[0].Late || runs[0].Manual || runs[0].StartedAt == nil {
		t.Fatalf("runs = %+v", runs)
	}
	if !e.cron(c.ID).LastRunAt.Equal(at(2026, 6, 1, 13, 0, 20)) {
		t.Fatalf("last_run_at = %v", e.cron(c.ID).LastRunAt)
	}
}

// A crash between the claim commit and the start must neither lose the slot
// nor start it twice.
func TestCrashBetweenClaimAndStartIsRecoveredOnce(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	s := e.sched()

	// Claim, then "crash": the Starter call never happens.
	claims, err := s.claimDue(context.Background(), e.clock.Now())
	if err != nil || len(claims) != 1 {
		t.Fatalf("claimDue = %d, %v", len(claims), err)
	}
	if e.starter.callCount() != 0 || e.runs(c.ID)[0].Status != db.CronRunClaimed {
		t.Fatal("setup: the run must be claimed and not started")
	}

	// Restart 30s later: a fresh claim is not yet the reaper's business.
	e.clock.Advance(30 * time.Second)
	s2 := e.sched()
	tick(t, s2)
	if e.starter.callCount() != 0 {
		t.Fatal("a claim younger than a minute must be left alone")
	}

	// Past a minute the reaper re-drives it, once.
	e.clock.Advance(90 * time.Second)
	tick(t, s2)
	tick(t, s2)
	runs := e.runs(c.ID)
	if len(runs) != 1 || runs[0].Status != db.CronRunStarted || runs[0].SessionID == nil {
		t.Fatalf("runs after reaping = %+v", runs)
	}
	if e.starter.callCount() != 1 || e.count("sessions") != 1 {
		t.Fatalf("starter calls %d, sessions %d, want 1 and 1", e.starter.callCount(), e.count("sessions"))
	}
}

// Crash after the key was claimed but before the session row existed: the
// reaper releases the key and starts it once more.
func TestReaperReleasesKeyWithoutSession(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	s := e.sched()
	claims, err := s.claimDue(context.Background(), e.clock.Now())
	if err != nil || len(claims) != 1 {
		t.Fatalf("claimDue = %d, %v", len(claims), err)
	}
	run := claims[0].Run
	var ghost string
	if err := e.pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&ghost); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := db.ClaimIdempotencyKey(context.Background(), e.pool, "cron:"+c.ID, run.IdempotencyKey(), "h", ghost, 24*time.Hour); err != nil || !claimed {
		t.Fatalf("setup key: %v %v", claimed, err)
	}

	e.clock.Advance(2 * time.Minute)
	tick(t, e.sched())
	got, _ := db.GetCronRun(context.Background(), e.pool, run.ID)
	if got.Status != db.CronRunStarted || got.SessionID == nil || *got.SessionID == ghost {
		t.Fatalf("run = %+v, want started with a real session (not the ghost %s)", got, ghost)
	}
	if e.count("sessions") != 1 || e.count("runner_idempotency") != 1 {
		t.Fatalf("sessions=%d keys=%d, want 1 and 1", e.count("sessions"), e.count("runner_idempotency"))
	}
}

// Crash after the session was created but before the run was marked started:
// the reaper adopts that session and never calls the Starter again.
func TestReaperAdoptsExistingSession(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	s := e.sched()
	claims, _ := s.claimDue(context.Background(), e.clock.Now())
	run := claims[0].Run
	sid, err := e.starter.Start(context.Background(), &claims[0].Cron, &run) // starts, then "crashes" before marking
	if err != nil {
		t.Fatal(err)
	}
	e.starter.mu.Lock()
	e.starter.calls = nil
	e.starter.mu.Unlock()

	e.clock.Advance(2 * time.Minute)
	tick(t, e.sched())
	got, _ := db.GetCronRun(context.Background(), e.pool, run.ID)
	if got.Status != db.CronRunStarted || got.SessionID == nil || *got.SessionID != sid {
		t.Fatalf("run = %+v, want started with session %s", got, sid)
	}
	if e.starter.callCount() != 0 || e.count("sessions") != 1 || len(e.runs(c.ID)) != 1 {
		t.Fatalf("starter calls %d sessions %d", e.starter.callCount(), e.count("sessions"))
	}
}

func TestReaperFailsClaimsOlderThan23Hours(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	if _, err := e.sched().claimDue(context.Background(), e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(24 * time.Hour)
	s := e.sched()
	tick(t, s)
	var old db.CronRun
	for _, r := range e.runs(c.ID) {
		if r.Status == db.CronRunFailed {
			old = r
		}
	}
	if old.ID == "" || old.Reason == nil || !strings.Contains(*old.Reason, "expired") {
		t.Fatalf("runs = %+v, want the 24h-old claim failed as expired", e.runs(c.ID))
	}
	// The overdue slot itself was collapsed into one fresh run at this tick, not the old claim.
	if e.starter.callCount() != 1 {
		t.Fatalf("starter calls = %d, want only the fresh catch-up run", e.starter.callCount())
	}
}

func TestTwoSchedulersOneFiring(t *testing.T) {
	e := newEnv(t)
	e.addCron("0 * * * *", "UTC")
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	s1, s2 := e.sched(), e.sched()

	var wg sync.WaitGroup
	for round := 0; round < 3; round++ {
		for _, s := range []*Scheduler{s1, s2} {
			wg.Add(1)
			go func(s *Scheduler) {
				defer wg.Done()
				if err := s.Tick(context.Background()); err != nil {
					t.Errorf("Tick: %v", err)
				}
			}(s)
		}
		wg.Wait()
	}
	if n := e.starter.callCount(); n != 1 {
		t.Fatalf("two schedulers started %d sessions, want exactly 1", n)
	}
	if e.count("cron_runs") != 1 || e.count("sessions") != 1 {
		t.Fatalf("runs=%d sessions=%d", e.count("cron_runs"), e.count("sessions"))
	}
	holders := 0
	for _, s := range []*Scheduler{s1, s2} {
		if s.HoldsLock() {
			holders++
		}
	}
	if holders != 1 {
		t.Fatalf("%d schedulers hold the lock, want 1", holders)
	}

	// The holder goes away: the other takes over on its next tick.
	var standby, holder *Scheduler
	if s1.HoldsLock() {
		holder, standby = s1, s2
	} else {
		holder, standby = s2, s1
	}
	holder.Release(context.Background())
	e.clock.Set(at(2026, 6, 1, 14, 0, 5))
	tick(t, standby)
	if !standby.HoldsLock() || e.count("cron_runs") != 2 {
		t.Fatalf("standby did not take over: holds=%v runs=%d", standby.HoldsLock(), e.count("cron_runs"))
	}
}

// Spring forward (New York, 2026-03-08): the 02:30 slot does not exist and runs
// once, at 03:00 EDT (07:00Z); the day after is a normal 02:30 EDT.
func TestSchedulerDSTSpringForward(t *testing.T) {
	e := newEnv(t)
	e.clock.Set(at(2026, 3, 7, 12, 0, 0))
	c := e.addCron("30 2 * * *", "America/New_York")
	if got := e.cron(c.ID).NextRunAt; !got.Equal(at(2026, 3, 8, 7, 0, 0)) {
		t.Fatalf("first slot = %v, want 2026-03-08T07:00Z", got)
	}
	s := e.sched()
	e.clock.Set(at(2026, 3, 8, 6, 59, 0))
	tick(t, s)
	if len(e.runs(c.ID)) != 0 {
		t.Fatal("fired before the first valid time after the gap")
	}
	e.clock.Set(at(2026, 3, 8, 7, 0, 10))
	tick(t, s)
	e.clock.Set(at(2026, 3, 8, 12, 0, 0))
	tick(t, s)
	runs := e.runs(c.ID)
	if len(runs) != 1 || !runs[0].ScheduledFor.Equal(at(2026, 3, 8, 7, 0, 0)) || runs[0].Late {
		t.Fatalf("spring-forward day runs = %+v, want exactly one at 07:00Z", runs)
	}
	e.clock.Set(at(2026, 3, 9, 6, 30, 10))
	tick(t, s)
	if len(e.runs(c.ID)) != 2 {
		t.Fatalf("day after: %d runs total, want 2", len(e.runs(c.ID)))
	}
}

// Fall back (New York, 2026-11-01): 01:30 happens twice; the slot fires once,
// at its first occurrence (05:30Z), and not again at the second (06:30Z).
func TestSchedulerDSTFallBack(t *testing.T) {
	e := newEnv(t)
	e.clock.Set(at(2026, 10, 31, 12, 0, 0))
	c := e.addCron("30 1 * * *", "America/New_York")
	s := e.sched()
	e.clock.Set(at(2026, 11, 1, 5, 30, 10))
	tick(t, s)
	e.clock.Set(at(2026, 11, 1, 6, 30, 10))
	tick(t, s)
	e.clock.Set(at(2026, 11, 1, 12, 0, 0))
	tick(t, s)
	runs := e.runs(c.ID)
	if len(runs) != 1 || !runs[0].ScheduledFor.Equal(at(2026, 11, 1, 5, 30, 0)) {
		t.Fatalf("fall-back day runs = %+v, want exactly one at 05:30Z", runs)
	}
	e.clock.Set(at(2026, 11, 2, 6, 30, 10))
	tick(t, s)
	if len(e.runs(c.ID)) != 2 {
		t.Fatalf("next day: %d runs total, want 2", len(e.runs(c.ID)))
	}
}

func TestHeldRunGraceMeasuredFromSlot(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.GraceSeconds = ptr(3600) })
	s := e.sched() // started at 12:00
	e.starter.setErr(fmt.Errorf("no daemon connected: %w", ErrCapacity))

	e.clock.Set(at(2026, 6, 1, 13, 0, 30))
	tick(t, s)
	r := e.runs(c.ID)[0]
	if r.Status != db.CronRunHeld || r.Reason == nil {
		t.Fatalf("run = %+v, want held with a reason", r)
	}
	e.clock.Set(at(2026, 6, 1, 13, 59, 0))
	tick(t, s)
	if got := e.runs(c.ID)[0].Status; got != db.CronRunHeld {
		t.Fatalf("status inside the window = %s", got)
	}
	e.clock.Set(at(2026, 6, 1, 14, 0, 45)) // slot + grace passed, and the 14:00 slot is due too
	tick(t, s)
	runs := e.runs(c.ID) // newest first
	if len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
	for _, run := range runs {
		if run.Status != db.CronRunSkipped || run.Reason == nil {
			t.Fatalf("run = %+v, want skipped with a reason", run)
		}
	}
	// The 14:00 slot arrived while 13:00 was still held; the window then closed on 13:00.
	if *runs[0].Reason != "previous run still active" || !strings.Contains(*runs[1].Reason, "grace window") {
		t.Fatalf("reasons: %q / %q", *runs[0].Reason, *runs[1].Reason)
	}
	if got := e.cron(c.ID).ConsecutiveFailures; got != 0 {
		t.Fatalf("held/skipped runs counted %d failures", got)
	}
}

// A machine that was off longer than the grace window still gets its one
// catch-up run: the window is measured from the later of the slot and the
// scheduler's start-up.
func TestRestartPastGraceWindowStillRuns(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.GraceSeconds = ptr(3600) })
	e.clock.Set(start0.Add(5 * 24 * time.Hour)) // five days later: way past slot + grace
	s := e.sched()
	e.starter.setErr(fmt.Errorf("no daemon connected yet: %w", ErrCapacity))

	tick(t, s)
	runs := e.runs(c.ID)
	if len(runs) != 1 || runs[0].Status != db.CronRunHeld || !runs[0].Late {
		t.Fatalf("runs = %+v, want one late held run (not skipped)", runs)
	}
	e.clock.Advance(40 * time.Minute)
	tick(t, s)
	if e.runs(c.ID)[0].Status != db.CronRunHeld {
		t.Fatal("still inside the window measured from start-up; must stay held")
	}
	e.starter.setErr(nil) // the daemon connects
	e.clock.Advance(time.Minute)
	tick(t, s)
	runs = e.runs(c.ID)
	if len(runs) != 1 || runs[0].Status != db.CronRunStarted || !runs[0].Late || runs[0].SessionID == nil {
		t.Fatalf("runs = %+v, want the one catch-up run started", runs)
	}
	if e.starter.callCount() < 2 {
		t.Fatal("setup: expected retries")
	}
}

func TestRestartHeldPastStartupWindowIsSkipped(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.GraceSeconds = ptr(3600) })
	e.clock.Set(start0.Add(5 * 24 * time.Hour))
	s := e.sched()
	e.starter.setErr(fmt.Errorf("cluster at capacity: %w", ErrCapacity))
	tick(t, s)
	e.clock.Advance(61 * time.Minute)
	tick(t, s)
	got := e.runs(c.ID) // newest first; the catch-up run is the oldest slot
	oldest := got[len(got)-1]
	if oldest.Status != db.CronRunSkipped || oldest.Reason == nil || !strings.Contains(*oldest.Reason, "grace window") {
		t.Fatalf("runs = %+v, want the catch-up run skipped after start-up + grace", got)
	}
}

// A run that stays held is retried every tick but creates nothing beyond its
// own row: no extra runs, no idempotency keys, no sessions, no failures.
func TestHeldRetriesCreateNoExtraRows(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.GraceSeconds = ptr(6 * 3600) })
	s := e.sched()
	e.starter.setErr(fmt.Errorf("no daemon: %w", ErrCapacity))
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	for i := 0; i < 20; i++ {
		e.clock.Advance(30 * time.Second)
		tick(t, s)
	}
	if n := len(e.runs(c.ID)); n != 1 {
		t.Fatalf("%d run rows after 21 held ticks, want 1", n)
	}
	if e.count("sessions") != 0 || e.count("runner_idempotency") != 0 {
		t.Fatalf("held retries created sessions=%d keys=%d", e.count("sessions"), e.count("runner_idempotency"))
	}
	if e.cron(c.ID).ConsecutiveFailures != 0 {
		t.Fatal("capacity is not a failure")
	}
	if e.starter.callCount() != 21 {
		t.Fatalf("starter retried %d times, want once per tick (21)", e.starter.callCount())
	}
	e.starter.setErr(nil)
	e.clock.Advance(30 * time.Second)
	tick(t, s)
	if got := e.runs(c.ID)[0]; got.Status != db.CronRunStarted || e.count("sessions") != 1 {
		t.Fatalf("run = %+v sessions=%d, want started once capacity returns", got, e.count("sessions"))
	}
}

func TestOverlapSkipsSlotWhilePreviousSessionActive(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	first := e.runs(c.ID)[0]
	if first.Status != db.CronRunStarted {
		t.Fatalf("setup: %+v", first)
	}
	e.sessions.setActive(*first.SessionID, true)

	e.clock.Set(at(2026, 6, 1, 14, 0, 5))
	tick(t, s)
	runs := e.runs(c.ID) // newest first
	if len(runs) != 2 || runs[0].Status != db.CronRunSkipped || runs[0].Reason == nil || *runs[0].Reason != "previous run still active" {
		t.Fatalf("runs = %+v, want the 14:00 slot skipped: previous run still active", runs)
	}
	if e.starter.callCount() != 1 {
		t.Fatal("a skipped slot must not call the starter")
	}
	e.sessions.setActive(*first.SessionID, false)
	e.clock.Set(at(2026, 6, 1, 15, 0, 5))
	tick(t, s)
	if got := e.runs(c.ID)[0]; got.Status != db.CronRunStarted {
		t.Fatalf("after the session ended: %+v", got)
	}
}

func TestNewSlotWhileEarlierRunHeldIsSkipped(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.GraceSeconds = ptr(6 * 3600) })
	s := e.sched()
	e.starter.setErr(fmt.Errorf("busy: %w", ErrCapacity))
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	e.clock.Set(at(2026, 6, 1, 14, 0, 5))
	tick(t, s)
	runs := e.runs(c.ID)
	if len(runs) != 2 || runs[0].Status != db.CronRunSkipped || runs[1].Status != db.CronRunHeld {
		t.Fatalf("runs = %+v, want the new slot skipped and the earlier one still held", runs)
	}
	if runs[0].Reason == nil || *runs[0].Reason != "previous run still active" {
		t.Fatalf("reason = %v", runs[0].Reason)
	}
}

func TestThreeConsecutiveFailuresPause(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	e.starter.setErr(errors.New("boom"))
	for i, h := range []int{13, 14, 15} {
		e.clock.Set(at(2026, 6, 1, h, 0, 5))
		tick(t, s)
		if got := e.cron(c.ID).ConsecutiveFailures; got != i+1 {
			t.Fatalf("after failure %d: count = %d", i+1, got)
		}
	}
	got := e.cron(c.ID)
	if got.PausedReason == nil || !strings.Contains(*got.PausedReason, "3") {
		t.Fatalf("paused_reason = %v", got.PausedReason)
	}
	select {
	case msg := <-e.paused:
		if !strings.HasPrefix(msg, c.ID) {
			t.Fatalf("OnPaused = %q", msg)
		}
	default:
		t.Fatal("OnPaused was not called")
	}
	for _, r := range e.runs(c.ID) {
		if r.Status != db.CronRunFailed || r.Reason == nil || !strings.Contains(*r.Reason, "boom") {
			t.Fatalf("run = %+v, want failed with the cause", r)
		}
	}
	calls := e.starter.callCount()
	e.clock.Set(at(2026, 6, 1, 16, 0, 5))
	tick(t, s)
	if e.starter.callCount() != calls || len(e.runs(c.ID)) != 3 {
		t.Fatal("a paused cron must not fire")
	}
}

func TestCredentialErrorFailsAndSuccessResetsCount(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	e.starter.setErr(fmt.Errorf("connection needs sign-in: %w", ErrCredential))
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	e.clock.Set(at(2026, 6, 1, 14, 0, 5))
	tick(t, s)
	if got := e.cron(c.ID).ConsecutiveFailures; got != 2 {
		t.Fatalf("count = %d, want 2 (credential errors are failures)", got)
	}
	if r := e.runs(c.ID)[0]; r.Status != db.CronRunFailed {
		t.Fatalf("run = %+v", r)
	}
	e.starter.setErr(nil)
	e.clock.Set(at(2026, 6, 1, 15, 0, 5))
	tick(t, s)
	if got := e.cron(c.ID).ConsecutiveFailures; got != 0 {
		t.Fatalf("count after a good run = %d", got)
	}
}

func TestEditAndResumeDoNotFireImmediately(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	ctx := context.Background()
	e.clock.Set(at(2026, 6, 1, 20, 30, 0)) // the 13:00 slot is long overdue, unclaimed

	sched := "*/30 * * * *"
	upd, err := s.UpdateCron(ctx, "acct-1", c.ID, CronPatch{Schedule: &sched})
	if err != nil {
		t.Fatal(err)
	}
	if !upd.NextRunAt.Equal(at(2026, 6, 1, 21, 0, 0)) {
		t.Fatalf("next_run_at after schedule edit = %v, want 21:00 (from now)", upd.NextRunAt)
	}
	tick(t, s)
	if len(e.runs(c.ID)) != 0 {
		t.Fatal("editing the schedule fired an overdue run immediately")
	}

	tz := "America/New_York"
	upd, err = s.UpdateCron(ctx, "acct-1", c.ID, CronPatch{Timezone: &tz})
	if err != nil || !upd.NextRunAt.Equal(at(2026, 6, 1, 21, 0, 0)) {
		t.Fatalf("timezone edit: %v, next=%v", err, upd.NextRunAt)
	}

	// A paused cron whose slot passed while paused: resuming does not fire it.
	if ok, err := db.PauseCron(ctx, e.pool, c.ID, "access revoked"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	e.clock.Set(at(2026, 6, 2, 9, 10, 0))
	upd, err = s.ResumeCron(ctx, "acct-1", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if upd.PausedReason != nil || upd.ConsecutiveFailures != 0 || !upd.NextRunAt.Equal(at(2026, 6, 2, 9, 30, 0)) {
		t.Fatalf("resumed = paused %v failures %d next %v", upd.PausedReason, upd.ConsecutiveFailures, upd.NextRunAt)
	}
	tick(t, s)
	if len(e.runs(c.ID)) != 0 {
		t.Fatal("resuming fired immediately")
	}

	// Re-enabling a disabled cron recomputes too; an unrelated edit does not touch next_run_at.
	off := false
	if _, err := s.UpdateCron(ctx, "acct-1", c.ID, CronPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(at(2026, 6, 5, 7, 0, 0))
	on := true
	upd, _ = s.UpdateCron(ctx, "acct-1", c.ID, CronPatch{Enabled: &on})
	if !upd.NextRunAt.After(e.clock.Now()) {
		t.Fatalf("re-enable next_run_at = %v", upd.NextRunAt)
	}
	name := "renamed"
	before := upd.NextRunAt
	upd, _ = s.UpdateCron(ctx, "acct-1", c.ID, CronPatch{Name: &name})
	if !upd.NextRunAt.Equal(before) || upd.Name != "renamed" {
		t.Fatalf("rename changed next_run_at: %v -> %v", before, upd.NextRunAt)
	}

	// Validation and ownership.
	bad := "*/5 * * * *"
	if _, err := s.UpdateCron(ctx, "acct-1", c.ID, CronPatch{Schedule: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too-frequent edit: %v", err)
	}
	if _, err := s.UpdateCron(ctx, "acct-2", c.ID, CronPatch{Name: &name}); !errors.Is(err, db.ErrCronNotFound) {
		t.Fatalf("another account's edit: %v", err)
	}
}

func TestRunNow(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	ctx := context.Background()

	run, err := s.RunNow(ctx, "acct-1", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Manual || run.Status != db.CronRunStarted || run.SessionID == nil {
		t.Fatalf("run = %+v", run)
	}
	wantKey := fmt.Sprintf("cron:%s:manual:%s", c.ID, run.ID)
	if run.IdempotencyKey() != wantKey {
		t.Fatalf("key = %s, want %s", run.IdempotencyKey(), wantKey)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM runner_idempotency WHERE key = $1`, wantKey).Scan(&n); err != nil || n != 1 {
		t.Fatalf("idempotency key rows = %d, %v", n, err)
	}
	if !e.cron(c.ID).NextRunAt.Equal(at(2026, 6, 1, 13, 0, 0)) {
		t.Fatal("a manual run must not move the schedule")
	}
	e.sessions.setActive(*run.SessionID, true)
	if _, err := s.RunNow(ctx, "acct-1", c.ID); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second run-now while active: %v", err)
	}
	if _, err := s.RunNow(ctx, "acct-2", c.ID); !errors.Is(err, db.ErrCronNotFound) {
		t.Fatalf("another account's run-now: %v", err)
	}
	e.sessions.setActive(*run.SessionID, false)
	e.starter.setErr(fmt.Errorf("no daemon: %w", ErrCapacity))
	held, err := s.RunNow(ctx, "acct-1", c.ID)
	if err != nil || held.Status != db.CronRunHeld {
		t.Fatalf("held run-now = %+v, %v", held, err)
	}
	if _, err := db.PauseCron(ctx, e.pool, c.ID, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunNow(ctx, "acct-1", c.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("run-now of a paused cron: %v", err)
	}
}

func TestAccountLimits(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 20; i++ {
		e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.Name = fmt.Sprintf("c%d", i) })
	}
	if _, err := e.sched().CreateCron(context.Background(), NewCron{
		Owner: "acct-1", Name: "one too many", Schedule: "0 * * * *", Timezone: "UTC", Prompt: "p",
		TokenID: "t", TokenExpiresAt: e.clock.Now().Add(time.Hour), Enabled: true,
	}); !errors.Is(err, db.ErrCronLimit) {
		t.Fatalf("21st cron: %v, want ErrCronLimit", err)
	}
	if _, err := e.sched().CreateCron(context.Background(), NewCron{
		Owner: "acct-2", Name: "other", Schedule: "0 * * * *", Timezone: "UTC", Prompt: "p",
		TokenID: "t2", TokenExpiresAt: e.clock.Now().Add(time.Hour), Enabled: true,
	}); err != nil {
		t.Fatalf("another account is not limited: %v", err)
	}
}

func TestConcurrentSessionLimitHolds(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.GraceSeconds = ptr(6 * 3600) })
	s := e.sched()
	e.sessions.setConcurrent("acct-1", 2)
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	r := e.runs(c.ID)[0]
	if r.Status != db.CronRunHeld || e.starter.callCount() != 0 {
		t.Fatalf("run = %+v, starter calls %d; over the limit must hold without calling the starter", r, e.starter.callCount())
	}
	e.sessions.setConcurrent("acct-1", 1)
	e.clock.Advance(30 * time.Second)
	tick(t, s)
	if got := e.runs(c.ID)[0]; got.Status != db.CronRunStarted {
		t.Fatalf("run = %+v after capacity freed", got)
	}
}

func TestClaimPausesExpiredTokenAndBadSchedule(t *testing.T) {
	e := newEnv(t)
	exp := e.addCron("0 * * * *", "UTC", func(n *NewCron) {
		n.Name = "expiring"
		n.TokenExpiresAt = e.clock.Now().Add(30 * time.Minute)
	})
	bad := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.Name = "bad" })
	if _, err := e.pool.Exec(context.Background(), `UPDATE crons SET schedule = '@every 1m' WHERE id = $1`, bad.ID); err != nil {
		t.Fatal(err)
	}
	s := e.sched()
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	if e.starter.callCount() != 0 {
		t.Fatal("neither cron may start")
	}
	if r := e.cron(exp.ID).PausedReason; r == nil || !strings.Contains(*r, "expired") {
		t.Fatalf("expired cron paused_reason = %v", r)
	}
	if r := e.cron(bad.ID).PausedReason; r == nil || !strings.Contains(*r, "schedule") {
		t.Fatalf("bad schedule paused_reason = %v", r)
	}
	if len(e.paused) != 2 {
		t.Fatalf("OnPaused fired %d times, want 2", len(e.paused))
	}
}

func TestHeldRunOfPausedCronIsSkipped(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.GraceSeconds = ptr(6 * 3600) })
	s := e.sched()
	e.starter.setErr(fmt.Errorf("busy: %w", ErrCapacity))
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	if _, err := db.PauseCron(context.Background(), e.pool, c.ID, "access revoked"); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	calls := e.starter.callCount()
	tick(t, s)
	r := e.runs(c.ID)[0]
	if r.Status != db.CronRunSkipped || e.starter.callCount() != calls {
		t.Fatalf("run = %+v; a held run of a paused cron is skipped without a start attempt", r)
	}
}

func TestPrunesOldRuns(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	ctx := context.Background()
	old, err := db.InsertManualCronRun(ctx, e.pool, c.ID, e.clock.Now().Add(-100*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.TransitionCronRun(ctx, e.pool, old.ID, []string{db.CronRunClaimed}, db.CronRunSkipped, "x", "", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	recent, _ := db.InsertManualCronRun(ctx, e.pool, c.ID, e.clock.Now().Add(-10*24*time.Hour))
	if _, err := db.TransitionCronRun(ctx, e.pool, recent.ID, []string{db.CronRunClaimed}, db.CronRunSkipped, "x", "", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	tick(t, e.sched())
	if _, err := db.GetCronRun(ctx, e.pool, old.ID); !errors.Is(err, db.ErrCronNotFound) {
		t.Fatalf("100-day-old run survived: %v", err)
	}
	if _, err := db.GetCronRun(ctx, e.pool, recent.ID); err != nil {
		t.Fatalf("10-day-old run pruned: %v", err)
	}
}

func TestCreateCronValidation(t *testing.T) {
	e := newEnv(t)
	s := e.sched()
	base := func() NewCron {
		return NewCron{Owner: "acct-1", Name: "n", Schedule: "0 * * * *", Timezone: "UTC", Prompt: "p",
			TokenID: "t", TokenExpiresAt: e.clock.Now().Add(time.Hour), Enabled: true}
	}
	cases := map[string]func(*NewCron){
		"empty name":       func(n *NewCron) { n.Name = "" },
		"empty prompt":     func(n *NewCron) { n.Prompt = "" },
		"huge prompt":      func(n *NewCron) { n.Prompt = strings.Repeat("x", 16385) },
		"@every":           func(n *NewCron) { n.Schedule = "@every 1h" },
		"tz prefix":        func(n *NewCron) { n.Schedule = "CRON_TZ=UTC 0 * * * *" },
		"too frequent":     func(n *NewCron) { n.Schedule = "*/5 * 1 * *" },
		"never":            func(n *NewCron) { n.Schedule = "0 0 30 2 *" },
		"bad zone":         func(n *NewCron) { n.Timezone = "Nowhere/Land" },
		"bad runtime":      func(n *NewCron) { n.Runtime = "daemon" },
		"no token":         func(n *NewCron) { n.TokenID = "" },
		"expired token":    func(n *NewCron) { n.TokenExpiresAt = e.clock.Now().Add(-time.Hour) },
		"no owner":         func(n *NewCron) { n.Owner = "" },
		"grace too small":  func(n *NewCron) { n.GraceSeconds = ptr(-5) },
		"runtime too long": func(n *NewCron) { n.MaxRuntimeSeconds = 10 * 24 * 3600 },
		"mcp not an array": func(n *NewCron) { n.MCP = []byte(`{"a":1}`) },
	}
	for name, mut := range cases {
		in := base()
		mut(&in)
		if _, err := s.CreateCron(context.Background(), in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if e.count("crons") != 0 {
		t.Fatalf("rejected crons left %d rows", e.count("crons"))
	}
	c, err := s.CreateCron(context.Background(), base())
	if err != nil {
		t.Fatal(err)
	}
	if c.Runtime != "auto" || c.GraceSeconds != 3600 || c.MaxRuntimeSeconds != 1800 || string(c.MCP) != "[]" {
		t.Fatalf("defaults = %+v", c)
	}
	if !c.NextRunAt.Equal(at(2026, 6, 1, 13, 0, 0)) {
		t.Fatalf("next_run_at = %v", c.NextRunAt)
	}
}
