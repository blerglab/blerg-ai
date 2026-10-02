package cron

// Round-2 fixes (G3b): a manual run and a due slot do not overlap, reaper re-drives are capped,
// and run-now works on a pool of one connection (the overlap check runs on the transaction).

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MINOR 29 (a): a run-now that is still open when a scheduled slot comes due makes the slot skip,
// even though the manual run's own slot time ("now") is later than the scheduled slot's.
func TestOpenManualRunMakesADueSlotSkip(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	s := e.sched()
	now := at(2026, 6, 1, 13, 0, 5)
	e.clock.Set(now)
	manual, err := db.InsertManualCronRunGuarded(context.Background(), e.pool, c.ID, now, now.Add(3*time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	tick(t, s)
	var slot *db.CronRun
	for _, r := range e.runs(c.ID) {
		if r.ID != manual.ID {
			slot = &r
		}
	}
	if slot == nil || slot.Status != db.CronRunSkipped || slot.Reason == nil || *slot.Reason != "previous run still active" {
		t.Fatalf("scheduled slot = %+v, want skipped: previous run still active", slot)
	}
	if got := e.starter.callCount(); got != 0 {
		t.Errorf("the starter ran %d times while a manual run was open", got)
	}
}

// MINOR 29 (b): a start that always times out is re-driven a few times and then failed.
func TestReaperCapsRedrivesOfAStartThatNeverFinishes(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	hang := &hookStarter{inner: e.starter,
		before: func(ctx context.Context, _ *db.Cron, _ *db.CronRun) error { <-ctx.Done(); return ctx.Err() },
	}
	s := e.sched(func(cfg *Config) { cfg.Starter = hang; cfg.StartTimeout = 20 * time.Millisecond })
	e.clock.Set(at(2026, 6, 1, 13, 0, 5))
	tick(t, s) // the claim's own start: times out, left for the reaper
	for i := range maxReaperRedrives {
		e.clock.Advance(2 * time.Minute)
		tick(t, s)
		if r := e.runs(c.ID)[0]; r.Status != db.CronRunClaimed {
			t.Fatalf("after re-drive %d the run is %q, want still claimed", i+1, r.Status)
		}
	}
	e.clock.Advance(2 * time.Minute)
	tick(t, s) // one more than the cap
	r := e.runs(c.ID)[0]
	if r.Status != db.CronRunFailed || r.Reason == nil || !strings.Contains(*r.Reason, "attempts") {
		t.Fatalf("run = %+v, want failed after %d re-drives", r, maxReaperRedrives)
	}
}

// MINOR 30: the overlap check of a run-now runs on the transaction, so one connection is enough.
func TestRunNowOnASingleConnectionPool(t *testing.T) {
	e := newEnv(t)
	c := e.addCron("0 * * * *", "UTC")
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.RuntimeParams["search_path"] = "t22_cron"
	one, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	s, err := New(Config{Pool: one, Starter: e.starter, Sessions: e.sessions, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.RunNow(ctx, "acct-1", c.ID)
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
			t.Fatalf("RunNow on a one-connection pool: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d run-nows succeeded, want 1", ok)
	}
}
