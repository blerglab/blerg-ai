package db_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// setupTicketBoard creates a board with repos "repo-a" and "repo-b", drops and
// recreates the test schema, runs migrations, and returns the board and the
// three default columns (Backlog, Doing, Done) in rank order. All operations
// use the provided pool so that search_path remains consistent throughout the
// test.
func setupTicketBoard(t *testing.T, pool *pgxpool.Pool) (*db.BoardRow, []db.ColumnRow) {
	t.Helper()
	ctx := context.Background()
	setupSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
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
	return &board, cols
}

// TestTicketCreateDefaults verifies that CreateTicket assigns sensible
// defaults (priority=medium, first non-terminal column, end-of-column rank)
// and that GetTicket returns the repos and tags verbatim.
func TestTicketCreateDefaults(t *testing.T) {
	pool := connect(t)
	board, cols := setupTicketBoard(t, pool)
	ctx := context.Background()

	backlogID := cols[0].ID // first non-terminal column

	in := db.TicketInput{
		BoardID: board.ID,
		Title:   "My first ticket",
		Repos:   []string{"repo-a"},
		// Priority and ColumnID are omitted → defaults apply
		Tags: []string{"bug", "frontend"},
	}

	row, err := db.CreateTicket(ctx, pool, in)
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if row.ID == "" {
		t.Fatal("CreateTicket: got empty ID")
	}
	if row.Priority != "medium" {
		t.Errorf("Priority = %q, want %q", row.Priority, "medium")
	}
	if row.ColumnID == nil || *row.ColumnID != backlogID {
		t.Errorf("ColumnID = %v, want %q", row.ColumnID, backlogID)
	}
	if row.Rank == "" {
		t.Error("Rank is empty")
	}
	if row.Version != 0 {
		t.Errorf("Version = %d, want 0", row.Version)
	}

	// GetTicket should return repos and tags.
	detail, err := db.GetTicket(ctx, pool, row.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if len(detail.Repos) != 1 || detail.Repos[0] != "repo-a" {
		t.Errorf("Repos = %v, want [repo-a]", detail.Repos)
	}
	if len(detail.Tags) != 2 {
		t.Errorf("Tags = %v, want 2 entries", detail.Tags)
	}
	for _, want := range []string{"bug", "frontend"} {
		found := false
		for _, got := range detail.Tags {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Tags missing %q; got %v", want, detail.Tags)
		}
	}
	if len(detail.DependsOn) != 0 {
		t.Errorf("DependsOn = %v, want empty", detail.DependsOn)
	}
	if len(detail.Blocks) != 0 {
		t.Errorf("Blocks = %v, want empty", detail.Blocks)
	}

	// Second ticket should get a rank after the first.
	row2, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID: board.ID,
		Title:   "Second ticket",
		Repos:   []string{"repo-b"},
	})
	if err != nil {
		t.Fatalf("CreateTicket (second): %v", err)
	}
	if row2.Rank <= row.Rank {
		t.Errorf("second ticket rank %q should be > first rank %q", row2.Rank, row.Rank)
	}
}

// TestTicketCreateExplicitColumn verifies that an explicit ColumnID and
// Priority are respected.
func TestTicketCreateExplicitColumn(t *testing.T) {
	pool := connect(t)
	board, cols := setupTicketBoard(t, pool)
	ctx := context.Background()

	doingID := cols[1].ID
	sz := "M"
	body := "details"

	row, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Explicit column ticket",
		Repos:    []string{"repo-a", "repo-b"},
		ColumnID: &doingID,
		Priority: "high",
		Size:     &sz,
		Body:     &body,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if row.ColumnID == nil || *row.ColumnID != doingID {
		t.Errorf("ColumnID = %v, want %q", row.ColumnID, doingID)
	}
	if row.Priority != "high" {
		t.Errorf("Priority = %q, want high", row.Priority)
	}
	if row.Size == nil || *row.Size != "M" {
		t.Errorf("Size = %v, want M", row.Size)
	}
	if row.Body == nil || *row.Body != "details" {
		t.Errorf("Body = %v, want details", row.Body)
	}
}

