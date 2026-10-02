package db_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// A pause decided on a stale token must not pause a cron that already holds a new one.
func TestPauseCronForTokenIsCompareAndSet(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	c, err := db.InsertCron(ctx, pool, newCron("acct-a", "cas", t0), 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateCron(ctx, pool, "acct-a", c.ID, func(x *db.Cron) error { x.TokenID = "renewed"; return nil }); err != nil {
		t.Fatal(err)
	}
	ok, err := db.PauseCronForToken(ctx, pool, c.ID, c.TokenID, "access revoked")
	if err != nil || ok {
		t.Fatalf("pause on the old token = %v, %v; want false (the cron holds a new token)", ok, err)
	}
	if got, _ := db.GetCron(ctx, pool, c.ID); got.PausedReason != nil {
		t.Fatalf("cron paused: %v", *got.PausedReason)
	}
	ok, err = db.PauseCronForToken(ctx, pool, c.ID, "renewed", "access revoked")
	if err != nil || !ok {
		t.Fatalf("pause on the current token = %v, %v; want true", ok, err)
	}
}

func TestInsertManualCronRunGuarded(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	c, err := db.InsertCron(ctx, pool, newCron("acct-a", "guard", t0), 5)
	if err != nil {
		t.Fatal(err)
	}
	lease := t0.Add(3 * time.Minute)

	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = db.InsertManualCronRunGuarded(ctx, pool, c.ID, t0, lease, nil)
		}()
	}
	wg.Wait()
	ok := 0
	for _, e := range errs {
		switch {
		case e == nil:
			ok++
		case errors.Is(e, db.ErrCronRunOpen):
		default:
			t.Fatal(e)
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent inserts succeeded, want exactly 1", ok)
	}
	if _, err := db.InsertManualCronRunGuarded(ctx, pool, "00000000-0000-0000-0000-000000000000", t0, lease, nil); !errors.Is(err, db.ErrCronNotFound) {
		t.Fatalf("missing cron: %v", err)
	}

	// The busy callback runs under the lock and can refuse.
	runs, _ := db.ListCronRuns(ctx, pool, c.ID, 5)
	if _, err := db.TransitionCronRun(ctx, pool, runs[0].ID, []string{db.CronRunClaimed}, db.CronRunSkipped, "x", "", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertManualCronRunGuarded(ctx, pool, c.ID, t0, lease, func(context.Context, db.Querier) (bool, error) { return true, nil }); !errors.Is(err, db.ErrCronRunOpen) {
		t.Fatalf("busy: %v", err)
	}
}

func TestListReapableSkipsLeasedRuns(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	c, err := db.InsertCron(ctx, pool, newCron("acct-a", "lease", t0), 5)
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.InsertManualCronRunGuarded(ctx, pool, c.ID, t0, t0.Add(150*time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	after, notAfter := t0.Add(-23*time.Hour), t0.Add(-time.Minute)
	// 90 s later the claim is old enough, but its lease is still valid.
	got, err := db.ListReapableClaimedCronRuns(ctx, pool, after.Add(90*time.Second), notAfter.Add(90*time.Second), t0.Add(90*time.Second))
	if err != nil || len(got) != 0 {
		t.Fatalf("leased run listed: %+v, %v", got, err)
	}
	got, err = db.ListReapableClaimedCronRuns(ctx, pool, after.Add(3*time.Minute), notAfter.Add(3*time.Minute), t0.Add(3*time.Minute))
	if err != nil || len(got) != 1 || got[0].ID != run.ID {
		t.Fatalf("lapsed lease not listed: %+v, %v", got, err)
	}
}

func TestTokenRevocationQueue(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	for range 2 { // idempotent
		if err := db.EnqueueTokenRevocation(ctx, pool, "acct-a", "tok-1"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListTokenRevocations(ctx, pool, 10)
	if err != nil || len(got) != 1 || got[0].TokenID != "tok-1" || got[0].AccountID != "acct-a" {
		t.Fatalf("queue = %+v, %v", got, err)
	}
	if err := db.DeleteTokenRevocation(ctx, pool, "tok-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.ListTokenRevocations(ctx, pool, 10); len(got) != 0 {
		t.Fatalf("queue after ack = %+v", got)
	}
}
