package releaseperf

import (
	"context"
	"testing"

	rdb "github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestSeedBoardDatasetCounts(t *testing.T) {
	pool := migratedRunnerPool(t)
	ctx := context.Background()
	boardID, n, err := SeedBoardDataset(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if boardID == "" {
		t.Fatal("expected a real board id")
	}
	if n < 150 {
		t.Fatalf("expected at least 150 seeded cards, got %d", n)
	}

	// The board-tab scenario opens the board through HandleGetBoard's own
	// read path (db.GetBoard) — this is what actually would have caught
	// seeding into the wrong database (the separate board/web service's
	// own schema, which runner's "boards" table knows nothing about, so
	// every /api/boards/{id} call 404'd for 18 release-perf runs).
	if _, err := rdb.GetBoard(ctx, pool, boardID); err != nil {
		t.Fatalf("expected the seeded board to be readable through runner's own GetBoard, got: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tickets WHERE board_id = $1", boardID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("expected %d rows in tickets, got %d", n, count)
	}

	// Re-seeding must replace, not duplicate, and not fail on the name
	// collision with the board it just replaced.
	boardID2, n2, err := SeedBoardDataset(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	var count2 int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tickets WHERE board_id = $1", boardID2).Scan(&count2); err != nil {
		t.Fatal(err)
	}
	if count2 != n2 {
		t.Fatalf("expected re-seeding to replace rather than duplicate: got %d rows for %d cards", count2, n2)
	}
	var staleCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM boards WHERE id = $1", boardID).Scan(&staleCount); err != nil {
		t.Fatal(err)
	}
	if boardID != boardID2 && staleCount != 0 {
		t.Fatalf("expected the prior seeded board %q to be gone after re-seeding, found %d", boardID, staleCount)
	}
}
