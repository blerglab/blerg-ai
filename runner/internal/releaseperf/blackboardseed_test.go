package releaseperf

import (
	"context"
	"testing"

	rdb "github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestSeedBlackboardDataset(t *testing.T) {
	pool := migratedRunnerPool(t)
	ctx := context.Background()
	const workspaceID = "release-perf-workspace"
	if err := SeedBlackboardDataset(ctx, pool, "release-perf-account", workspaceID); err != nil {
		t.Fatal(err)
	}

	// HandleListBlackboards (runner/internal/server/blackboard_plan.go) finds
	// a workspace's blackboard by record->>'workspace_id', not by owner —
	// the blackboard-rendered scenario opens through that same workspace-
	// scoped lookup, so the seeded record must carry the real workspace id,
	// not the owning account's id.
	var linked bool
	if err := pool.QueryRow(ctx, "SELECT record->>'workspace_id' = $1 FROM blackboards WHERE id = 'release-perf-blackboard'", workspaceID).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if !linked {
		t.Fatal("expected the seeded blackboard's record.workspace_id to be the seeded workspace id")
	}

	// The real server re-derives the record's id from the unmarshaled blob
	// itself (loadBlackboardCore) and re-queries by that id — a blob
	// missing "id" silently loads as "" there, so the real load path is
	// the only thing that actually proves the seeded blob is well-formed, a
	// column-level check of record->>'workspace_id' alone would not have
	// caught this.
	store := rdb.BlackboardStore{Pool: pool}
	loaded, _, _, err := store.LoadReadPage(ctx, "release-perf-blackboard", 0, 50, true)
	if err != nil {
		t.Fatalf("expected the seeded blackboard to load through the real read path, got: %v", err)
	}
	if loaded.ID != "release-perf-blackboard" {
		t.Fatalf("expected the loaded record's own id to be 'release-perf-blackboard', got %q", loaded.ID)
	}
	if len(loaded.Goals) == 0 {
		t.Fatal("expected the loaded record to carry the seeded goal")
	}
	var goals, items, events int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM goals WHERE blackboard_id = 'release-perf-blackboard'").Scan(&goals); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM plan_items WHERE blackboard_id = 'release-perf-blackboard'").Scan(&items); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM plan_events WHERE blackboard_id = 'release-perf-blackboard'").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if goals == 0 || items == 0 || events == 0 {
		t.Fatalf("expected a non-empty blackboard, got goals=%d items=%d events=%d", goals, items, events)
	}

	// plan_events is append-only (immutable-history trigger); re-seeding
	// must be a no-op rather than attempt a delete that the trigger refuses.
	if err := SeedBlackboardDataset(ctx, pool, "release-perf-account", workspaceID); err != nil {
		t.Fatal(err)
	}
	var goals2 int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM goals WHERE blackboard_id = 'release-perf-blackboard'").Scan(&goals2); err != nil {
		t.Fatal(err)
	}
	if goals2 != goals {
		t.Fatalf("expected re-seeding to be a no-op: %d vs %d goals", goals2, goals)
	}
}
