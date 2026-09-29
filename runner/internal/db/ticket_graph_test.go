package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// connectWithSchema creates a pool that bakes the given schema into every
// connection via RuntimeParams, ensuring concurrent goroutines all operate in
// the same schema even when the pool creates fresh connections.
func connectWithSchema(t *testing.T, schema string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("connectWithSchema parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connectWithSchema new pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// TestTicketGraph is the DB-gated test suite for ticket_graph.go.
// Run with:
//
//	export TEST_DATABASE_URL="postgres://postgres:test@localhost:5544/blerg-runner_test"
//	$HOME/go/bin/go test ./internal/db/ -run TestTicketGraph -v
func TestTicketGraph(t *testing.T) {
	// Phase 1: use the shared base pool to drop/create the test schema and run
	// migrations. setupSchema sets search_path for the base pool, which is fine
	// for sequential schema-management operations.
	basePool := connect(t)
	ctx := context.Background()
	setupSchema(t, basePool)
	if err := db.RunMigrations(ctx, basePool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// Phase 2: use a schema-aware pool for all actual test operations. Every
	// connection this pool creates (including those spawned for concurrent
	// goroutines) has search_path=test_blerg_runner baked in via RuntimeParams.
	pool := connectWithSchema(t, "test_blerg_runner")

	board, err := db.CreateBoard(ctx, pool, "Test Board", nil, []string{"repo-a", "repo-b"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(cols))
	}
	colID := cols[0].ID // Backlog

	makeTicket := func(title string) db.TicketRow {
		t.Helper()
		ticket, err := db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID:  board.ID,
			Title:    title,
			Repos:    []string{"repo-a"},
			ColumnID: &colID,
			Priority: "medium",
		})
		if err != nil {
			t.Fatalf("CreateTicket %q: %v", title, err)
		}
		return ticket
	}

	// ── 1. Self-edge ──────────────────────────────────────────────────────────
	t.Run("SelfDependency", func(t *testing.T) {
		A := makeTicket("Self-A")
		err := db.AddDependency(ctx, pool, A.ID, A.ID, "human")
		if !errors.Is(err, db.ErrSelfDependency) {
			t.Errorf("AddDependency self: got %v, want ErrSelfDependency", err)
		}
	})

	// ── 2. Cycle detection: A→B, B→C, then C→A must fail ────────────────────
	t.Run("CycleDetection", func(t *testing.T) {
		A := makeTicket("Cycle-A")
		B := makeTicket("Cycle-B")
		C := makeTicket("Cycle-C")

		if err := db.AddDependency(ctx, pool, A.ID, B.ID, "human"); err != nil {
			t.Fatalf("AddDependency A→B: %v", err)
		}
		if err := db.AddDependency(ctx, pool, B.ID, C.ID, "human"); err != nil {
			t.Fatalf("AddDependency B→C: %v", err)
		}
		err := db.AddDependency(ctx, pool, C.ID, A.ID, "human")
		if !errors.Is(err, db.ErrDependencyCycle) {
			t.Errorf("AddDependency C→A: got %v, want ErrDependencyCycle", err)
		}
	})

	// ── 3. Concurrent AddDependency — exactly one goroutine wins ─────────────
	//
	// Two goroutines race to add A→B and B→A. The board-row FOR UPDATE lock
	// serialises them: whichever acquires the lock second will observe the
	// committed edge and return ErrDependencyCycle. Exactly one must return nil.
	t.Run("ConcurrentCycle", func(t *testing.T) {
		A := makeTicket("Conc-A")
		B := makeTicket("Conc-B")

		errs := make([]error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs[0] = db.AddDependency(ctx, pool, A.ID, B.ID, "human")
		}()
		go func() {
			defer wg.Done()
			errs[1] = db.AddDependency(ctx, pool, B.ID, A.ID, "human")
		}()
		wg.Wait()

		successes := 0
		for _, e := range errs {
			if e == nil {
				successes++
			} else if !errors.Is(e, db.ErrDependencyCycle) {
				t.Errorf("unexpected error from concurrent AddDependency: %v", e)
			}
		}
		if successes != 1 {
			t.Errorf("expected exactly 1 success, got %d (err[0]=%v err[1]=%v)",
				successes, errs[0], errs[1])
		}
	})

	// ── 4. Cross-board dependency is rejected ─────────────────────────────────
	t.Run("CrossBoard", func(t *testing.T) {
		board2, err := db.CreateBoard(ctx, pool, "Board2", nil, []string{"repo-a"}, nil)
		if err != nil {
			t.Fatalf("CreateBoard2: %v", err)
		}
		cols2, err := db.ListColumns(ctx, pool, board2.ID)
		if err != nil {
			t.Fatalf("ListColumns board2: %v", err)
		}
		col2ID := cols2[0].ID

		A := makeTicket("CrossBoard-A")
		B, err := db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID:  board2.ID,
			Title:    "CrossBoard-B",
			Repos:    []string{"repo-a"},
			ColumnID: &col2ID,
			Priority: "medium",
		})
		if err != nil {
			t.Fatalf("CreateTicket board2: %v", err)
		}

		err = db.AddDependency(ctx, pool, A.ID, B.ID, "human")
		if !errors.Is(err, db.ErrCrossBoardDependency) {
			t.Errorf("cross-board AddDependency: got %v, want ErrCrossBoardDependency", err)
		}
	})

	// ── 5. SplitTicket ────────────────────────────────────────────────────────
	//
	// Setup:
	//   dep1 → origin → dep2   (dep1 depends on origin; origin depends on dep2)
	// After split into [child1, child2]:
	//   dep1 → child1, dep1 → child2   (inbound repointed to all children)
	//   child1 → dep2, child2 → dep2   (outbound copied to each child)
	//   origin archived, no dependency edges remain on origin
	//   split event written on origin listing child IDs
	t.Run("Split", func(t *testing.T) {
		origin, err := db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID:  board.ID,
			Title:    "Origin",
			Repos:    []string{"repo-a", "repo-b"},
			ColumnID: &colID,
			Priority: "high",
			Tags:     []string{"tag1"},
		})
		if err != nil {
			t.Fatalf("CreateTicket origin: %v", err)
		}

		dep1 := makeTicket("Split-Dep1") // will depend on origin
		dep2 := makeTicket("Split-Dep2") // origin will depend on this

		if err := db.AddDependency(ctx, pool, dep1.ID, origin.ID, "human"); err != nil {
			t.Fatalf("AddDependency dep1→origin: %v", err)
		}
		if err := db.AddDependency(ctx, pool, origin.ID, dep2.ID, "human"); err != nil {
			t.Fatalf("AddDependency origin→dep2: %v", err)
		}

		children, err := db.SplitTicket(ctx, pool, origin.ID, []string{"Child-1", "Child-2"})
		if err != nil {
			t.Fatalf("SplitTicket: %v", err)
		}
		if len(children) != 2 {
			t.Fatalf("expected 2 children, got %d", len(children))
		}

		// Children inherit priority, have nil size, and live in origin's column.
		for i, c := range children {
			if c.Priority != "high" {
				t.Errorf("children[%d].Priority = %q, want high", i, c.Priority)
			}
			if c.Size != nil {
				t.Errorf("children[%d].Size should be nil, got %v", i, *c.Size)
			}
			if c.ColumnID == nil || *c.ColumnID != colID {
				t.Errorf("children[%d].ColumnID = %v, want %s", i, c.ColumnID, colID)
			}
		}

		// Origin is archived.
		originRow, err := db.GetTicket(ctx, pool, origin.ID)
		if err != nil {
			t.Fatalf("GetTicket origin after split: %v", err)
		}
		if originRow.ArchivedAt == nil {
			t.Error("origin.ArchivedAt should be set after split")
		}
		if originRow.ColumnID != nil {
			t.Errorf("origin.ColumnID should be nil after split, got %v", *originRow.ColumnID)
		}

		// dep1 now depends on all children, not origin.
		dep1Detail, err := db.GetTicket(ctx, pool, dep1.ID)
		if err != nil {
			t.Fatalf("GetTicket dep1: %v", err)
		}
		if len(dep1Detail.DependsOn) != 2 {
			t.Errorf("dep1.DependsOn = %v (len=%d), want 2 entries (the two children)",
				dep1Detail.DependsOn, len(dep1Detail.DependsOn))
		}
		childIDSet := map[string]bool{children[0].ID: true, children[1].ID: true}
		for _, d := range dep1Detail.DependsOn {
			if !childIDSet[d] {
				t.Errorf("dep1 depends on %s which is not a child of origin", d)
			}
		}

		// Each child depends on dep2 (outbound copy) and inherits repos/tags.
		for i, c := range children {
			cd, err := db.GetTicket(ctx, pool, c.ID)
			if err != nil {
				t.Fatalf("GetTicket child[%d]: %v", i, err)
			}
			// Must depend on dep2.
			foundDep2 := false
			for _, d := range cd.DependsOn {
				if d == dep2.ID {
					foundDep2 = true
					break
				}
			}
			if !foundDep2 {
				t.Errorf("child[%d] (%s) does not depend on dep2 (outbound copy missing)", i, c.ID)
			}
			// Repos: inherited from origin (repo-a, repo-b).
			if len(cd.Repos) != 2 {
				t.Errorf("child[%d].Repos = %v, want [repo-a repo-b]", i, cd.Repos)
			}
			// Tags: inherited from origin (tag1).
			if len(cd.Tags) != 1 || cd.Tags[0] != "tag1" {
				t.Errorf("child[%d].Tags = %v, want [tag1]", i, cd.Tags)
			}
		}

		// Split event on origin lists child IDs.
		events, _, err := db.ListTicketEvents(ctx, pool, origin.ID, 10, "")
		if err != nil {
			t.Fatalf("ListTicketEvents origin: %v", err)
		}
		var splitEvent *db.TicketEvent
		for i := range events {
			if events[i].Type == "split" {
				splitEvent = &events[i]
				break
			}
		}
		if splitEvent == nil {
			t.Fatal("no split event found for origin")
		}
		if splitEvent.Actor != "system" {
			t.Errorf("split event actor = %q, want system", splitEvent.Actor)
		}
		var eventData map[string][]string
		if err := json.Unmarshal(splitEvent.Data, &eventData); err != nil {
			t.Fatalf("split event data unmarshal: %v", err)
		}
		if len(eventData["children"]) != 2 {
			t.Errorf("split event data.children = %v, want 2 IDs", eventData["children"])
		}
		for _, cid := range eventData["children"] {
			if !childIDSet[cid] {
				t.Errorf("split event child id %s not in children set", cid)
			}
		}

		// Origin must have zero dependency edges (inbound or outbound) after split.
		var originEdges int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM ticket_dependencies
			 WHERE ticket_id = $1 OR depends_on_ticket_id = $1
		`, origin.ID).Scan(&originEdges); err != nil {
			t.Fatalf("count origin edges: %v", err)
		}
		if originEdges != 0 {
			t.Errorf("origin still has %d dependency edge(s) after split, want 0", originEdges)
		}
	})

	// ── 6. ErrTooFewChildren ─────────────────────────────────────────────────
	t.Run("TooFewChildren", func(t *testing.T) {
		A := makeTicket("SplitFail-A")
		_, err := db.SplitTicket(ctx, pool, A.ID, []string{"only-one"})
		if !errors.Is(err, db.ErrTooFewChildren) {
			t.Errorf("SplitTicket 1 title: got %v, want ErrTooFewChildren", err)
		}
	})

	// ── 7. Events pagination ─────────────────────────────────────────────────
	t.Run("EventsPagination", func(t *testing.T) {
		A := makeTicket("Events-A")

		for i := 0; i < 5; i++ {
			if err := db.AppendTicketEvent(ctx, pool, db.TicketEvent{
				TicketID: A.ID,
				Type:     fmt.Sprintf("type-%d", i),
				Actor:    "test",
			}); err != nil {
				t.Fatalf("AppendTicketEvent %d: %v", i, err)
			}
		}

		// Page 1: 3 events (newest first).
		page1, cursor, err := db.ListTicketEvents(ctx, pool, A.ID, 3, "")
		if err != nil {
			t.Fatalf("ListTicketEvents page1: %v", err)
		}
		if len(page1) != 3 {
			t.Errorf("page1 len = %d, want 3", len(page1))
		}
		if cursor == "" {
			t.Error("expected non-empty cursor after page1")
		}
		// Events must be descending by ID.
		for i := 1; i < len(page1); i++ {
			if page1[i].ID >= page1[i-1].ID {
				t.Errorf("page1 not in descending order at index %d: %d >= %d",
					i, page1[i].ID, page1[i-1].ID)
			}
		}

		// Page 2: remaining 2 events.
		page2, cursor2, err := db.ListTicketEvents(ctx, pool, A.ID, 3, cursor)
		if err != nil {
			t.Fatalf("ListTicketEvents page2: %v", err)
		}
		if len(page2) != 2 {
			t.Errorf("page2 len = %d, want 2", len(page2))
		}
		if cursor2 != "" {
			t.Errorf("expected empty cursor after last page, got %q", cursor2)
		}

		// No overlap: all page2 IDs must be smaller than all page1 IDs.
		if len(page1) > 0 && len(page2) > 0 {
			minPage1 := page1[len(page1)-1].ID
			maxPage2 := page2[0].ID
			if maxPage2 >= minPage1 {
				t.Errorf("pages overlap or out of order: page2[0].ID=%d >= page1[-1].ID=%d",
					maxPage2, minPage1)
			}
		}
	})

	// ── 8. RemoveDependency ──────────────────────────────────────────────────
	t.Run("RemoveDependency", func(t *testing.T) {
		A := makeTicket("Remove-A")
		B := makeTicket("Remove-B")

		if err := db.AddDependency(ctx, pool, A.ID, B.ID, "human"); err != nil {
			t.Fatalf("AddDependency A→B: %v", err)
		}

		if err := db.RemoveDependency(ctx, pool, A.ID, B.ID, "human"); err != nil {
			t.Fatalf("RemoveDependency A→B: %v", err)
		}

		// Verify edge is gone: adding B→A should now succeed (no cycle).
		if err := db.AddDependency(ctx, pool, B.ID, A.ID, "human"); err != nil {
			t.Errorf("AddDependency B→A after remove: expected nil, got %v", err)
		}

		// dependency_removed event was written on A.
		events, _, err := db.ListTicketEvents(ctx, pool, A.ID, 20, "")
		if err != nil {
			t.Fatalf("ListTicketEvents A: %v", err)
		}
		found := false
		for _, e := range events {
			if e.Type == "dependency_removed" {
				found = true
				break
			}
		}
		if !found {
			t.Error("no dependency_removed event for A")
		}
	})

	// ── 8b. Actor attribution ─────────────────────────────────────────────────
	t.Run("ActorAttribution", func(t *testing.T) {
		A := makeTicket("Actor-A")
		B := makeTicket("Actor-B")

		// Adding with actor="assist" should write event with that actor.
		if err := db.AddDependency(ctx, pool, A.ID, B.ID, "assist"); err != nil {
			t.Fatalf("AddDependency with actor=assist: %v", err)
		}
		events, _, err := db.ListTicketEvents(ctx, pool, A.ID, 10, "")
		if err != nil {
			t.Fatalf("ListTicketEvents: %v", err)
		}
		var addedEvent *db.TicketEvent
		for i := range events {
			if events[i].Type == "dependency_added" {
				addedEvent = &events[i]
				break
			}
		}
		if addedEvent == nil {
			t.Fatal("no dependency_added event found")
		}
		if addedEvent.Actor != "assist" {
			t.Errorf("dependency_added actor = %q, want assist", addedEvent.Actor)
		}

		// Removing with actor="assist" should write event with that actor.
		if err := db.RemoveDependency(ctx, pool, A.ID, B.ID, "assist"); err != nil {
			t.Fatalf("RemoveDependency with actor=assist: %v", err)
		}
		events2, _, err := db.ListTicketEvents(ctx, pool, A.ID, 10, "")
		if err != nil {
			t.Fatalf("ListTicketEvents after remove: %v", err)
		}
		var removedEvent *db.TicketEvent
		for i := range events2 {
			if events2[i].Type == "dependency_removed" {
				removedEvent = &events2[i]
				break
			}
		}
		if removedEvent == nil {
			t.Fatal("no dependency_removed event found")
		}
		if removedEvent.Actor != "assist" {
			t.Errorf("dependency_removed actor = %q, want assist", removedEvent.Actor)
		}
	})

	// ── 9. RemoveDependency no-op writes no event ────────────────────────────
	t.Run("RemoveDependencyNoOp", func(t *testing.T) {
		A := makeTicket("RemoveNoOp-A")
		B := makeTicket("RemoveNoOp-B")

		// Count events before: removing a non-existent edge must not add any.
		before, _, err := db.ListTicketEvents(ctx, pool, A.ID, 200, "")
		if err != nil {
			t.Fatalf("ListTicketEvents before: %v", err)
		}

		// No A→B edge exists. Remove is an idempotent no-op → returns nil.
		if err := db.RemoveDependency(ctx, pool, A.ID, B.ID, "human"); err != nil {
			t.Fatalf("RemoveDependency no-op: expected nil, got %v", err)
		}

		after, _, err := db.ListTicketEvents(ctx, pool, A.ID, 200, "")
		if err != nil {
			t.Fatalf("ListTicketEvents after: %v", err)
		}
		if len(after) != len(before) {
			t.Errorf("no-op RemoveDependency changed event count: before=%d after=%d (phantom event written)",
				len(before), len(after))
		}
	})
}
