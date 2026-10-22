package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ResolveBoardRepo renames the board's repo from (a bare desktop folder name) to to (the owner/name
// its remote points at), on the board and on every card of the board that names it, in one
// transaction, keeping each one's place: the board's order (its first repo is the board sessions'
// working directory) and each card's rank (its first is the card's primary). A board or card that
// already names to keeps one entry, at the earlier place. ErrNotFound when
// the board no longer has from.
func ResolveBoardRepo(ctx context.Context, pool *pgxpool.Pool, boardID, from, to string) (Board, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Board{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	if err := lockBoard(ctx, tx, boardID); err != nil {
		return Board{}, err
	}
	// The board's order is kept: the renamed repo stays where from was (the first is the board
	// sessions' working directory). When the board already has to, it keeps the earlier place.
	var fromPos int
	err = tx.QueryRow(ctx, `SELECT position FROM board_repos WHERE board_id = $1 AND repo = $2`, boardID, from).Scan(&fromPos)
	if errors.Is(err, pgx.ErrNoRows) {
		return Board{}, ErrNotFound
	}
	if err != nil {
		return Board{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE board_repos SET position = LEAST(position, $3) WHERE board_id = $1 AND repo = $2`, boardID, to, fromPos); err != nil {
		return Board{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE board_repos SET repo = $3 WHERE board_id = $1 AND repo = $2
		   AND NOT EXISTS (SELECT 1 FROM board_repos o WHERE o.board_id = $1 AND o.repo = $3)`, boardID, from, to); err != nil {
		return Board{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM board_repos WHERE board_id = $1 AND repo = $2`, boardID, from); err != nil {
		return Board{}, err
	}
	// Every card that names from is a mutation of that card: its version moves.
	if _, err := tx.Exec(ctx, `
		UPDATE cards SET version = version + 1, updated_at = now()
		 WHERE board_id = $1 AND id IN (SELECT card_id FROM card_repos WHERE repo = $2)`, boardID, from); err != nil {
		return Board{}, err
	}
	// A card naming both keeps to at the earlier of the two ranks, so a card whose primary was
	// from keeps the resolved repo as its primary.
	if _, err := tx.Exec(ctx, `
		UPDATE card_repos t SET rank = f.rank FROM card_repos f
		 WHERE t.card_id = f.card_id AND t.repo = $3 AND f.repo = $2 AND f.rank < t.rank
		   AND t.card_id IN (SELECT id FROM cards WHERE board_id = $1)`, boardID, from, to); err != nil {
		return Board{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE card_repos SET repo = $3
		 WHERE repo = $2 AND card_id IN (SELECT id FROM cards WHERE board_id = $1)
		   AND NOT EXISTS (SELECT 1 FROM card_repos o WHERE o.card_id = card_repos.card_id AND o.repo = $3)`, boardID, from, to); err != nil {
		return Board{}, err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM card_repos WHERE repo = $2 AND card_id IN (SELECT id FROM cards WHERE board_id = $1)`, boardID, from); err != nil {
		return Board{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE boards SET updated_at = now() WHERE id = $1`, boardID); err != nil {
		return Board{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Board{}, err
	}
	return GetBoard(ctx, pool, boardID)
}
