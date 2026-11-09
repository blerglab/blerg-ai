package releaseperf

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	rdb "github.com/blerglab/blerg-ai/runner/internal/db"
)

func migratedRunnerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := adminDSN(t)
	ctx := context.Background()
	name := "blerg_release_perf_test_seed"
	dsn, err := CreateDisposableDatabase(ctx, admin, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DropDatabase(context.Background(), admin, name) })
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := rdb.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestSeedRunnerDatasetCounts(t *testing.T) {
	pool := migratedRunnerPool(t)
	ctx := context.Background()
	summary, err := SeedRunnerDataset(ctx, pool, "release-perf-account")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Workspaces < 25 {
		t.Fatalf("expected at least 25 seeded workspaces, got %d", summary.Workspaces)
	}
	if summary.SwitchTargetSessionID == "" {
		t.Fatal("expected a second workspace id for the workspace-switch scenario to switch to")
	}
	if summary.SwitchTargetSessionID == summary.LongTranscriptSessionID {
		t.Fatal("expected the switch target to be a different workspace than the long-transcript one")
	}
	if summary.LongTranscriptEvents != 8000 {
		t.Fatalf("expected 8000 long-transcript events, got %d", summary.LongTranscriptEvents)
	}
	if summary.InsightsConversations != 420 {
		t.Fatalf("expected 420 seeded insights conversations, got %d", summary.InsightsConversations)
	}
	if summary.InsightsEvents != 168000 {
		t.Fatalf("expected 168000 seeded insights events, got %d", summary.InsightsEvents)
	}
	if summary.InsightsCardRefs != 1000 {
		t.Fatalf("expected 1000 seeded card references, got %d", summary.InsightsCardRefs)
	}

	var longCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM agent_events WHERE session_id = $1", summary.LongTranscriptSessionID).Scan(&longCount); err != nil {
		t.Fatal(err)
	}

	// The typing scenario needs a composer the app will actually let you
	// type into: a 'stopped' session's composer is disabled (ChatView.tsx's
	// inputLocked), which is why the real run's typing scenario timed out
	// waiting for 'key-committed'. The long-transcript workspace must be a
	// live, non-terminal status.
	var longStatus string
	if err := pool.QueryRow(ctx, "SELECT status FROM sessions WHERE id = $1", summary.LongTranscriptSessionID).Scan(&longStatus); err != nil {
		t.Fatal(err)
	}
	if longStatus == "stopped" || longStatus == "ended" || longStatus == "error" {
		t.Fatalf("expected the long-transcript workspace to be live (composer enabled), got status %q", longStatus)
	}
	if longCount != 8000 {
		t.Fatalf("expected 8000 rows in the long transcript session, got %d", longCount)
	}

	var insightsEventCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_events ae
		JOIN sessions s ON s.id = ae.session_id
		WHERE s.parent_session_id = $1`, summary.InsightsOwnerSessionID).Scan(&insightsEventCount); err != nil {
		t.Fatal(err)
	}
	if insightsEventCount != 168000 {
		t.Fatalf("expected 168000 rows under the insights owner session, got %d", insightsEventCount)
	}

	// Re-seeding (as a fresh run against the same disposable database would
	// do) must not duplicate data; it clears and reseeds instead.
	summary2, err := SeedRunnerDataset(ctx, pool, "release-perf-account")
	if err != nil {
		t.Fatal(err)
	}
	if summary2.InsightsEvents != 168000 {
		t.Fatalf("expected re-seeding to produce the same counts, got %d", summary2.InsightsEvents)
	}
	var total int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE spawning_account_id = $1", "release-perf-account").Scan(&total); err != nil {
		t.Fatal(err)
	}
	// 25 workspaces + 420 insights children must not have doubled to 2x on reseed.
	if total > 25+420+1 {
		t.Fatalf("re-seeding appears to have duplicated sessions: %d total", total)
	}
}

func TestSeedIsAnchoredToFixedEpochNotNow(t *testing.T) {
	pool := migratedRunnerPool(t)
	ctx := context.Background()
	if _, err := SeedRunnerDataset(ctx, pool, "release-perf-account"); err != nil {
		t.Fatal(err)
	}
	var started1 string
	if err := pool.QueryRow(ctx, "SELECT started_at::text FROM sessions WHERE spawning_account_id = $1 ORDER BY started_at LIMIT 1", "release-perf-account").Scan(&started1); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedRunnerDataset(ctx, pool, "release-perf-account"); err != nil {
		t.Fatal(err)
	}
	var started2 string
	if err := pool.QueryRow(ctx, "SELECT started_at::text FROM sessions WHERE spawning_account_id = $1 ORDER BY started_at LIMIT 1", "release-perf-account").Scan(&started2); err != nil {
		t.Fatal(err)
	}
	if started1 != started2 {
		t.Fatalf("expected identical seeded timestamps across runs (fixed epoch, not now()): %s vs %s", started1, started2)
	}
}
