package cron

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func ptr[T any](v T) *T { return &v }

// hookStarter wraps the fake Starter so a test can slow, block or fail chosen calls.
type hookStarter struct {
	inner *fakeStarter
	// before runs first with the call's context; a non-nil return is the Start result (no session).
	before func(ctx context.Context, c *db.Cron, run *db.CronRun) error
	// after runs once the inner start succeeded (the session and key exist); its error replaces the
	// result, as a start that did its work and then hit its deadline would.
	after func(ctx context.Context, sid string) error
}

func (h *hookStarter) Start(ctx context.Context, c *db.Cron, run *db.CronRun) (string, error) {
	if h.before != nil {
		if err := h.before(ctx, c, run); err != nil {
			return "", err
		}
	}
	// The real start's writes are not cancelled by the child deadline of a slow caller.
	sid, err := h.inner.Start(context.WithoutCancel(ctx), c, run)
	if err != nil {
		return "", err
	}
	if h.after != nil {
		if err := h.after(ctx, sid); err != nil {
			return "", err
		}
	}
	return sid, nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// MAJOR 3: a Start that outlasts its child deadline but whose session came to exist is left `claimed`
// for the reaper, which adopts the session. It is not failed and does not count toward the pause.
func TestSlowStartPastDeadlineIsAdoptedNotFailed(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	slow := &hookStarter{inner: e.starter,
		before: func(ctx context.Context, _ *db.Cron, _ *db.CronRun) error { <-ctx.Done(); return nil },
		after:  func(ctx context.Context, _ string) error { return fmt.Errorf("start: %w", ctx.Err()) },
	}
	s := e.sched(func(cfg *Config) { cfg.Starter = slow; cfg.StartTimeout = 50 * time.Millisecond })
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)

	runs := e.runs(c.ID)
	if len(runs) != 1 || runs[0].Status != db.CronRunClaimed {
		t.Fatalf("runs = %+v, want the run left claimed for the reaper", runs)
	}
	if got := e.cron(c.ID); got.ConsecutiveFailures != 0 || got.PausedReason != nil {
		t.Fatalf("cron = failures %d paused %v; a deadline is not a failed run", got.ConsecutiveFailures, got.PausedReason)
	}
	e.clock.Advance(2 * time.Minute)
	tick(t, s)
	got := e.runs(c.ID)[0]
	if got.Status != db.CronRunStarted || got.SessionID == nil {
		t.Fatalf("run = %+v, want the reaper to adopt the session", got)
	}
	if e.count("sessions") != 1 || e.cron(c.ID).ConsecutiveFailures != 0 {
		t.Fatalf("sessions %d failures %d", e.count("sessions"), e.cron(c.ID).ConsecutiveFailures)
	}
}

// Any error while the run's key already exists is not a failure either: the session may exist.
func TestErrorWhileKeyExistsLeavesRunClaimed(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	late := &hookStarter{inner: e.starter,
		after: func(context.Context, string) error { return errors.New("late boom") },
	}
	s := e.sched(func(cfg *Config) { cfg.Starter = late })
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	if r := e.runs(c.ID)[0]; r.Status != db.CronRunClaimed {
		t.Fatalf("run = %+v, want claimed", r)
	}
	if got := e.cron(c.ID).ConsecutiveFailures; got != 0 {
		t.Fatalf("failures = %d", got)
	}
	e.clock.Advance(2 * time.Minute)
	tick(t, s)
	if r := e.runs(c.ID)[0]; r.Status != db.CronRunStarted || e.count("sessions") != 1 {
		t.Fatalf("run = %+v sessions %d", r, e.count("sessions"))
	}
}

// A plain deadline with nothing written is left for the reaper too (it may have started elsewhere).
func TestDeadlineWithNoSessionYetIsNotCounted(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	hang := &hookStarter{inner: e.starter,
		before: func(ctx context.Context, _ *db.Cron, _ *db.CronRun) error { <-ctx.Done(); return ctx.Err() },
	}
	s := e.sched(func(cfg *Config) { cfg.Starter = hang; cfg.StartTimeout = 30 * time.Millisecond })
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	if r := e.runs(c.ID)[0]; r.Status != db.CronRunClaimed || e.cron(c.ID).ConsecutiveFailures != 0 {
		t.Fatalf("run = %+v failures %d", r, e.cron(c.ID).ConsecutiveFailures)
	}
}

