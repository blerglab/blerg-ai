package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrColumnHasLiveTickets is returned by DeleteColumn when the column still
// has live (non-archived) tickets referencing it.
var ErrColumnHasLiveTickets = errors.New("column has live tickets")

// ─── Row types ────────────────────────────────────────────────────────────────

// BoardRow is a row from the boards table.
type BoardRow struct {
	ID              string
	Name            string
	Description     *string
	DefaultDaemonID *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ColumnRow is a row from the columns table.
type ColumnRow struct {
	ID         string
	BoardID    string
	Rank       string
	Name       string
	IsTerminal bool
	CreatedAt  time.Time
}

// ─── Boards ───────────────────────────────────────────────────────────────────

// CreateBoard creates a new board with the given name, optional description,
// repos, and optional default daemon ID. It seeds three default columns —
// Backlog, Doing, Done (in that order) — with Done marked as terminal, and
// inserts the given repos into board_repos. All writes happen in one
// transaction.
func CreateBoard(ctx context.Context, pool *pgxpool.Pool, name string, description *string, repos []string, defaultDaemonID *string) (BoardRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return BoardRow{}, fmt.Errorf("CreateBoard begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned

	var row BoardRow
	err = tx.QueryRow(ctx, `
		INSERT INTO boards (name, description, default_daemon_id)
		VALUES ($1, $2, $3)
		RETURNING id, name, description, default_daemon_id, created_at, updated_at
	`, name, description, defaultDaemonID).Scan(
		&row.ID, &row.Name, &row.Description, &row.DefaultDaemonID,
		&row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		return BoardRow{}, fmt.Errorf("CreateBoard insert board: %w", err)
	}

	// Insert board_repos.
	for _, repo := range repos {
		if _, err := tx.Exec(ctx, `
			INSERT INTO board_repos (board_id, repo) VALUES ($1, $2)
		`, row.ID, repo); err != nil {
			return BoardRow{}, fmt.Errorf("CreateBoard insert board_repo %s: %w", repo, err)
		}
	}

	// Seed default columns: Backlog, Doing, Done.
	backlogRank := RankInitial()
	doingRank, err := RankBetween(backlogRank, "")
	if err != nil {
		return BoardRow{}, fmt.Errorf("CreateBoard rank doing: %w", err)
	}
	doneRank, err := RankBetween(doingRank, "")
	if err != nil {
		return BoardRow{}, fmt.Errorf("CreateBoard rank done: %w", err)
	}

	defaultCols := []struct {
		name       string
		rank       string
		isTerminal bool
	}{
		{"Backlog", backlogRank, false},
		{"Doing", doingRank, false},
		{"Done", doneRank, true},
	}
	for _, col := range defaultCols {
		if _, err := tx.Exec(ctx, `
			INSERT INTO columns (board_id, rank, name, is_terminal)
			VALUES ($1, $2, $3, $4)
		`, row.ID, col.rank, col.name, col.isTerminal); err != nil {
			return BoardRow{}, fmt.Errorf("CreateBoard insert column %s: %w", col.name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return BoardRow{}, fmt.Errorf("CreateBoard commit: %w", err)
	}
	return row, nil
}

// ListBoards returns all boards ordered by created_at ascending.
func ListBoards(ctx context.Context, pool *pgxpool.Pool) ([]BoardRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, description, default_daemon_id, created_at, updated_at
		  FROM boards
		 ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("ListBoards: %w", err)
	}
	return scanBoardRows(rows)
}

// GetBoard looks up a board by ID. Returns an error wrapping pgx.ErrNoRows if
// not found.
func GetBoard(ctx context.Context, pool *pgxpool.Pool, id string) (BoardRow, error) {
	var row BoardRow
	err := pool.QueryRow(ctx, `
		SELECT id, name, description, default_daemon_id, created_at, updated_at
		  FROM boards
		 WHERE id = $1
	`, id).Scan(&row.ID, &row.Name, &row.Description, &row.DefaultDaemonID,
		&row.CreatedAt, &row.UpdatedAt)
	if err != nil {
		return BoardRow{}, fmt.Errorf("GetBoard: %w", err)
	}
	return row, nil
}

// DeleteBoard deletes a board by ID. Child rows (board_repos, columns,
// tickets) are removed by ON DELETE CASCADE.
func DeleteBoard(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM boards WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("DeleteBoard: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("DeleteBoard: %w", pgx.ErrNoRows)
	}
	return nil
}

// ListBoardRepos returns the repos associated with a board. The board_repos
// table has no ordering column, so repos are returned alphabetically for a
// deterministic, stable order.
func ListBoardRepos(ctx context.Context, pool *pgxpool.Pool, boardID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT repo FROM board_repos WHERE board_id = $1 ORDER BY repo ASC
	`, boardID)
	if err != nil {
		return nil, fmt.Errorf("ListBoardRepos: %w", err)
	}
	defer rows.Close()
	var repos []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, fmt.Errorf("ListBoardRepos scan: %w", err)
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

func scanBoardRows(rows pgx.Rows) ([]BoardRow, error) {
	defer rows.Close()
	var result []BoardRow
	for rows.Next() {
		var r BoardRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.DefaultDaemonID,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanBoardRows: %w", err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ─── Columns ──────────────────────────────────────────────────────────────────

// AddColumn adds a new column to a board. If afterColumnID is non-nil the new
// column is inserted immediately after it (before the column that currently
// follows it); otherwise it is appended at the end.
//
// Rank resolution and the INSERT run in one transaction that first locks the
// board row (SELECT … FOR UPDATE), serialising concurrent column-ordering
// operations on the same board. Without this, two concurrent adds into the same
// gap could read identical neighbor ranks and produce a duplicate rank — there
// is intentionally no unique constraint on (board_id, rank).
func AddColumn(ctx context.Context, pool *pgxpool.Pool, boardID, name string, afterColumnID *string, terminal bool) (ColumnRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ColumnRow{}, fmt.Errorf("AddColumn begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned

	// Serialise per-board column ordering by locking the board row.
	if _, err := tx.Exec(ctx, `SELECT id FROM boards WHERE id = $1 FOR UPDATE`, boardID); err != nil {
		return ColumnRow{}, fmt.Errorf("AddColumn lock board: %w", err)
	}

	var prevRank, nextRank string

	if afterColumnID != nil {
		// Rank of the column we insert after.
		if err := tx.QueryRow(ctx, `
			SELECT rank FROM columns WHERE id = $1
		`, *afterColumnID).Scan(&prevRank); err != nil {
			return ColumnRow{}, fmt.Errorf("AddColumn get afterColumn rank: %w", err)
		}
		// Rank of the next column in the same board (if any).
		err := tx.QueryRow(ctx, `
			SELECT rank FROM columns
			 WHERE board_id = $1 AND rank > $2
			 ORDER BY rank ASC
			 LIMIT 1
		`, boardID, prevRank).Scan(&nextRank)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return ColumnRow{}, fmt.Errorf("AddColumn get next rank: %w", err)
		}
		// nextRank is "" if no following column exists; RankBetween handles that.
	} else {
		// Append at end: prevRank = current max rank in board (or "" if empty).
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(rank), '') FROM columns WHERE board_id = $1
		`, boardID).Scan(&prevRank); err != nil {
			return ColumnRow{}, fmt.Errorf("AddColumn get max rank: %w", err)
		}
		nextRank = ""
	}

	rank, err := RankBetween(prevRank, nextRank)
	if err != nil {
		return ColumnRow{}, fmt.Errorf("AddColumn compute rank: %w", err)
	}

	var col ColumnRow
	if err := tx.QueryRow(ctx, `
		INSERT INTO columns (board_id, rank, name, is_terminal)
		VALUES ($1, $2, $3, $4)
		RETURNING id, board_id, rank, name, is_terminal, created_at
	`, boardID, rank, name, terminal).Scan(
		&col.ID, &col.BoardID, &col.Rank, &col.Name, &col.IsTerminal, &col.CreatedAt,
	); err != nil {
		return ColumnRow{}, fmt.Errorf("AddColumn insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ColumnRow{}, fmt.Errorf("AddColumn commit: %w", err)
	}
	return col, nil
}

// RenameColumn changes the name of a column and returns the updated row.
func RenameColumn(ctx context.Context, pool *pgxpool.Pool, id, name string) (ColumnRow, error) {
	var col ColumnRow
	err := pool.QueryRow(ctx, `
		UPDATE columns SET name = $2 WHERE id = $1
		RETURNING id, board_id, rank, name, is_terminal, created_at
	`, id, name).Scan(
		&col.ID, &col.BoardID, &col.Rank, &col.Name, &col.IsTerminal, &col.CreatedAt,
	)
	if err != nil {
		return ColumnRow{}, fmt.Errorf("RenameColumn: %w", err)
	}
	return col, nil
}

// SetColumnTerminal sets the is_terminal flag on a column and returns the
// updated row.
func SetColumnTerminal(ctx context.Context, pool *pgxpool.Pool, id string, terminal bool) (ColumnRow, error) {
	var col ColumnRow
	err := pool.QueryRow(ctx, `
		UPDATE columns SET is_terminal = $2 WHERE id = $1
		RETURNING id, board_id, rank, name, is_terminal, created_at
	`, id, terminal).Scan(
		&col.ID, &col.BoardID, &col.Rank, &col.Name, &col.IsTerminal, &col.CreatedAt,
	)
	if err != nil {
		return ColumnRow{}, fmt.Errorf("SetColumnTerminal: %w", err)
	}
	return col, nil
}

// MoveColumn reorders a column by computing a new rank between the given
// neighbor columns. after is the column this should come after (its rank
// becomes prevRank); before is the column this should come before (its rank
// becomes nextRank). Either may be nil (no bound on that side).
//
// Like AddColumn, the read-neighbor-ranks-then-write sequence runs in one
// transaction that first locks the board row (SELECT … FOR UPDATE), serialising
// concurrent reorders on the same board so they cannot read identical neighbor
// ranks and collide on rank.
func MoveColumn(ctx context.Context, pool *pgxpool.Pool, id string, after, before *string) (ColumnRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ColumnRow{}, fmt.Errorf("MoveColumn begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned

	// Lock the board row to serialise per-board column ordering.
	var boardID string
	if err := tx.QueryRow(ctx, `SELECT board_id FROM columns WHERE id = $1`, id).Scan(&boardID); err != nil {
		return ColumnRow{}, fmt.Errorf("MoveColumn get board_id: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM boards WHERE id = $1 FOR UPDATE`, boardID); err != nil {
		return ColumnRow{}, fmt.Errorf("MoveColumn lock board: %w", err)
	}

	var prevRank, nextRank string

	if after != nil {
		if err := tx.QueryRow(ctx, `
			SELECT rank FROM columns WHERE id = $1
		`, *after).Scan(&prevRank); err != nil {
			return ColumnRow{}, fmt.Errorf("MoveColumn get after rank: %w", err)
		}
	}
	if before != nil {
		if err := tx.QueryRow(ctx, `
			SELECT rank FROM columns WHERE id = $1
		`, *before).Scan(&nextRank); err != nil {
			return ColumnRow{}, fmt.Errorf("MoveColumn get before rank: %w", err)
		}
	}

	rank, err := RankBetween(prevRank, nextRank)
	if err != nil {
		return ColumnRow{}, fmt.Errorf("MoveColumn compute rank: %w", err)
	}

	var col ColumnRow
	if err := tx.QueryRow(ctx, `
		UPDATE columns SET rank = $2 WHERE id = $1
		RETURNING id, board_id, rank, name, is_terminal, created_at
	`, id, rank).Scan(
		&col.ID, &col.BoardID, &col.Rank, &col.Name, &col.IsTerminal, &col.CreatedAt,
	); err != nil {
		return ColumnRow{}, fmt.Errorf("MoveColumn update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ColumnRow{}, fmt.Errorf("MoveColumn commit: %w", err)
	}
	return col, nil
}

// DeleteColumn deletes a column. Returns ErrColumnHasLiveTickets when any
// live (non-archived, i.e. archived_at IS NULL) ticket still references it.
func DeleteColumn(ctx context.Context, pool *pgxpool.Pool, id string) error {
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM tickets
		 WHERE column_id = $1 AND archived_at IS NULL
	`, id).Scan(&count); err != nil {
		return fmt.Errorf("DeleteColumn check live tickets: %w", err)
	}
	if count > 0 {
		return ErrColumnHasLiveTickets
	}

	tag, err := pool.Exec(ctx, `DELETE FROM columns WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("DeleteColumn: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("DeleteColumn: %w", pgx.ErrNoRows)
	}
	return nil
}

// ColumnBoardID returns the board_id of the column with the given id.
// Returns an error wrapping pgx.ErrNoRows when the column doesn't exist.
func ColumnBoardID(ctx context.Context, pool *pgxpool.Pool, columnID string) (string, error) {
	var boardID string
	err := pool.QueryRow(ctx, `SELECT board_id FROM columns WHERE id = $1`, columnID).Scan(&boardID)
	if err != nil {
		return "", fmt.Errorf("ColumnBoardID: %w", err)
	}
	return boardID, nil
}

// GetColumn looks up a column by ID. Returns an error wrapping pgx.ErrNoRows if
// not found.
func GetColumn(ctx context.Context, pool *pgxpool.Pool, id string) (ColumnRow, error) {
	var col ColumnRow
	err := pool.QueryRow(ctx, `
		SELECT id, board_id, rank, name, is_terminal, created_at
		  FROM columns WHERE id = $1
	`, id).Scan(&col.ID, &col.BoardID, &col.Rank, &col.Name, &col.IsTerminal, &col.CreatedAt)
	if err != nil {
		return ColumnRow{}, fmt.Errorf("GetColumn: %w", err)
	}
	return col, nil
}

// ListColumns returns all columns for a board ordered by rank ascending.
func ListColumns(ctx context.Context, pool *pgxpool.Pool, boardID string) ([]ColumnRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, board_id, rank, name, is_terminal, created_at
		  FROM columns
		 WHERE board_id = $1
		 ORDER BY rank ASC
	`, boardID)
	if err != nil {
		return nil, fmt.Errorf("ListColumns: %w", err)
	}
	return scanColumnRows(rows)
}

func scanColumnRows(rows pgx.Rows) ([]ColumnRow, error) {
	defer rows.Close()
	var result []ColumnRow
	for rows.Next() {
		var r ColumnRow
		if err := rows.Scan(&r.ID, &r.BoardID, &r.Rank, &r.Name,
			&r.IsTerminal, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanColumnRows: %w", err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
