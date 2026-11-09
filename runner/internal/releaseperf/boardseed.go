package releaseperf

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	rdb "github.com/blerglab/blerg-ai/runner/internal/db"
)

const boardSeedCardCount = 150

// seedBoardName distinguishes the release-perf board from any other board on
// the account, for the idempotency check below.
const seedBoardName = "Release-perf board"

// seedBoardRepo is the one repo every seeded ticket must belong to
// (CreateTicket validates that every ticket repo is one of its board's own
// repos).
const seedBoardRepo = "/repos/release-perf"

// SeedBoardDataset creates one board with boardSeedCardCount-or-more tickets
// spread across its columns, through runner's own real board functions
// (runner/internal/db's CreateBoard/AddColumn/CreateTicket) against
// runnerPool — never the separate, standalone board/web service: the
// board-tab scenario's route (/boards/:id, HandleGetBoard) reads runner's
// own "boards" table (runner/internal/db/migrations/009_boards.sql), a
// distinct schema from board/internal/db's — seeding the latter (an earlier
// version of this seeder did, with raw SQL since that module can't be
// imported from here) left runner's own table never knowing the board
// existed, so every /api/boards/{id} call 404'd.
//
// It is idempotent: a prior release-perf board, found by its fixed name, is
// replaced rather than duplicated.
func SeedBoardDataset(ctx context.Context, runnerPool *pgxpool.Pool) (string, int, error) {
	boards, err := rdb.ListBoards(ctx, runnerPool)
	if err != nil {
		return "", 0, fmt.Errorf("list boards: %w", err)
	}
	for _, b := range boards {
		if b.Name == seedBoardName {
			if err := rdb.DeleteBoard(ctx, runnerPool, b.ID); err != nil {
				return "", 0, fmt.Errorf("clear prior seeded board: %w", err)
			}
			break
		}
	}

	description := "Seeded for BLG-453 release performance checks"
	board, err := rdb.CreateBoard(ctx, runnerPool, seedBoardName, &description, []string{seedBoardRepo}, nil)
	if err != nil {
		return "", 0, fmt.Errorf("create board: %w", err)
	}

	cols, err := rdb.ListColumns(ctx, runnerPool, board.ID)
	if err != nil {
		return "", 0, fmt.Errorf("list seeded default columns: %w", err)
	}
	// CreateBoard already seeds Backlog/Doing/Done; add one more non-
	// terminal column so cards spread across more than the bare default,
	// matching the design's "several columns" ask.
	extra, err := rdb.AddColumn(ctx, runnerPool, board.ID, "Ready", &cols[0].ID, false)
	if err != nil {
		return "", 0, fmt.Errorf("add extra column: %w", err)
	}
	cols = append(cols, extra)

	for n := 1; n <= boardSeedCardCount; n++ {
		col := cols[n%len(cols)]
		risk := []string{"low", "medium", "high"}[n%3]
		size := []string{"S", "M", "L", "XL"}[n%4]
		body := "Seeded for the board-tab scenario in BLG-453's release performance check."
		if _, err := rdb.CreateTicket(ctx, runnerPool, rdb.TicketInput{
			BoardID:  board.ID,
			Title:    fmt.Sprintf("Release-perf seeded card %d", n),
			Body:     &body,
			Repos:    []string{seedBoardRepo},
			ColumnID: &col.ID,
			Priority: []string{"low", "medium", "high", "urgent"}[n%4],
			Size:     &size,
			Risk:     &risk,
		}); err != nil {
			return "", 0, fmt.Errorf("create ticket %d: %w", n, err)
		}
	}
	return board.ID, boardSeedCardCount, nil
}