// MAJOR 4: a manual start still in flight past the reaper's age is not re-driven.
func TestManualRunInFlightIsNotReaped(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	gate := &hookStarter{inner: e.starter, before: func(ctx context.Context, _ *db.Cron, _ *db.CronRun) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}}
	manual := e.sched(func(cfg *Config) { cfg.Starter = gate })
	done := make(chan error, 1)
	go func() { _, err := manual.RunNow(context.Background(), "acct-1", c.ID); done <- err }()
	<-entered

	// The reaper sees a manual claim older than 60 s while its start is still running.
	e.clock.Advance(90 * time.Second)
	tick(t, e.sched())
	if n := e.starter.callCount(); n != 0 {
		t.Fatalf("starter calls = %d while the manual start was still blocked; the reaper re-drove an in-flight manual start", n)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if e.count("sessions") != 1 {
		t.Fatalf("sessions = %d, want 1", e.count("sessions"))
	}
	if r := e.runs(c.ID)[0]; r.Status != db.CronRunStarted {
		t.Fatalf("run = %+v", r)
	}
}

// After its lease lapses (the process died mid-start) a manual claim is reaped like any other.
func TestManualRunWithLapsedLeaseIsReaped(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	ctx := context.Background()
	run, err := db.InsertManualCronRunGuarded(ctx, e.pool, c.ID, e.clock.Now(), e.clock.Now().Add(2*time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(90 * time.Second)
	tick(t, s)
	if e.starter.callCount() != 0 {
		t.Fatal("re-drove a manual claim whose lease is still valid")
	}
	e.clock.Advance(2 * time.Minute)
	tick(t, s)
	got, _ := db.GetCronRun(ctx, e.pool, run.ID)
	if got.Status != db.CronRunStarted {
		t.Fatalf("run = %+v, want reaped and started once the lease lapsed", got)
	}
}

// MINOR 6: a double click (concurrent run-now) starts one session; the rest are told it is running.
func TestConcurrentRunNowStartsOneSession(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	slow := &hookStarter{inner: e.starter, before: func(context.Context, *db.Cron, *db.CronRun) error {
		time.Sleep(150 * time.Millisecond)
		return nil
	}}
	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each click is its own request: its own management scheduler.
			_, errs[i] = e.sched(func(cfg *Config) { cfg.Starter = slow }).RunNow(context.Background(), "acct-1", c.ID)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrAlreadyRunning):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || e.count("sessions") != 1 || len(e.runs(c.ID)) != 1 {
		t.Fatalf("succeeded %d, sessions %d, runs %d; want exactly one of each", ok, e.count("sessions"), len(e.runs(c.ID)))
	}
}

// The account's concurrent-session limit is checked and used atomically: two crons of one account
// firing together never exceed it.
func TestConcurrentLimitIsAtomicAcrossCrons(t *testing.T) {
	e := newEnv(t)
	a := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.Name = "a" })
	b := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.Name = "b" })
	cc := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.Name = "c" })
	var mu sync.Mutex
	started := 0
	sess := &countingSessions{started: func() int { mu.Lock(); defer mu.Unlock(); return started }}
	st := &hookStarter{inner: e.starter, before: func(context.Context, *db.Cron, *db.CronRun) error {
		time.Sleep(100 * time.Millisecond)
		return nil
	}, after: func(context.Context, string) error { mu.Lock(); started++; mu.Unlock(); return nil }}
	s := e.sched(func(cfg *Config) { cfg.Starter = st; cfg.Sessions = sess; cfg.MaxConcurrentPerAccount = 2 })
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	n := 0
	for _, id := range []string{a.ID, b.ID, cc.ID} {
		for _, r := range e.runs(id) {
			if r.Status == db.CronRunStarted {
				n++
			}
		}
	}
	if n != 2 {
		t.Fatalf("%d runs started, want exactly the limit of 2 (the third held)", n)
	}
}

