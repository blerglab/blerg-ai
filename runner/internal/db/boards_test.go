package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// TestBoardCRUD covers CreateBoard (seeds repos + 3 default columns, Done terminal),
// GetBoard, ListBoards, and DeleteBoard cascade.
func TestBoardCRUD(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	desc := "my board"
	board, err := db.CreateBoard(ctx, pool, "Sprint 1", &desc, []string{"repo-a", "repo-b"}, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if board.ID == "" {
		t.Fatal("CreateBoard: got empty ID")
	}
	if board.Name != "Sprint 1" {
		t.Errorf("CreateBoard name = %q, want %q", board.Name, "Sprint 1")
	}
	if board.Description == nil || *board.Description != "my board" {
		t.Errorf("CreateBoard description = %v, want %q", board.Description, "my board")
	}

	// ── Verify board_repos were seeded ───────────────────────────────────────
	var repoCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM board_repos WHERE board_id = $1`, board.ID,
	).Scan(&repoCount); err != nil {
		t.Fatalf("count board_repos: %v", err)
	}
	if repoCount != 2 {
		t.Errorf("board_repos count = %d, want 2", repoCount)
	}

	// ── Verify 3 default columns were seeded in order ─────────────────────────
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("ListColumns: got %d columns, want 3", len(cols))
	}
	wantNames := []string{"Backlog", "Doing", "Done"}
	for i, col := range cols {
		if col.Name != wantNames[i] {
			t.Errorf("column[%d].Name = %q, want %q", i, col.Name, wantNames[i])
		}
	}
	// Ranks must be strictly ascending.
	if cols[0].Rank >= cols[1].Rank || cols[1].Rank >= cols[2].Rank {
		t.Errorf("columns not in ascending rank order: %q %q %q",
			cols[0].Rank, cols[1].Rank, cols[2].Rank)
	}
	// Backlog and Doing must not be terminal; Done must be terminal.
	if cols[0].IsTerminal {
		t.Error("Backlog.IsTerminal = true, want false")
	}
	if cols[1].IsTerminal {
		t.Error("Doing.IsTerminal = true, want false")
	}
	if !cols[2].IsTerminal {
		t.Error("Done.IsTerminal = false, want true")
	}

	// ── GetBoard ──────────────────────────────────────────────────────────────
	got, err := db.GetBoard(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	if got.ID != board.ID || got.Name != board.Name {
		t.Errorf("GetBoard: got %+v, want id=%s name=%s", got, board.ID, board.Name)
	}

	// ── ListBoards ────────────────────────────────────────────────────────────
	boards, err := db.ListBoards(ctx, pool)
	if err != nil {
		t.Fatalf("ListBoards: %v", err)
	}
	if len(boards) != 1 || boards[0].ID != board.ID {
		t.Errorf("ListBoards: got %d boards, want 1 with id=%s", len(boards), board.ID)
	}

	// ── DeleteBoard cascades ──────────────────────────────────────────────────
	if err := db.DeleteBoard(ctx, pool, board.ID); err != nil {
		t.Fatalf("DeleteBoard: %v", err)
	}

	// Board should be gone.
	boards2, err := db.ListBoards(ctx, pool)
	if err != nil {
		t.Fatalf("ListBoards after delete: %v", err)
	}
	if len(boards2) != 0 {
		t.Errorf("ListBoards after delete: got %d boards, want 0", len(boards2))
	}

	// Columns should be gone (CASCADE).
	cols2, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns after delete: %v", err)
	}
	if len(cols2) != 0 {
		t.Errorf("ListColumns after delete: got %d columns, want 0", len(cols2))
	}

	// board_repos should be gone (CASCADE).
	var repoCount2 int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM board_repos WHERE board_id = $1`, board.ID,
	).Scan(&repoCount2); err != nil {
		t.Fatalf("count board_repos after delete: %v", err)
	}
	if repoCount2 != 0 {
		t.Errorf("board_repos count after delete = %d, want 0", repoCount2)
	}
}

