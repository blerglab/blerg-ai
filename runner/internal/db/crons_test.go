package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// cronTestPool is a multi-connection pool on its own schema (so it cannot
// collide with the other db tests) with every migration applied.
func cronTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	ctx := context.Background()
	const schema = "t22_dbcrons"
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
	cfg.MaxConns = 8
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
	return pool
}

var t0 = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func newCron(owner, name string, next time.Time) db.Cron {
	return db.Cron{
		OwnerAccountID: owner, Name: name, Enabled: true,
		Schedule: "0 * * * *", Timezone: "UTC", Prompt: "do it", Runtime: "auto",
		MCP: []byte("[]"), TokenID: "tok-" + name, TokenExpiresAt: t0.Add(24 * time.Hour),
		GraceSeconds: 3600, MaxRuntimeSeconds: 1800, NextRunAt: next,
	}
}

func TestCronCRUDAndLimit(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()

	a1, err := db.InsertCron(ctx, pool, newCron("acct-a", "one", t0), 2)
	if err != nil {
		t.Fatalf("InsertCron: %v", err)
	}
	if a1.ID == "" || a1.Engine != "claude" || string(a1.MCP) != "[]" {
		t.Fatalf("unexpected row: %+v", a1)
	}
	if _, err := db.InsertCron(ctx, pool, newCron("acct-a", "two", t0), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCron(ctx, pool, newCron("acct-a", "three", t0), 2); !errors.Is(err, db.ErrCronLimit) {
		t.Fatalf("third cron: %v, want ErrCronLimit", err)
	}
	if _, err := db.InsertCron(ctx, pool, newCron("acct-b", "other", t0), 2); err != nil {
		t.Fatalf("other account is not limited by acct-a: %v", err)
	}

	// Concurrent creates cannot overshoot the limit.
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := db.InsertCron(ctx, pool, newCron("acct-c", fmt.Sprintf("c%d", i), t0), 3); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if ok != 3 {
		t.Fatalf("concurrent creates succeeded %d times, want exactly 3", ok)
	}

	list, err := db.ListCrons(ctx, pool, "acct-a")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListCrons = %d, %v", len(list), err)
	}

	if _, err := db.GetOwnedCron(ctx, pool, "acct-b", a1.ID); !errors.Is(err, db.ErrCronNotFound) {
		t.Fatalf("another account's cron: %v, want ErrCronNotFound", err)
	}
	if _, err := db.GetOwnedCron(ctx, pool, "acct-a", "00000000-0000-0000-0000-000000000000"); !errors.Is(err, db.ErrCronNotFound) {
		t.Fatalf("missing cron: %v", err)
	}
	if _, err := db.GetOwnedCron(ctx, pool, "acct-a", "not-a-uuid"); !errors.Is(err, db.ErrCronNotFound) {
		t.Fatalf("malformed id must be not-found, not an error: %v", err)
	}

	upd, err := db.UpdateCron(ctx, pool, "acct-a", a1.ID, func(c *db.Cron) error {
		c.Name = "renamed"
		c.Enabled = false
		return nil
	})
	if err != nil || upd.Name != "renamed" || upd.Enabled || !upd.UpdatedAt.After(a1.UpdatedAt) {
		t.Fatalf("UpdateCron = %+v, %v", upd, err)
	}
	boom := errors.New("boom")
	if _, err := db.UpdateCron(ctx, pool, "acct-a", a1.ID, func(*db.Cron) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("mutator error must propagate: %v", err)
	}
	if _, err := db.UpdateCron(ctx, pool, "acct-b", a1.ID, func(*db.Cron) error { return nil }); !errors.Is(err, db.ErrCronNotFound) {
		t.Fatalf("cross-account update: %v", err)
	}

	run, err := db.InsertManualCronRun(ctx, pool, a1.ID, t0)
	if err != nil || !run.Manual || run.Status != db.CronRunClaimed {
		t.Fatalf("manual run = %+v, %v", run, err)
	}
	if _, err := db.DeleteCron(ctx, pool, "acct-b", a1.ID); !errors.Is(err, db.ErrCronNotFound) {
		t.Fatalf("cross-account delete: %v", err)
	}
	del, err := db.DeleteCron(ctx, pool, "acct-a", a1.ID)
	if err != nil || del.TokenID != a1.TokenID {
		t.Fatalf("DeleteCron = %+v, %v (must return the row so its token can be revoked)", del, err)
	}
	if _, err := db.GetCronRun(ctx, pool, run.ID); !errors.Is(err, db.ErrCronNotFound) {
		t.Fatalf("runs must cascade with the cron: %v", err)
	}
}

