package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrBoardIntentUsed: the approved board creation intent was already consumed by another account
// or another draft.
var ErrBoardIntentUsed = errors.New("the approved board creation intent has already been used")

// CreateApprovedBoard consumes a signed board creation intent and writes exactly one board in the
// same transaction. The same intent with the same account and digest (a retried approval) answers
// the board it created (replayed); any other reuse is ErrBoardIntentUsed.
func CreateApprovedBoard(ctx context.Context, pool *pgxpool.Pool, p BoardParams, account, intent, digest string) (Board, bool, error) {
	before := func(tx pgx.Tx) (*Board, error) {
		tag, err := tx.Exec(ctx, `INSERT INTO board_create_intents(intent,account_id,digest) VALUES($1,$2,$3) ON CONFLICT(intent) DO NOTHING`, intent, account, digest)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 1 {
			return nil, nil
		}
		var priorAccount, priorDigest string
		var boardID *string
		if err := tx.QueryRow(ctx, `SELECT account_id::text,digest,board_id::text FROM board_create_intents WHERE intent=$1`, intent).Scan(&priorAccount, &priorDigest, &boardID); err != nil {
			return nil, err
		}
		if priorAccount != account || priorDigest != digest || boardID == nil {
			return nil, ErrBoardIntentUsed
		}
		b, err := scanBoard(tx.QueryRow(ctx, `SELECT `+boardCols+` FROM boards WHERE id=$1`, *boardID))
		if errors.Is(err, pgx.ErrNoRows) {
			// The board was deleted since: the intent stays spent.
			return nil, ErrBoardIntentUsed
		}
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT repo FROM board_repos WHERE board_id=$1 ORDER BY position, repo`, b.ID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				return nil, err
			}
			b.Repos = append(b.Repos, r)
		}
		return &b, rows.Err()
	}
	after := func(tx pgx.Tx, b Board) error {
		_, err := tx.Exec(ctx, `UPDATE board_create_intents SET board_id=$2 WHERE intent=$1`, intent, b.ID)
		return err
	}
	return createBoard(ctx, pool, p, before, after)
}