// TestTicketCreateValidation ensures CreateTicket rejects invalid inputs.
func TestTicketCreateValidation(t *testing.T) {
	pool := connect(t)
	board, _ := setupTicketBoard(t, pool)
	ctx := context.Background()

	good := db.TicketInput{BoardID: board.ID, Title: "T", Repos: []string{"repo-a"}}

	cases := []struct {
		name string
		mod  func(*db.TicketInput)
	}{
		{"zero repos", func(in *db.TicketInput) { in.Repos = nil }},
		{"empty repos", func(in *db.TicketInput) { in.Repos = []string{} }},
		{"repo not in board", func(in *db.TicketInput) { in.Repos = []string{"repo-z"} }},
		{"bad priority", func(in *db.TicketInput) { in.Priority = "critical" }},
		{"bad size", func(in *db.TicketInput) {
			sz := "HUGE"
			in.Size = &sz
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := good // copy
			tc.mod(&in)
			_, err := db.CreateTicket(ctx, pool, in)
			if err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

// TestTicketMove verifies that MoveTicket places the ticket between the given
// neighbor tickets (via after / before ticket IDs) and updates the column_id.
func TestTicketMove(t *testing.T) {
	pool := connect(t)
	board, cols := setupTicketBoard(t, pool)
	ctx := context.Background()

	backlogID := cols[0].ID
	doingID := cols[1].ID

	// Create three tickets in Backlog.
	t1, err := db.CreateTicket(ctx, pool, db.TicketInput{BoardID: board.ID, Title: "T1", Repos: []string{"repo-a"}, ColumnID: &backlogID})
	if err != nil {
		t.Fatalf("create T1: %v", err)
	}
	t2, err := db.CreateTicket(ctx, pool, db.TicketInput{BoardID: board.ID, Title: "T2", Repos: []string{"repo-a"}, ColumnID: &backlogID})
	if err != nil {
		t.Fatalf("create T2: %v", err)
	}
	t3, err := db.CreateTicket(ctx, pool, db.TicketInput{BoardID: board.ID, Title: "T3", Repos: []string{"repo-a"}, ColumnID: &backlogID})
	if err != nil {
		t.Fatalf("create T3: %v", err)
	}

	// Move T2 to Doing, between T1 and T3 (even though they're in Backlog,
	// their ranks are used as bounds in the new column's ordering space).
	moved, err := db.MoveTicket(ctx, pool, t2.ID, doingID, &t1.ID, &t3.ID, nil)
	if err != nil {
		t.Fatalf("MoveTicket: %v", err)
	}
	if moved.ColumnID == nil || *moved.ColumnID != doingID {
		t.Errorf("ColumnID = %v, want %q", moved.ColumnID, doingID)
	}
	if moved.Rank <= t1.Rank || moved.Rank >= t3.Rank {
		t.Errorf("rank %q should be between %q and %q", moved.Rank, t1.Rank, t3.Rank)
	}

	// Move T1 to front of Doing (no after, before = T2 in its new rank).
	movedFront, err := db.MoveTicket(ctx, pool, t1.ID, doingID, nil, &t2.ID, nil)
	if err != nil {
		t.Fatalf("MoveTicket to front: %v", err)
	}
	// Re-fetch moved T2 to get its current rank.
	movedT2Det, err := db.GetTicket(ctx, pool, t2.ID)
	if err != nil {
		t.Fatalf("GetTicket T2: %v", err)
	}
	if movedFront.Rank >= movedT2Det.Rank {
		t.Errorf("front rank %q should be < T2 rank %q", movedFront.Rank, movedT2Det.Rank)
	}

	// Move T3 to end of Doing (after = T2 in Doing, no before).
	movedEnd, err := db.MoveTicket(ctx, pool, t3.ID, doingID, &t2.ID, nil, nil)
	if err != nil {
		t.Fatalf("MoveTicket to end: %v", err)
	}
	if movedEnd.Rank <= movedT2Det.Rank {
		t.Errorf("end rank %q should be > T2 rank %q", movedEnd.Rank, movedT2Det.Rank)
	}
}

// TestTicketMoveVersionGuard verifies the expectVersion contract on MoveTicket:
// nil → no guard, no version bump; correct version → bump (+1); stale version →
// ErrStaleVersion with no change.
func TestTicketMoveVersionGuard(t *testing.T) {
	pool := connect(t)
	board, cols := setupTicketBoard(t, pool)
	ctx := context.Background()

	backlogID := cols[0].ID
	doingID := cols[1].ID

	tk, err := db.CreateTicket(ctx, pool, db.TicketInput{BoardID: board.ID, Title: "Guard", Repos: []string{"repo-a"}, ColumnID: &backlogID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tk.Version != 0 {
		t.Fatalf("initial version = %d, want 0", tk.Version)
	}

	// nil expectVersion: no bump.
	unguarded, err := db.MoveTicket(ctx, pool, tk.ID, doingID, nil, nil, nil)
	if err != nil {
		t.Fatalf("unguarded move: %v", err)
	}
	if unguarded.Version != 0 {
		t.Errorf("version after unguarded move = %d, want 0 (no bump)", unguarded.Version)
	}

	// Correct expectVersion: bump to 1.
	v0 := 0
	guarded, err := db.MoveTicket(ctx, pool, tk.ID, backlogID, nil, nil, &v0)
	if err != nil {
		t.Fatalf("guarded move: %v", err)
	}
	if guarded.Version != 1 {
		t.Errorf("version after guarded move = %d, want 1", guarded.Version)
	}

	// Stale expectVersion: ErrStaleVersion, no change.
	stale := 0
	if _, err := db.MoveTicket(ctx, pool, tk.ID, doingID, nil, nil, &stale); !errors.Is(err, db.ErrStaleVersion) {
		t.Fatalf("stale move err = %v, want ErrStaleVersion", err)
	}
	after, err := db.GetTicket(ctx, pool, tk.ID)
	if err != nil {
		t.Fatalf("GetTicket after stale: %v", err)
	}
	if after.Version != 1 {
		t.Errorf("version after stale move = %d, want 1 (unchanged)", after.Version)
	}
	if after.ColumnID == nil || *after.ColumnID != backlogID {
		t.Errorf("column after stale move = %v, want %s (unchanged)", after.ColumnID, backlogID)
	}
}

// TestTicketArchive verifies that ArchiveTicket sets archived_at and nulls column_id.
func TestTicketArchive(t *testing.T) {
	pool := connect(t)
	board, _ := setupTicketBoard(t, pool)
	ctx := context.Background()

	row, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID: board.ID,
		Title:   "Archivable",
		Repos:   []string{"repo-a"},
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if row.ColumnID == nil {
		t.Fatal("ticket should have a column before archiving")
	}

	before := time.Now().Add(-time.Second)
	archived, err := db.ArchiveTicket(ctx, pool, row.ID)
	if err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}
	if archived.ColumnID != nil {
		t.Errorf("ColumnID after archive = %v, want nil", archived.ColumnID)
	}
	if archived.ArchivedAt == nil {
		t.Fatal("ArchivedAt is nil after archive")
	}
	if archived.ArchivedAt.Before(before) {
		t.Errorf("ArchivedAt %v is before test start %v", archived.ArchivedAt, before)
	}
}

// TestTicketUpdateVersionGuard verifies that:
//   - Updating with the correct expectVersion succeeds and bumps version.
//   - Updating with a stale expectVersion returns ErrStaleVersion.
//   - Additive tag/repo ops work independently of the version guard.
func TestTicketUpdateVersionGuard(t *testing.T) {
	pool := connect(t)
	board, _ := setupTicketBoard(t, pool)
	ctx := context.Background()

	row, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID: board.ID,
		Title:   "Version test",
		Repos:   []string{"repo-a"},
		Tags:    []string{"init"},
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if row.Version != 0 {
		t.Fatalf("initial version = %d, want 0", row.Version)
	}

	// ── Correct expectVersion: update should succeed ──────────────────────────
	newTitle := "Updated title"
	v0 := 0
	updated, err := db.UpdateTicket(ctx, pool, row.ID, db.TicketPatch{Title: &newTitle}, &v0)
	if err != nil {
		t.Fatalf("UpdateTicket (correct version): %v", err)
	}
	if updated.Title != "Updated title" {
		t.Errorf("Title = %q, want %q", updated.Title, "Updated title")
	}
	if updated.Version != 1 {
		t.Errorf("Version = %d, want 1 after update", updated.Version)
	}

	// ── Stale expectVersion: should return ErrStaleVersion ───────────────────
	anotherTitle := "Should not apply"
	_, err = db.UpdateTicket(ctx, pool, row.ID, db.TicketPatch{Title: &anotherTitle}, &v0) // v0=0, current is now 1
	if !errors.Is(err, db.ErrStaleVersion) {
		t.Fatalf("UpdateTicket (stale): got %v, want ErrStaleVersion", err)
	}

	// ── No expectVersion: scalar update always succeeds ───────────────────────
	v1 := 1
	pri := "high"
	updated2, err := db.UpdateTicket(ctx, pool, row.ID, db.TicketPatch{Priority: &pri}, &v1)
	if err != nil {
		t.Fatalf("UpdateTicket (no version guard): %v", err)
	}
	if updated2.Priority != "high" {
		t.Errorf("Priority = %q, want high", updated2.Priority)
	}
	if updated2.Version != 2 {
		t.Errorf("Version = %d, want 2", updated2.Version)
	}

	// ── Additive tag ops are idempotent and don't bump version ───────────────
	noVersionExpect := updated2.Version
	withTags, err := db.UpdateTicket(ctx, pool, row.ID, db.TicketPatch{
		AddTags: []string{"new-tag", "init"}, // "init" already exists → idempotent
		RmTags:  []string{"init"},
	}, nil)
	if err != nil {
		t.Fatalf("UpdateTicket (additive tags): %v", err)
	}
	if withTags.Version != noVersionExpect {
		t.Errorf("Version bumped on tag-only patch: got %d, want %d", withTags.Version, noVersionExpect)
	}

	detail, err := db.GetTicket(ctx, pool, row.ID)
	if err != nil {
		t.Fatalf("GetTicket after tag update: %v", err)
	}
	// Should have "new-tag" and NOT "init".
	tagSet := map[string]bool{}
	for _, tag := range detail.Tags {
		tagSet[tag] = true
	}
	if !tagSet["new-tag"] {
		t.Errorf("tag 'new-tag' not present; tags = %v", detail.Tags)
	}
	if tagSet["init"] {
		t.Errorf("tag 'init' still present after removal; tags = %v", detail.Tags)
	}

	// ── Additive repo ops ─────────────────────────────────────────────────────
	withRepos, err := db.UpdateTicket(ctx, pool, row.ID, db.TicketPatch{
		AddRepos: []string{"repo-b"},
	}, nil)
	if err != nil {
		t.Fatalf("UpdateTicket (additive repos): %v", err)
	}
	if withRepos.Version != noVersionExpect {
		t.Errorf("Version bumped on repo-add-only patch: got %d, want %d", withRepos.Version, noVersionExpect)
	}
	detail2, err := db.GetTicket(ctx, pool, row.ID)
	if err != nil {
		t.Fatalf("GetTicket after repo add: %v", err)
	}
	repoSet := map[string]bool{}
	for _, r := range detail2.Repos {
		repoSet[r] = true
	}
	if !repoSet["repo-a"] || !repoSet["repo-b"] {
		t.Errorf("repos = %v, want both repo-a and repo-b", detail2.Repos)
	}

	// ── Repos whole-set replace is version-guarded ────────────────────────────
	newRepos := []string{"repo-b"}
	curVersion := withRepos.Version
	afterReplace, err := db.UpdateTicket(ctx, pool, row.ID, db.TicketPatch{Repos: &newRepos}, &curVersion)
	if err != nil {
		t.Fatalf("UpdateTicket (repos replace): %v", err)
	}
	if afterReplace.Version != curVersion+1 {
		t.Errorf("Version = %d after repos replace, want %d", afterReplace.Version, curVersion+1)
	}
	detail3, err := db.GetTicket(ctx, pool, row.ID)
	if err != nil {
		t.Fatalf("GetTicket after repos replace: %v", err)
	}
	if len(detail3.Repos) != 1 || detail3.Repos[0] != "repo-b" {
		t.Errorf("Repos after replace = %v, want [repo-b]", detail3.Repos)
	}

	// Repos replace with stale version → ErrStaleVersion
	staleRepos := []string{"repo-a"}
	staleV := curVersion // version is now curVersion+1, so this is stale
	_, err = db.UpdateTicket(ctx, pool, row.ID, db.TicketPatch{Repos: &staleRepos}, &staleV)
	if !errors.Is(err, db.ErrStaleVersion) {
		t.Fatalf("repos replace with stale version: got %v, want ErrStaleVersion", err)
	}

	// ── AddRepos must validate board membership (no FK enforces it) ───────────
	_, err = db.UpdateTicket(ctx, pool, row.ID, db.TicketPatch{AddRepos: []string{"not-on-board"}}, nil)
	if err == nil {
		t.Fatal("UpdateTicket AddRepos with off-board repo: got nil, want rejection")
	}
	// The off-board repo must NOT have been inserted.
	detailAfterReject, err := db.GetTicket(ctx, pool, row.ID)
	if err != nil {
		t.Fatalf("GetTicket after rejected AddRepos: %v", err)
	}
	for _, r := range detailAfterReject.Repos {
		if r == "not-on-board" {
			t.Errorf("off-board repo was inserted despite validation: %v", detailAfterReject.Repos)
		}
	}
}

// TestTicketUpdateSingleVersionBump verifies that a patch carrying BOTH a
// scalar field and a whole-set Repos replace increments version exactly once
// (version+1), not twice. The expectVersion guard stays a single check.
func TestTicketUpdateSingleVersionBump(t *testing.T) {
	pool := connect(t)
	board, _ := setupTicketBoard(t, pool)
	ctx := context.Background()

	row, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID: board.ID,
		Title:   "Combined patch",
		Repos:   []string{"repo-a"},
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if row.Version != 0 {
		t.Fatalf("initial version = %d, want 0", row.Version)
	}

	newTitle := "Renamed"
	newRepos := []string{"repo-b"}
	v0 := 0
	updated, err := db.UpdateTicket(ctx, pool, row.ID, db.TicketPatch{
		Title: &newTitle,
		Repos: &newRepos,
	}, &v0)
	if err != nil {
		t.Fatalf("UpdateTicket (combined scalar+repos): %v", err)
	}
	if updated.Version != 1 {
		t.Errorf("Version = %d after combined patch, want exactly 1", updated.Version)
	}
	if updated.Title != "Renamed" {
		t.Errorf("Title = %q, want Renamed", updated.Title)
	}
	detail, err := db.GetTicket(ctx, pool, row.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if len(detail.Repos) != 1 || detail.Repos[0] != "repo-b" {
		t.Errorf("Repos = %v, want [repo-b]", detail.Repos)
	}
}

// TestTicketListReady verifies the Ready filter:
//   - A ticket with an unsatisfied blocker (blocker in non-terminal column) is excluded.
//   - Once the blocker is moved to a terminal column, the ticket is included.
//   - Once the blocker is archived, the ticket is included.
func TestTicketListReady(t *testing.T) {
	pool := connect(t)
	board, cols := setupTicketBoard(t, pool)
	ctx := context.Background()

	backlogID := cols[0].ID
	doneID := cols[2].ID // terminal

	// Create two tickets: blocker and dependent.
	blocker, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Blocker",
		Repos:    []string{"repo-a"},
		ColumnID: &backlogID,
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	dependent, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Dependent",
		Repos:    []string{"repo-a"},
		ColumnID: &backlogID,
	})
	if err != nil {
		t.Fatalf("create dependent: %v", err)
	}

	// Insert dependency: dependent depends on blocker.
	if _, err := pool.Exec(ctx, `
		INSERT INTO ticket_dependencies (ticket_id, depends_on_ticket_id) VALUES ($1, $2)
	`, dependent.ID, blocker.ID); err != nil {
		t.Fatalf("insert dependency: %v", err)
	}

	// Without Ready filter: both tickets should appear.
	all, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets (all): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListTickets (all): got %d, want 2", len(all))
	}

	// With Ready filter: only blocker (no unsatisfied deps) should appear.
	ready, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Ready: true, Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets (ready): %v", err)
	}
	if len(ready) != 1 || ready[0].ID != blocker.ID {
		t.Errorf("ListTickets (ready): got %v, want [blocker=%s]", ticketIDs(ready), blocker.ID)
	}

	// Move blocker to Done (terminal) → dependent becomes ready.
	if _, err := db.MoveTicket(ctx, pool, blocker.ID, doneID, nil, nil, nil); err != nil {
		t.Fatalf("MoveTicket blocker to Done: %v", err)
	}
	readyAfterMove, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Ready: true, Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets (ready after move): %v", err)
	}
	ids := map[string]bool{}
	for _, r := range readyAfterMove {
		ids[r.ID] = true
	}
	if !ids[blocker.ID] || !ids[dependent.ID] {
		t.Errorf("both should be ready after blocker moves to Done; got %v", ticketIDs(readyAfterMove))
	}

	// Archive blocker and move dependent back to Backlog.
	if _, err := db.ArchiveTicket(ctx, pool, blocker.ID); err != nil {
		t.Fatalf("archive blocker: %v", err)
	}
	if _, err := db.MoveTicket(ctx, pool, dependent.ID, backlogID, nil, nil, nil); err != nil {
		t.Fatalf("move dependent back: %v", err)
	}
	// blocker is archived so it won't appear; dependent should be ready.
	readyAfterArchive, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Ready: true, Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets (ready after archive): %v", err)
	}
	if len(readyAfterArchive) != 1 || readyAfterArchive[0].ID != dependent.ID {
		t.Errorf("after archiving blocker: got %v, want [dependent=%s]", ticketIDs(readyAfterArchive), dependent.ID)
	}
}