func TestClaimDueCrons(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	slot := t0.Add(-2 * time.Hour)

	due, _ := db.InsertCron(ctx, pool, newCron("a", "due", slot), 20)
	future, _ := db.InsertCron(ctx, pool, newCron("a", "future", t0.Add(time.Hour)), 20)
	disabledC := newCron("a", "disabled", slot)
	disabledC.Enabled = false
	disabled, _ := db.InsertCron(ctx, pool, disabledC, 20)
	pausedC := newCron("a", "paused", slot)
	reason := "three failures"
	pausedC.PausedReason = &reason
	paused, _ := db.InsertCron(ctx, pool, pausedC, 20)
	recent, _ := db.InsertCron(ctx, pool, newCron("a", "recent", t0.Add(-30*time.Second)), 20)
	expiring, _ := db.InsertCron(ctx, pool, newCron("a", "expiring", slot), 20)

	next := t0.Add(45 * time.Minute)
	claims, err := db.ClaimDueCrons(ctx, pool, t0, 50, func(c *db.Cron) db.ClaimPlan {
		if c.ID == expiring.ID {
			return db.ClaimPlan{PauseReason: "token expired"}
		}
		return db.ClaimPlan{Next: next}
	})
	if err != nil {
		t.Fatalf("ClaimDueCrons: %v", err)
	}
	got := map[string]db.CronClaim{}
	for _, c := range claims {
		got[c.Cron.ID] = c
	}
	if len(claims) != 2 || got[due.ID].Run.ID == "" || got[recent.ID].Run.ID == "" {
		t.Fatalf("claimed %d crons, want due and recent only: %+v", len(claims), claims)
	}
	for _, id := range []string{future.ID, disabled.ID, paused.ID} {
		if _, ok := got[id]; ok {
			t.Fatalf("cron %s must not be claimed", id)
		}
	}
	d := got[due.ID]
	if d.Run.Status != db.CronRunClaimed || !d.Run.Late || !d.Run.ScheduledFor.Equal(slot) || d.Run.Manual {
		t.Fatalf("due run = %+v, want claimed, late, scheduled for the slot", d.Run)
	}
	if got[recent.ID].Run.Late {
		t.Fatal("a claim 30s after the slot is not late")
	}
	after, _ := db.GetCron(ctx, pool, due.ID)
	if !after.NextRunAt.Equal(next) || after.LastRunAt == nil || !after.LastRunAt.Equal(t0) {
		t.Fatalf("cron after claim: next=%v last=%v", after.NextRunAt, after.LastRunAt)
	}
	exp, _ := db.GetCron(ctx, pool, expiring.ID)
	if exp.PausedReason == nil || *exp.PausedReason != "token expired" {
		t.Fatalf("expiring cron paused_reason = %v", exp.PausedReason)
	}

	again, err := db.ClaimDueCrons(ctx, pool, t0, 50, func(*db.Cron) db.ClaimPlan { return db.ClaimPlan{Next: next} })
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim = %d, %v; a claimed slot must not be claimed twice", len(again), err)
	}
}