// TestColumnOperations covers AddColumn, RenameColumn, SetColumnTerminal, MoveColumn,
// and ListColumns ordering.
func TestColumnOperations(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	board, err := db.CreateBoard(ctx, pool, "Ops Board", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	// Default columns: Backlog, Doing, Done.
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns (initial): %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("expected 3 initial columns, got %d", len(cols))
	}
	backlogID := cols[0].ID
	doingID := cols[1].ID
	doneID := cols[2].ID

	// ── AddColumn after Doing (before Done) ──────────────────────────────────
	review, err := db.AddColumn(ctx, pool, board.ID, "Review", &doingID, false)
	if err != nil {
		t.Fatalf("AddColumn Review: %v", err)
	}
	if review.Name != "Review" {
		t.Errorf("AddColumn.Name = %q, want Review", review.Name)
	}
	if review.IsTerminal {
		t.Error("AddColumn.IsTerminal = true, want false")
	}

	// ListColumns should now return 4 columns in rank order.
	cols2, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns (after add): %v", err)
	}
	if len(cols2) != 4 {
		t.Fatalf("expected 4 columns after add, got %d", len(cols2))
	}
	wantOrder := []string{"Backlog", "Doing", "Review", "Done"}
	for i, col := range cols2 {
		if col.Name != wantOrder[i] {
			t.Errorf("column[%d].Name = %q, want %q", i, col.Name, wantOrder[i])
		}
	}

	// ── AddColumn appended (no afterColumnID) ─────────────────────────────────
	archived, err := db.AddColumn(ctx, pool, board.ID, "Archived", nil, true)
	if err != nil {
		t.Fatalf("AddColumn Archived: %v", err)
	}
	if archived.Rank <= cols2[3].Rank {
		t.Errorf("Archived rank %q should be > Done rank %q", archived.Rank, cols2[3].Rank)
	}

	// ── RenameColumn ─────────────────────────────────────────────────────────
	renamed, err := db.RenameColumn(ctx, pool, review.ID, "In Review")
	if err != nil {
		t.Fatalf("RenameColumn: %v", err)
	}
	if renamed.Name != "In Review" {
		t.Errorf("RenameColumn.Name = %q, want %q", renamed.Name, "In Review")
	}
	if renamed.ID != review.ID {
		t.Errorf("RenameColumn.ID = %q, want %q", renamed.ID, review.ID)
	}

	// ── SetColumnTerminal ─────────────────────────────────────────────────────
	toggled, err := db.SetColumnTerminal(ctx, pool, backlogID, true)
	if err != nil {
		t.Fatalf("SetColumnTerminal(true): %v", err)
	}
	if !toggled.IsTerminal {
		t.Error("SetColumnTerminal(true): IsTerminal = false, want true")
	}
	toggled2, err := db.SetColumnTerminal(ctx, pool, backlogID, false)
	if err != nil {
		t.Fatalf("SetColumnTerminal(false): %v", err)
	}
	if toggled2.IsTerminal {
		t.Error("SetColumnTerminal(false): IsTerminal = true, want false")
	}

	// ── MoveColumn: move Backlog to after Done ────────────────────────────────
	// Before: Backlog, Doing, InReview, Done, Archived
	// Move Backlog after Done (before Archived).
	moved, err := db.MoveColumn(ctx, pool, backlogID, &doneID, &archived.ID)
	if err != nil {
		t.Fatalf("MoveColumn: %v", err)
	}

	// Get the ranks from the neighboring columns to verify ordering.
	var doneRank, archivedRank string
	if err := pool.QueryRow(ctx, `SELECT rank FROM columns WHERE id = $1`, doneID).Scan(&doneRank); err != nil {
		t.Fatalf("get doneRank: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT rank FROM columns WHERE id = $1`, archived.ID).Scan(&archivedRank); err != nil {
		t.Fatalf("get archivedRank: %v", err)
	}
	if moved.Rank <= doneRank || moved.Rank >= archivedRank {
		t.Errorf("MoveColumn: rank %q should be between doneRank %q and archivedRank %q",
			moved.Rank, doneRank, archivedRank)
	}

	// ── MoveColumn: move to front (no after, before = first in list) ─────────
	// First re-list to get current first column.
	colsNow, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns (before move to front): %v", err)
	}
	first := colsNow[0]
	// Move the last column to before the first (to the front).
	last := colsNow[len(colsNow)-1]
	movedFront, err := db.MoveColumn(ctx, pool, last.ID, nil, &first.ID)
	if err != nil {
		t.Fatalf("MoveColumn to front: %v", err)
	}
	if movedFront.Rank >= first.Rank {
		t.Errorf("MoveColumn to front: rank %q should be < first rank %q",
			movedFront.Rank, first.Rank)
	}

	// ── MoveColumn: move to end (after last, no before) ──────────────────────
	colsNow2, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns (before move to end): %v", err)
	}
	secondToLast := colsNow2[len(colsNow2)-2]
	lastNow := colsNow2[len(colsNow2)-1]
	_ = lastNow
	// Move first to after second-to-last.
	movedEnd, err := db.MoveColumn(ctx, pool, colsNow2[0].ID, &secondToLast.ID, nil)
	if err != nil {
		t.Fatalf("MoveColumn to end: %v", err)
	}
	if movedEnd.Rank <= secondToLast.Rank {
		t.Errorf("MoveColumn to end: rank %q should be > secondToLast rank %q",
			movedEnd.Rank, secondToLast.Rank)
	}
}