// TestTicketPagination verifies that cursor pagination returns stable,
// non-overlapping pages covering all tickets.
func TestTicketPagination(t *testing.T) {
	pool := connect(t)
	board, _ := setupTicketBoard(t, pool)
	ctx := context.Background()

	// Create 7 tickets (odd number to test partial last page).
	const total = 7
	created := make([]db.TicketRow, total)
	for i := 0; i < total; i++ {
		var err error
		created[i], err = db.CreateTicket(ctx, pool, db.TicketInput{
			BoardID: board.ID,
			Title:   fmt.Sprintf("Ticket %d", i+1),
			Repos:   []string{"repo-a"},
		})
		if err != nil {
			t.Fatalf("CreateTicket[%d]: %v", i, err)
		}
	}

	// Paginate with page size 3.
	seen := map[string]bool{}
	cursor := ""
	pageCount := 0
	for {
		page, nextCursor, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{
			Limit:  3,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("ListTickets page %d: %v", pageCount+1, err)
		}
		if len(page) == 0 {
			break
		}
		// No duplicates across pages.
		for _, r := range page {
			if seen[r.ID] {
				t.Errorf("duplicate ticket %s on page %d", r.ID, pageCount+1)
			}
			seen[r.ID] = true
		}
		pageCount++
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	if len(seen) != total {
		t.Errorf("paginated total = %d, want %d", len(seen), total)
	}
	// 7 tickets at page size 3 → pages of 3, 3, 1 = 3 pages.
	if pageCount != 3 {
		t.Errorf("pageCount = %d, want 3", pageCount)
	}
}

// TestTicketListFilters covers column, priority, size, and tag filters.
func TestTicketListFilters(t *testing.T) {
	pool := connect(t)
	board, cols := setupTicketBoard(t, pool)
	ctx := context.Background()

	backlogID := cols[0].ID
	doingID := cols[1].ID

	sz := "S"
	_, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Small high-priority in backlog",
		Repos:    []string{"repo-a"},
		ColumnID: &backlogID,
		Priority: "high",
		Size:     &sz,
		Tags:     []string{"alpha"},
	})
	if err != nil {
		t.Fatalf("CreateTicket 1: %v", err)
	}

	_, err = db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Medium in doing",
		Repos:    []string{"repo-b"},
		ColumnID: &doingID,
		Tags:     []string{"beta"},
	})
	if err != nil {
		t.Fatalf("CreateTicket 2: %v", err)
	}

	// Filter by column.
	byCol, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{ColumnID: &backlogID, Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets byCol: %v", err)
	}
	if len(byCol) != 1 || byCol[0].Title != "Small high-priority in backlog" {
		t.Errorf("byCol = %v, want 1 ticket in backlog", byCol)
	}

	// Filter by priority.
	pri := "high"
	byPri, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Priority: &pri, Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets byPri: %v", err)
	}
	if len(byPri) != 1 {
		t.Errorf("byPri = %d tickets, want 1", len(byPri))
	}

	// Filter by size.
	bySz, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Size: &sz, Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets bySz: %v", err)
	}
	if len(bySz) != 1 {
		t.Errorf("bySz = %d tickets, want 1", len(bySz))
	}

	// Filter by tag.
	byTag, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Tags: []string{"alpha"}, Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets byTag: %v", err)
	}
	if len(byTag) != 1 || byTag[0].Title != "Small high-priority in backlog" {
		t.Errorf("byTag = %v, want 1 ticket with alpha", byTag)
	}

	// Archived tickets are excluded.
	_, err = db.ArchiveTicket(ctx, pool, byCol[0].ID)
	if err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}
	all, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets after archive: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("after archive: got %d tickets, want 1", len(all))
	}
}