// Concurrent claimers (two schedulers, or a tick racing a tick) split the due
// crons between them: every slot is claimed exactly once.
func TestClaimDueCronsConcurrentClaimsOnce(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	const n = 12
	for i := 0; i < n; i++ {
		if _, err := db.InsertCron(ctx, pool, newCron("a", fmt.Sprintf("c%d", i), t0.Add(-time.Hour)), 100); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]int{}
	start := make(chan struct{})
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claims, err := db.ClaimDueCrons(ctx, pool, t0, 3, func(*db.Cron) db.ClaimPlan {
				return db.ClaimPlan{Next: t0.Add(time.Hour)}
			})
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			mu.Lock()
			for _, c := range claims {
				seen[c.Cron.ID]++
			}
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("cron %s claimed %d times", id, c)
		}
	}
	// Drain the rest in one go: every cron ends up with exactly one run.
	if _, err := db.ClaimDueCrons(ctx, pool, t0, 50, func(*db.Cron) db.ClaimPlan { return db.ClaimPlan{Next: t0.Add(time.Hour)} }); err != nil {
		t.Fatal(err)
	}
	var runs, distinct int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT cron_id) FROM cron_runs`).Scan(&runs, &distinct); err != nil {
		t.Fatal(err)
	}
	if runs != n || distinct != n {
		t.Fatalf("runs=%d distinct crons=%d, want %d each", runs, distinct, n)
	}
}

func TestCronRunTransitionsAndQueries(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	c, _ := db.InsertCron(ctx, pool, newCron("a", "x", t0), 20)

	r, err := db.InsertManualCronRun(ctx, pool, c.ID, t0)
	if err != nil {
		t.Fatal(err)
	}
	// claimed -> held -> started, guarded by the expected source status.
	if ok, err := db.TransitionCronRun(ctx, pool, r.ID, []string{db.CronRunClaimed}, db.CronRunHeld, "waiting for capacity", "", t0); err != nil || !ok {
		t.Fatalf("claimed->held: %v %v", ok, err)
	}
	if ok, _ := db.TransitionCronRun(ctx, pool, r.ID, []string{db.CronRunClaimed}, db.CronRunFailed, "late", "", t0); ok {
		t.Fatal("a transition from the wrong source status must not apply")
	}
	held, err := db.ListCronRunsByStatus(ctx, pool, db.CronRunHeld)
	if err != nil || len(held) != 1 || held[0].Reason == nil || *held[0].Reason != "waiting for capacity" {
		t.Fatalf("held = %+v, %v", held, err)
	}
	sid := "11111111-1111-1111-1111-111111111111"
	if ok, err := db.TransitionCronRun(ctx, pool, r.ID, []string{db.CronRunClaimed, db.CronRunHeld}, db.CronRunStarted, "", sid, t0.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("held->started: %v %v", ok, err)
	}
	got, _ := db.GetCronRun(ctx, pool, r.ID)
	if got.Status != db.CronRunStarted || got.SessionID == nil || *got.SessionID != sid || got.StartedAt == nil || got.Reason != nil {
		t.Fatalf("started run = %+v", got)
	}
	latest, err := db.LatestStartedCronRun(ctx, pool, c.ID, "")
	if err != nil || latest == nil || latest.ID != r.ID {
		t.Fatalf("LatestStartedCronRun = %+v, %v", latest, err)
	}
	none, err := db.LatestStartedCronRun(ctx, pool, c.ID, r.ID)
	if err != nil || none != nil {
		t.Fatalf("excluding the run: %+v, %v", none, err)
	}

	// Earlier open run detection.
	early, _ := db.InsertManualCronRun(ctx, pool, c.ID, t0.Add(-time.Hour))
	later := db.CronRun{ID: "22222222-2222-2222-2222-222222222222", CronID: c.ID, ScheduledFor: t0}
	open, err := db.HasEarlierOpenCronRun(ctx, pool, later.CronID, later.ID, later.ScheduledFor)
	if err != nil || !open {
		t.Fatalf("HasEarlierOpenCronRun = %v, %v, want true (claimed %s)", open, err, early.ID)
	}
	if _, err := db.TransitionCronRun(ctx, pool, early.ID, []string{db.CronRunClaimed}, db.CronRunSkipped, "x", "", t0); err != nil {
		t.Fatal(err)
	}
	if open, _ := db.HasEarlierOpenCronRun(ctx, pool, later.CronID, later.ID, later.ScheduledFor); open {
		t.Fatal("skipped runs are not open")
	}

	// Stale claim queries: rows between the bounds are reap candidates, older ones expire.
	mk := func(age time.Duration) string {
		row, err := db.InsertManualCronRun(ctx, pool, c.ID, t0.Add(-age))
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	fresh, mid, old := mk(10*time.Second), mk(5*time.Minute), mk(30*time.Hour)
	cands, err := db.ListClaimedCronRunsBetween(ctx, pool, t0.Add(-23*time.Hour), t0.Add(-time.Minute))
	if err != nil || len(cands) != 1 || cands[0].ID != mid {
		t.Fatalf("reap candidates = %+v, %v (fresh %s mid %s old %s)", cands, err, fresh, mid, old)
	}
	n, err := db.FailClaimedCronRunsBefore(ctx, pool, t0.Add(-23*time.Hour), "claim expired")
	if err != nil || n != 1 {
		t.Fatalf("FailClaimedCronRunsBefore = %d, %v", n, err)
	}
	if r, _ := db.GetCronRun(ctx, pool, old); r.Status != db.CronRunFailed {
		t.Fatalf("old claim = %s", r.Status)
	}

	list, err := db.ListCronRuns(ctx, pool, c.ID, 2)
	if err != nil || len(list) != 2 || list[0].ScheduledFor.Before(list[1].ScheduledFor) {
		t.Fatalf("ListCronRuns newest first, limited: %+v, %v", list, err)
	}

	// Pruning removes by claimed_at.
	if _, err := pool.Exec(ctx, `UPDATE cron_runs SET claimed_at = $2 WHERE id = $1`, old, t0.Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	pruned, err := db.PruneCronRuns(ctx, pool, t0.Add(-90*24*time.Hour))
	if err != nil || pruned != 1 {
		t.Fatalf("PruneCronRuns = %d, %v", pruned, err)
	}
}

func TestCronFailureCountAndPause(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	c, _ := db.InsertCron(ctx, pool, newCron("a", "x", t0), 20)
	for i := 1; i <= 3; i++ {
		f, paused, err := db.RecordCronFailure(ctx, pool, c.ID, 3, "3 runs failed in a row")
		if err != nil || f != i || paused != (i == 3) {
			t.Fatalf("failure %d: count=%d paused=%v err=%v", i, f, paused, err)
		}
	}
	got, _ := db.GetCron(ctx, pool, c.ID)
	if got.PausedReason == nil || *got.PausedReason != "3 runs failed in a row" {
		t.Fatalf("paused_reason = %v", got.PausedReason)
	}
	// A cron already paused keeps its first reason and is not reported paused again.
	_, paused, _ := db.RecordCronFailure(ctx, pool, c.ID, 3, "another")
	got, _ = db.GetCron(ctx, pool, c.ID)
	if paused || *got.PausedReason != "3 runs failed in a row" {
		t.Fatalf("second pause: paused=%v reason=%v", paused, *got.PausedReason)
	}
	if err := db.ResetCronFailures(ctx, pool, c.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetCron(ctx, pool, c.ID)
	if got.ConsecutiveFailures != 0 {
		t.Fatalf("failures after reset = %d", got.ConsecutiveFailures)
	}
	ok, err := db.PauseCron(ctx, pool, c.ID, "access revoked")
	if err != nil || ok {
		t.Fatalf("PauseCron on an already-paused cron = %v %v, want false", ok, err)
	}
}

func TestGetIdempotencyKey(t *testing.T) {
	pool := cronTestPool(t)
	ctx := context.Background()
	if row, err := db.GetIdempotencyKey(ctx, pool, "agent:t", "cron:x:y"); err != nil || row != nil {
		t.Fatalf("absent key = %+v, %v", row, err)
	}
	sid := "33333333-3333-3333-3333-333333333333"
	if _, claimed, err := db.ClaimIdempotencyKey(ctx, pool, "agent:t", "cron:x:y", "h", sid, time.Hour); err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	row, err := db.GetIdempotencyKey(ctx, pool, "agent:t", "cron:x:y")
	if err != nil || row == nil || row.SessionID != sid {
		t.Fatalf("GetIdempotencyKey = %+v, %v", row, err)
	}
}
