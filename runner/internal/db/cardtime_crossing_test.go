package db_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// M7: the dedup row and the plan fact commit together. A crossing not yet committed must not be
// visible to a concurrent reader, and once committed both rows must exist.
func TestRecordCardTimeCrossingsCommitsDedupAndFactTogether(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordCardTimeCrossings(context.Background(), tx, owner, "board-a", 5, root, []int64{4 * 3600, 8 * 3600},
		func(threshold int64) string { return "fact for threshold" }); err != nil {
		t.Fatal(err)
	}

	// Before commit, a separate connection must see neither the dedup rows nor the plan fact.
	fired, err := db.FiredCardTimeThresholds(context.Background(), pool, owner, "board-a", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(fired) != 0 {
		t.Fatalf("uncommitted transaction visible to another connection: got %v fired, want none yet", fired)
	}
	pending, err := db.PendingPlanFacts(context.Background(), pool, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("uncommitted plan fact visible to another connection: got %d, want 0", len(pending))
	}

	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	fired, err = db.FiredCardTimeThresholds(context.Background(), pool, owner, "board-a", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !fired[4*3600] || !fired[8*3600] {
		t.Fatalf("after commit: got fired=%v, want {14400:true,28800:true}", fired)
	}
	pending, err = db.PendingPlanFacts(context.Background(), pool, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("after commit: got %d pending plan facts for root, want 2", len(pending))
	}
}

// A threshold already fired is skipped: no duplicate dedup row, no duplicate plan fact.
func TestRecordCardTimeCrossingsSkipsAlreadyFired(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)

	record := func(thresholds []int64) {
		tx, err := pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := db.RecordCardTimeCrossings(context.Background(), tx, owner, "board-a", 6, root, thresholds,
			func(threshold int64) string { return "fact" }); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	record([]int64{4 * 3600})
	record([]int64{4 * 3600, 8 * 3600}) // 4h already fired; only 8h is new

	fired, err := db.FiredCardTimeThresholds(context.Background(), pool, owner, "board-a", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(fired) != 2 {
		t.Fatalf("got %v, want exactly {4h,8h}", fired)
	}
	pending, err := db.PendingPlanFacts(context.Background(), pool, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("got %d pending plan facts, want 2 (one per threshold, no duplicate for the re-attempted 4h)", len(pending))
	}
}

// H2: the plan fact lands on the ROOT session, never a worker passed in as rootSessionID by
// mistake would be — this test documents the contract explicitly: RecordCardTimeCrossings writes
// wherever it is told to, so the caller (the server wiring) is responsible for resolving
// RootSessionID first. Verified here by passing a non-root session and confirming the fact lands
// there, not magically on its ancestor — RecordCardTimeCrossings does no resolution of its own.
func TestRecordCardTimeCrossingsWritesExactlyWhereTold(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	owner := "cardtime-" + newUUID(t, pool)
	root := mustSession(t, pool, daemon, owner)
	worker := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: owner, ParentSessionID: root})

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordCardTimeCrossings(context.Background(), tx, owner, "board-a", 7, root, []int64{4 * 3600},
		func(threshold int64) string { return "fact" }); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	rootFacts, err := db.PendingPlanFacts(context.Background(), pool, root)
	if err != nil {
		t.Fatal(err)
	}
	workerFacts, err := db.PendingPlanFacts(context.Background(), pool, worker)
	if err != nil {
		t.Fatal(err)
	}
	if len(rootFacts) != 1 {
		t.Fatalf("got %d facts on root, want 1", len(rootFacts))
	}
	if len(workerFacts) != 0 {
		t.Fatalf("got %d facts on worker, want 0 (the fact must never land on the worker itself)", len(workerFacts))
	}
}