// TestTicketGetDepsAndBlocks verifies that GetTicket populates DependsOn and Blocks.
func TestTicketGetDepsAndBlocks(t *testing.T) {
	pool := connect(t)
	board, _ := setupTicketBoard(t, pool)
	ctx := context.Background()

	t1, err := db.CreateTicket(ctx, pool, db.TicketInput{BoardID: board.ID, Title: "T1", Repos: []string{"repo-a"}})
	if err != nil {
		t.Fatalf("create T1: %v", err)
	}
	t2, err := db.CreateTicket(ctx, pool, db.TicketInput{BoardID: board.ID, Title: "T2", Repos: []string{"repo-a"}})
	if err != nil {
		t.Fatalf("create T2: %v", err)
	}

	// T2 depends on T1.
	if _, err := pool.Exec(ctx, `
		INSERT INTO ticket_dependencies (ticket_id, depends_on_ticket_id) VALUES ($1, $2)
	`, t2.ID, t1.ID); err != nil {
		t.Fatalf("insert dependency: %v", err)
	}

	// T1 blocks T2; T2 depends on T1.
	d1, err := db.GetTicket(ctx, pool, t1.ID)
	if err != nil {
		t.Fatalf("GetTicket T1: %v", err)
	}
	if len(d1.DependsOn) != 0 {
		t.Errorf("T1.DependsOn = %v, want empty", d1.DependsOn)
	}
	if len(d1.Blocks) != 1 || d1.Blocks[0] != t2.ID {
		t.Errorf("T1.Blocks = %v, want [%s]", d1.Blocks, t2.ID)
	}

	d2, err := db.GetTicket(ctx, pool, t2.ID)
	if err != nil {
		t.Fatalf("GetTicket T2: %v", err)
	}
	if len(d2.DependsOn) != 1 || d2.DependsOn[0] != t1.ID {
		t.Errorf("T2.DependsOn = %v, want [%s]", d2.DependsOn, t1.ID)
	}
	if len(d2.Blocks) != 0 {
		t.Errorf("T2.Blocks = %v, want empty", d2.Blocks)
	}
}