// TestColumnDeleteLiveTickets covers the ErrColumnHasLiveTickets guard:
// - Blocked when a live (non-archived) ticket references the column.
// - Succeeds when only archived tickets exist (or none).
func TestColumnDeleteLiveTickets(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	board, err := db.CreateBoard(ctx, pool, "Ticket Board", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	backlogID := cols[0].ID

	// ── No tickets: delete should succeed ────────────────────────────────────
	// (Use a fresh column so we don't break the board for later checks.)
	extraCol, err := db.AddColumn(ctx, pool, board.ID, "Temp", nil, false)
	if err != nil {
		t.Fatalf("AddColumn Temp: %v", err)
	}
	if err := db.DeleteColumn(ctx, pool, extraCol.ID); err != nil {
		t.Fatalf("DeleteColumn (no tickets): %v", err)
	}

	// ── Insert a live ticket ──────────────────────────────────────────────────
	var ticketID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (board_id, column_id, title, rank)
		VALUES ($1, $2, 'Test Ticket', 'h')
		RETURNING id
	`, board.ID, backlogID).Scan(&ticketID); err != nil {
		t.Fatalf("insert live ticket: %v", err)
	}

	// Delete should fail with ErrColumnHasLiveTickets.
	err = db.DeleteColumn(ctx, pool, backlogID)
	if !errors.Is(err, db.ErrColumnHasLiveTickets) {
		t.Fatalf("DeleteColumn with live ticket: got %v, want ErrColumnHasLiveTickets", err)
	}

	// ── Archive the ticket (column_id = NULL, archived_at = now()) ────────────
	if _, err := pool.Exec(ctx, `
		UPDATE tickets SET column_id = NULL, archived_at = now() WHERE id = $1
	`, ticketID); err != nil {
		t.Fatalf("archive ticket: %v", err)
	}

	// Now delete should succeed (only archived ticket, not live).
	if err := db.DeleteColumn(ctx, pool, backlogID); err != nil {
		t.Fatalf("DeleteColumn (only archived ticket): %v", err)
	}

	// Column should be gone.
	var colCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM columns WHERE id = $1`, backlogID,
	).Scan(&colCount); err != nil {
		t.Fatalf("count columns after delete: %v", err)
	}
	if colCount != 0 {
		t.Errorf("column still exists after successful delete: count = %d", colCount)
	}
}
