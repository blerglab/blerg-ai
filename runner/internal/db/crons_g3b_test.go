package db_test

// Round-2 fixes (G3b) in the cron tables: the manual-run guard's busy callback runs on the
// transaction, an open manual run blocks a scheduled slot, re-drives are counted, deleting a cron
// owes its current token, and the revocation outbox backs off a poisoned row.

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// singleConnPool opens a second pool of ONE connection on the schema cronTestPool built.
func singleConnPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.RuntimeParams["search_path"] = "t22_dbcrons"
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// MINOR 30: busy ran on the pool while the transaction held a connection and the row lock, so with
// a small pool concurrent run-now clicks deadlocked each other. It now runs on the transaction.
func TestInsertManualCronRunGuardedBusyRunsOnTheTransaction(t *testing.T) {
	pool := cronTestPool(t)
	c, err := db.InsertCron(context.Background(), pool, newCron("acct-a", "busytx", t0), 5)
	if err != nil {
		t.Fatal(err)
	}
	one := singleConnPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = db.InsertManualCronRunGuarded(ctx, one, c.ID, t0, t0.Add(3*time.Minute),
				func(ctx context.Context, q db.Querier) (bool, error) {
					// What the scheduler's overlap check reads, on the transaction's connection.
					_, err := db.HasEarlierOpenCronRun(ctx, q, c.ID, "00000000-0000-0000-0000-000000000000", t0.Add(time.Second))
					return false, err
				})
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
			t.Fatalf("a concurrent run-now on a one-connection pool: %v", e)
		}
	}
	if ok != 1 {
		t.Fatalf("%d run-nows succeeded, want exactly 1", ok)
	}
}

// MINOR 29 (a): an open manual run counts as an earlier open run whatever its slot time.
func TestOpenManualRunBlocksAScheduledSlot(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	c, err := db.InsertCron(ctx, pool, newCron("acct-a", "manual-blocks", t0), 5)
	if err != nil {
		t.Fatal(err)
	}
	slot := t0.Add(-time.Minute) // a slot earlier than the manual run's "now"
	run, err := db.InsertManualCronRunGuarded(ctx, pool, c.ID, t0, t0.Add(3*time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	open, err := db.HasEarlierOpenCronRun(ctx, pool, c.ID, "00000000-0000-0000-0000-00000000aaaa", slot)
	if err != nil || !open {
		t.Fatalf("HasEarlierOpenCronRun = %v, %v; want the open manual run to count", open, err)
	}
	if _, err := db.TransitionCronRun(ctx, pool, run.ID, []string{db.CronRunClaimed}, db.CronRunSkipped, "x", "", t0); err != nil {
		t.Fatal(err)
	}
	if open, _ := db.HasEarlierOpenCronRun(ctx, pool, c.ID, "00000000-0000-0000-0000-00000000aaaa", slot); open {
		t.Error("a finished manual run still blocks")
	}
}

// MINOR 29 (b): every re-drive of a run is counted.
func TestBumpCronRunRedrives(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	c, err := db.InsertCron(ctx, pool, newCron("acct-a", "redrive", t0), 5)
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.InsertManualCronRun(ctx, pool, c.ID, t0)
	if err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		got, err := db.BumpCronRunRedrives(ctx, pool, run.ID)
		if err != nil || got != want {
			t.Fatalf("redrive %d: got %d, %v", want, got, err)
		}
	}
}

// MINOR 29 (d): deleting a cron owes its CURRENT token, recorded under the same lock as the delete,
// so a renewal that landed after the caller read the row cannot orphan the new token.
func TestDeleteCronOwesItsCurrentToken(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	c, err := db.InsertCron(ctx, pool, newCron("acct-a", "del-owes", t0), 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE crons SET token_id = 'renewed-token' WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.DeleteCron(ctx, pool, "acct-a", c.ID)
	if err != nil || deleted.TokenID != "renewed-token" {
		t.Fatalf("DeleteCron = %+v, %v", deleted, err)
	}
	owed, err := db.ListTokenRevocations(ctx, pool, 10)
	if err != nil || len(owed) != 1 || owed[0].TokenID != "renewed-token" || owed[0].AccountID != "acct-a" {
		t.Fatalf("owed = %+v, %v; want the current token", owed, err)
	}
}

// MINOR 29 (c): a revocation core keeps refusing backs off and is finally dropped, instead of
// sitting at the head of the outbox forever.
func TestTokenRevocationBackoffAndDrop(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	for _, id := range []string{"poison", "fine"} {
		if err := db.EnqueueTokenRevocation(ctx, pool, "acct-a", id); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	due, err := db.ListDueTokenRevocations(ctx, pool, now, 50)
	if err != nil || len(due) != 2 {
		t.Fatalf("due = %+v, %v", due, err)
	}
	n, dropped, err := db.RecordTokenRevocationFailure(ctx, pool, "poison", now, 3)
	if err != nil || n != 1 || dropped {
		t.Fatalf("first failure = %d, %v, %v", n, dropped, err)
	}
	due, _ = db.ListDueTokenRevocations(ctx, pool, now, 50)
	if len(due) != 1 || due[0].TokenID != "fine" {
		t.Fatalf("due after one failure = %+v, want only the healthy row", due)
	}
	// It is owed again once the backoff has passed, and still listed as owed overall.
	if all, _ := db.ListTokenRevocations(ctx, pool, 10); len(all) != 2 {
		t.Fatalf("owed overall = %+v", all)
	}
	later := now.Add(2 * time.Hour)
	if due, _ = db.ListDueTokenRevocations(ctx, pool, later, 50); len(due) != 2 {
		t.Fatalf("due after the backoff = %+v", due)
	}
	if _, dropped, _ = db.RecordTokenRevocationFailure(ctx, pool, "poison", later, 3); dropped {
		t.Fatal("dropped at the second failure")
	}
	if n, dropped, _ = db.RecordTokenRevocationFailure(ctx, pool, "poison", later.Add(2*time.Hour), 3); !dropped || n != 3 {
		t.Fatalf("third failure = %d, dropped %v; want it dropped at the cap", n, dropped)
	}
	if all, _ := db.ListTokenRevocations(ctx, pool, 10); len(all) != 1 || all[0].TokenID != "fine" {
		t.Fatalf("owed after the drop = %+v", all)
	}
}