func ticketIDs(rows []db.TicketRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// TestTicketListTagsReposBlocked verifies that ListTickets populates Tags, Repos,
// and Blocked on each returned row without N+1 queries.
func TestTicketListTagsReposBlocked(t *testing.T) {
	pool := connect(t)
	board, cols := setupTicketBoard(t, pool)
	ctx := context.Background()

	backlogID := cols[0].ID
	doneID := cols[2].ID // terminal

	// blocker has tags ["backend","infra"] and repos ["repo-a","repo-b"] in order.
	blocker, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Blocker",
		Repos:    []string{"repo-a", "repo-b"},
		ColumnID: &backlogID,
		Tags:     []string{"infra", "backend"},
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	// dependent has tags ["frontend"] and repos ["repo-a"].
	dependent, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Dependent",
		Repos:    []string{"repo-a"},
		ColumnID: &backlogID,
		Tags:     []string{"frontend"},
	})
	if err != nil {
		t.Fatalf("create dependent: %v", err)
	}

	// dependent depends on blocker (blocker is in non-terminal column → dep is blocked).
	if _, err := pool.Exec(ctx, `
		INSERT INTO ticket_dependencies (ticket_id, depends_on_ticket_id) VALUES ($1, $2)
	`, dependent.ID, blocker.ID); err != nil {
		t.Fatalf("insert dependency: %v", err)
	}

	// ── Phase 1: tags, repos, and Blocked are populated ──────────────────────
	rows, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	byID := map[string]db.TicketRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}

	// blocker: tags alphabetical, repos in insertion/rank order, Blocked=false.
	b := byID[blocker.ID]
	if len(b.Tags) != 2 || b.Tags[0] != "backend" || b.Tags[1] != "infra" {
		t.Errorf("blocker Tags = %v, want [backend infra]", b.Tags)
	}
	if len(b.Repos) != 2 || b.Repos[0] != "repo-a" || b.Repos[1] != "repo-b" {
		t.Errorf("blocker Repos = %v, want [repo-a repo-b]", b.Repos)
	}
	if b.Blocked {
		t.Errorf("blocker Blocked = true, want false (no unsatisfied deps)")
	}

	// dependent: Blocked=true (blocker in non-terminal column).
	d := byID[dependent.ID]
	if len(d.Tags) != 1 || d.Tags[0] != "frontend" {
		t.Errorf("dependent Tags = %v, want [frontend]", d.Tags)
	}
	if len(d.Repos) != 1 || d.Repos[0] != "repo-a" {
		t.Errorf("dependent Repos = %v, want [repo-a]", d.Repos)
	}
	if !d.Blocked {
		t.Errorf("dependent Blocked = false, want true (unsatisfied dep)")
	}

	// ── Phase 2: move blocker to Done (terminal) → dependent Blocked=false ─────
	if _, err := db.MoveTicket(ctx, pool, blocker.ID, doneID, nil, nil, nil); err != nil {
		t.Fatalf("MoveTicket blocker to Done: %v", err)
	}
	rows2, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets after move: %v", err)
	}
	byID2 := map[string]db.TicketRow{}
	for _, r := range rows2 {
		byID2[r.ID] = r
	}
	if byID2[dependent.ID].Blocked {
		t.Errorf("dependent still Blocked after blocker moved to terminal column")
	}

	// ── Phase 3: archive blocker → only dependent remains, Blocked=false ───────
	if _, err := db.MoveTicket(ctx, pool, dependent.ID, backlogID, nil, nil, nil); err != nil {
		t.Fatalf("move dependent back: %v", err)
	}
	if _, err := db.ArchiveTicket(ctx, pool, blocker.ID); err != nil {
		t.Fatalf("archive blocker: %v", err)
	}
	rows3, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets after archive: %v", err)
	}
	if len(rows3) != 1 {
		t.Fatalf("after archive: got %d rows, want 1", len(rows3))
	}
	if rows3[0].ID != dependent.ID {
		t.Fatalf("after archive: got ticket %s, want dependent %s", rows3[0].ID, dependent.ID)
	}
	if rows3[0].Blocked {
		t.Errorf("dependent Blocked = true after blocker archived, want false")
	}
	// Tags and repos still populated after archive-phase query.
	if len(rows3[0].Tags) != 1 || rows3[0].Tags[0] != "frontend" {
		t.Errorf("dependent Tags after archive = %v, want [frontend]", rows3[0].Tags)
	}
}