// countingSessions reports the started count as the account's concurrent sessions.
type countingSessions struct {
	started func() int
}

func (c *countingSessions) SessionActive(context.Context, string) bool { return false }
func (c *countingSessions) ConcurrentActive(context.Context, string) int {
	return c.started()
}

// MINOR 5: manual-run failures never count toward the automatic pause.
func TestManualRunFailuresDoNotCountTowardPause(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	e.starter.setErr(errors.New("boom"))
	for range 4 {
		run, err := s.RunNow(context.Background(), "acct-1", c.ID)
		if err != nil || run.Status != db.CronRunFailed {
			t.Fatalf("run = %+v, %v", run, err)
		}
	}
	got := e.cron(c.ID)
	if got.ConsecutiveFailures != 0 || got.PausedReason != nil {
		t.Fatalf("cron = failures %d paused %v; manual failures must not count", got.ConsecutiveFailures, got.PausedReason)
	}
	select {
	case msg := <-e.paused:
		t.Fatalf("OnPaused fired: %s", msg)
	default:
	}
}

// MINOR 12a: one hung start does not delay another cron's start.
func TestHungStartDoesNotBlockOtherCrons(t *testing.T) {
	e := newEnv(t)
	hung := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.Name = "hung"; n.Owner = "acct-1" })
	other := e.addCron("0 * * * *", "UTC", func(n *NewCron) { n.Name = "other"; n.Owner = "acct-2" })
	release := make(chan struct{})
	st := &hookStarter{inner: e.starter, before: func(ctx context.Context, c *db.Cron, _ *db.CronRun) error {
		if c.ID == hung.ID {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return nil
	}}
	s := e.sched(func(cfg *Config) { cfg.Starter = st })
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	done := make(chan error, 1)
	go func() { done <- s.Tick(context.Background()) }()
	waitFor(t, "the other cron to start while the first hangs", func() bool {
		rs, _ := db.ListCronRuns(context.Background(), e.pool, other.ID, 5)
		return len(rs) == 1 && rs[0].Status == db.CronRunStarted
	})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r := e.runs(hung.ID)[0]; r.Status != db.CronRunStarted {
		t.Fatalf("hung cron run = %+v after release", r)
	}
}

