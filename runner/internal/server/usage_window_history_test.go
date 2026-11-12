package server

import (
	"context"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// TestUsageSampleHookRecordsTheWeeklyWindow exercises handleUsageLimits end to end: a daemon's
// "7d" report must reach the database through the registered hub.usageSample hook, the same way
// checkUsageNotices already does for its own hook.
func TestUsageSampleHookRecordsTheWeeklyWindow(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.mu.Lock()
	fx.hub.usageSample = fx.api.onUsageSample
	fx.hub.mu.Unlock()
	reset := time.Now().UTC().Add(6 * 24 * time.Hour)
	handleUsageLimits(fx.hub, fx.dc, protocol.UsageLimits{Provider: "claude", Windows: []protocol.UsageWindow{
		{ID: "7d", UsedPercent: 37, ResetsAt: reset}, {ID: "5h", UsedPercent: 90, ResetsAt: reset},
	}, FetchedAt: time.Now()})
	waitForCondition(t, func() bool {
		got, err := db.ListOwnerUsageWindowSamples(context.Background(), fx.pool, "owner-1", "claude", "7d", reset.Add(-8*24*time.Hour))
		return err == nil && len(got) == 1
	})
	got, err := db.ListOwnerUsageWindowSamples(context.Background(), fx.pool, "owner-1", "claude", "7d", reset.Add(-8*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UsedPercent != 37 || got[0].ResetsAt.Sub(reset).Abs() > time.Millisecond {
		t.Fatalf("recorded samples = %+v, want resets_at %v", got, reset)
	}
}

// waitForCondition polls briefly for the fire-and-forget recording goroutine, instead of sleeping
// a fixed guess.
func waitForCondition(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestInsightsUsageBudgetIsScopedToTheDaemonsOwner checks the data Insights Spending's burndown
// reads: an owner's weekly-window samples appear in their own /api/insights response, and are not
// visible to another account even when both can see /api/insights.
func TestInsightsUsageBudgetIsScopedToTheDaemonsOwner(t *testing.T) {
	f := newInsightsFixture(t)
	ctx := context.Background()
	const daemon = "00000000-0000-4000-8000-0000000000da"
	if _, err := f.pool.Exec(ctx, `UPDATE daemons SET desktop_owner_id='alice' WHERE id=$1`, daemon); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-2 * time.Hour)
	resetsAt := start.Add(7 * 24 * time.Hour)
	if err := db.RecordUsageWindowSample(ctx, f.pool, daemon, "claude", "7d", 42, start, resetsAt, start); err != nil {
		t.Fatal(err)
	}
	// A metered engine (codex, here) has no sample at all: it keeps dollars, no percent line.
	alice := f.insights(t, "/api/insights?range=7d", f.mint("alice", "session.start"))
	if len(alice.UsageBudget) != 1 || alice.UsageBudget[0].Provider != "claude" || alice.UsageBudget[0].UsedPercent != 42 {
		t.Fatalf("alice's usage budget = %+v", alice.UsageBudget)
	}
	bob := f.insights(t, "/api/insights?range=7d", f.mint("bob", "session.start"))
	if len(bob.UsageBudget) != 0 {
		t.Fatalf("bob must not see alice's daemon's samples: %+v", bob.UsageBudget)
	}
}