// TestGetTicketCard verifies GetTicketCard returns one ticket enriched with
// tags (alphabetical), repos (rank order), and blocked — and works for an
// archived ticket (no archived filter).
func TestGetTicketCard(t *testing.T) {
	pool := connect(t)
	board, cols := setupTicketBoard(t, pool)
	ctx := context.Background()

	backlogID := cols[0].ID

	blocker, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Blocker",
		Repos:    []string{"repo-a"},
		ColumnID: &backlogID,
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	dependent, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID:  board.ID,
		Title:    "Dependent",
		Repos:    []string{"repo-b", "repo-a"},
		ColumnID: &backlogID,
		Tags:     []string{"zeta", "alpha"},
	})
	if err != nil {
		t.Fatalf("create dependent: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ticket_dependencies (ticket_id, depends_on_ticket_id) VALUES ($1, $2)
	`, dependent.ID, blocker.ID); err != nil {
		t.Fatalf("insert dependency: %v", err)
	}

	// dependent: tags alphabetical, repos in rank (insertion) order, Blocked=true.
	card, err := db.GetTicketCard(ctx, pool, dependent.ID)
	if err != nil {
		t.Fatalf("GetTicketCard dependent: %v", err)
	}
	if len(card.Tags) != 2 || card.Tags[0] != "alpha" || card.Tags[1] != "zeta" {
		t.Errorf("Tags = %v, want [alpha zeta]", card.Tags)
	}
	if len(card.Repos) != 2 || card.Repos[0] != "repo-b" || card.Repos[1] != "repo-a" {
		t.Errorf("Repos = %v, want [repo-b repo-a] (rank order)", card.Repos)
	}
	if !card.Blocked {
		t.Errorf("Blocked = false, want true (unsatisfied dep)")
	}

	// blocker: no deps → Blocked=false.
	bCard, err := db.GetTicketCard(ctx, pool, blocker.ID)
	if err != nil {
		t.Fatalf("GetTicketCard blocker: %v", err)
	}
	if bCard.Blocked {
		t.Errorf("blocker Blocked = true, want false")
	}

	// Archive the dependent — GetTicketCard must still find it (no archived filter).
	if _, err := db.ArchiveTicket(ctx, pool, dependent.ID); err != nil {
		t.Fatalf("ArchiveTicket: %v", err)
	}
	archCard, err := db.GetTicketCard(ctx, pool, dependent.ID)
	if err != nil {
		t.Fatalf("GetTicketCard archived: %v", err)
	}
	if archCard.ArchivedAt == nil {
		t.Errorf("archived card ArchivedAt = nil, want set")
	}
	if len(archCard.Tags) != 2 {
		t.Errorf("archived card Tags = %v, want 2 entries", archCard.Tags)
	}
}

// TestListTicketsArchivedOnly verifies that TicketFilter.ArchivedOnly=true
// returns only archived tickets (ordered by archived_at DESC, id ASC), that
// default (false) still returns only live tickets, and that pagination works.
func TestListTicketsArchivedOnly(t *testing.T) {
	pool := connect(t)
	board, _ := setupTicketBoard(t, pool)
	ctx := context.Background()

	// Create 3 tickets (all land in Backlog by default).
	tk1, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID: board.ID, Title: "Archived 1", Repos: []string{"repo-a"},
	})
	if err != nil {
		t.Fatalf("CreateTicket 1: %v", err)
	}
	tk2, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID: board.ID, Title: "Archived 2", Repos: []string{"repo-a"},
	})
	if err != nil {
		t.Fatalf("CreateTicket 2: %v", err)
	}
	live, err := db.CreateTicket(ctx, pool, db.TicketInput{
		BoardID: board.ID, Title: "Live ticket", Repos: []string{"repo-a"},
	})
	if err != nil {
		t.Fatalf("CreateTicket live: %v", err)
	}

	// Archive tk1 and tk2.
	if _, err := db.ArchiveTicket(ctx, pool, tk1.ID); err != nil {
		t.Fatalf("ArchiveTicket 1: %v", err)
	}
	if _, err := db.ArchiveTicket(ctx, pool, tk2.ID); err != nil {
		t.Fatalf("ArchiveTicket 2: %v", err)
	}

	// ArchivedOnly=true → 2 archived, live not included.
	archived, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{ArchivedOnly: true, Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets(ArchivedOnly=true): %v", err)
	}
	if len(archived) != 2 {
		t.Fatalf("ArchivedOnly=true: want 2 tickets, got %d", len(archived))
	}
	for _, tk := range archived {
		if tk.ID == live.ID {
			t.Errorf("live ticket %s appeared in ArchivedOnly=true results", live.ID)
		}
		if tk.ArchivedAt == nil {
			t.Errorf("ticket %s has nil ArchivedAt in archived-only results", tk.ID)
		}
	}

	// ArchivedOnly=false (default) → only the live ticket.
	liveRows, _, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets(default): %v", err)
	}
	if len(liveRows) != 1 {
		t.Fatalf("default: want 1 live ticket, got %d", len(liveRows))
	}
	if liveRows[0].ID != live.ID {
		t.Errorf("default: got ticket %q, want live %q", liveRows[0].ID, live.ID)
	}

	// Pagination of archived: limit=1 yields a cursor, page 2 covers the rest.
	page1, cur1, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{ArchivedOnly: true, Limit: 1})
	if err != nil {
		t.Fatalf("ListTickets(ArchivedOnly, Limit=1) p1: %v", err)
	}
	if len(page1) != 1 {
		t.Fatalf("page1: want 1, got %d", len(page1))
	}
	if cur1 == "" {
		t.Fatal("page1: next_cursor should be set (2 archived tickets, page size 1)")
	}
	page2, cur2, err := db.ListTickets(ctx, pool, board.ID, db.TicketFilter{ArchivedOnly: true, Limit: 1, Cursor: cur1})
	if err != nil {
		t.Fatalf("ListTickets(ArchivedOnly, Limit=1) p2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2: want 1, got %d", len(page2))
	}
	if page1[0].ID == page2[0].ID {
		t.Errorf("page1 and page2 return same ticket %q; want different", page1[0].ID)
	}
	if cur2 != "" {
		t.Errorf("page2 next_cursor = %q, want empty (last page)", cur2)
	}

	// Repos are batch-fetched (each ticket has repo-a).
	for _, row := range archived {
		if len(row.Repos) != 1 || row.Repos[0] != "repo-a" {
			t.Errorf("ticket %s: Repos = %v, want [repo-a]", row.ID, row.Repos)
		}
	}
}