// MAJOR 5: the idempotency scope belongs to the cron, not its token: renewing between the start
// and the reaper leaves the reaper looking in the same place the Starter wrote.
func TestIdempotencyScopeSurvivesRenewal(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	claims, err := s.claimDue(context.Background(), e.clock.Now())
	if err != nil || len(claims) != 1 {
		t.Fatalf("claimDue = %d, %v", len(claims), err)
	}
	run := claims[0].Run
	sid, err := e.starter.Start(context.Background(), &claims[0].Cron, &run) // starts, then "crashes"
	if err != nil {
		t.Fatal(err)
	}
	e.starter.mu.Lock()
	e.starter.calls = nil
	e.starter.mu.Unlock()

	// The token is swapped underneath (a renew after the run finished starting).
	if _, err := db.UpdateCron(context.Background(), e.pool, "acct-1", c.ID, func(x *db.Cron) error {
		x.TokenID, x.TokenExpiresAt = "renewed-token", e.clock.Now().Add(365*24*time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old, renewed := *c, *e.cron(c.ID)
	if s.cfg.IdempotencyScope(&old) != s.cfg.IdempotencyScope(&renewed) {
		t.Fatalf("scope changed with the token: %q vs %q", s.cfg.IdempotencyScope(&old), s.cfg.IdempotencyScope(&renewed))
	}
	e.clock.Advance(2 * time.Minute)
	tick(t, e.sched())
	got, _ := db.GetCronRun(context.Background(), e.pool, run.ID)
	if got.Status != db.CronRunStarted || got.SessionID == nil || *got.SessionID != sid || e.starter.callCount() != 0 {
		t.Fatalf("run = %+v calls %d, want the original session %s adopted", got, e.starter.callCount(), sid)
	}
}

// MAJOR 5 and MINOR 9: a token swap is a compare-and-set that refuses while a run is claimed, and
// only then (a started or held run does not block it).
func TestTokenSwapCompareAndSet(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	ctx := context.Background()
	exp := e.clock.Now().Add(365 * 24 * time.Hour)
	swap := func(expect string) error {
		_, err := s.UpdateCron(ctx, "acct-1", c.ID, CronPatch{
			TokenID: ptr("t-new-" + expect), TokenExpiresAt: &exp, ExpectTokenID: expect, RefuseWhileClaimed: true,
		})
		return err
	}
	// A run claimed but not yet started: refused.
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	claims, err := s.claimDue(ctx, e.clock.Now())
	if err != nil || len(claims) != 1 {
		t.Fatalf("claimDue = %d, %v", len(claims), err)
	}
	if err := swap(c.TokenID); !errors.Is(err, ErrRunOpen) {
		t.Fatalf("swap while claimed = %v, want ErrRunOpen", err)
	}
	// Held: not blocked (nothing was written under the token).
	if _, err := db.TransitionCronRun(ctx, e.pool, claims[0].Run.ID, []string{db.CronRunClaimed}, db.CronRunHeld, "cap", "", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	// The stale token id is refused.
	if err := swap("not-the-current-token"); !errors.Is(err, ErrConflict) {
		t.Fatalf("swap with a stale expectation = %v, want ErrConflict", err)
	}
	if err := swap(c.TokenID); err != nil {
		t.Fatalf("swap while merely held: %v", err)
	}
	if got := e.cron(c.ID).TokenID; got != "t-new-"+c.TokenID {
		t.Fatalf("token id = %q", got)
	}
}

// MINOR 10: a disabled cron does not fire, and enabling it recomputes without firing; pausing is separate.
func TestDisabledCronDoesNotFire(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	ctx := context.Background()
	if _, err := s.UpdateCron(ctx, "acct-1", c.ID, CronPatch{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s)
	if len(e.runs(c.ID)) != 0 || e.starter.callCount() != 0 {
		t.Fatal("a disabled cron fired")
	}
	if got := e.cron(c.ID); got.Enabled || got.PausedReason != nil || got.TokenID != c.TokenID {
		t.Fatalf("disable must not pause or touch the token: %+v", got)
	}
	upd, err := s.UpdateCron(ctx, "acct-1", c.ID, CronPatch{Enabled: ptr(true)})
	if err != nil || !upd.NextRunAt.After(e.clock.Now()) {
		t.Fatalf("enable: %v next %v", err, upd)
	}
	tick(t, s)
	if len(e.runs(c.ID)) != 0 {
		t.Fatal("enabling fired immediately")
	}
}

// A claimed run whose cron was disabled meanwhile is skipped by the reaper.
func TestReaperSkipsRunOfDisabledCron(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	if _, err := s.claimDue(context.Background(), e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateCron(context.Background(), "acct-1", c.ID, CronPatch{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Minute)
	tick(t, s)
	if r := e.runs(c.ID)[0]; r.Status != db.CronRunSkipped || e.starter.callCount() != 0 {
		t.Fatalf("run = %+v calls %d", r, e.starter.callCount())
	}
}

// MAJOR 6: an explicit grace of 0 is kept on create; only an absent one defaults.
func TestCreateHonoursExplicitZeroGrace(t *testing.T) {
	e := newEnv(t)
	s := e.sched()
	in := func(g *int) NewCron {
		return NewCron{Owner: "acct-1", Name: "n", Schedule: "0 * * * *", Timezone: "UTC", Prompt: "p",
			TokenID: "t", TokenExpiresAt: e.clock.Now().Add(time.Hour), Enabled: true, GraceSeconds: g}
	}
	zero, err := s.CreateCron(context.Background(), in(ptr(0)))
	if err != nil || zero.GraceSeconds != 0 {
		t.Fatalf("explicit 0: %+v, %v", zero, err)
	}
	def, err := s.CreateCron(context.Background(), in(nil))
	if err != nil || def.GraceSeconds != DefaultGraceSeconds {
		t.Fatalf("absent: %+v, %v", def, err)
	}
}
