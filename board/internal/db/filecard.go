package db

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrFilingIntentUsed = errors.New("the approved filing intent has already been consumed")
var ErrSensitiveBodyForbidden = errors.New("sensitive reproduction details require board administration")

type FilingReceipt struct {
	ID      string `json:"id"`
	BoardID string `json:"board_id"`
	Number  int    `json:"number"`
}

// FileApprovedCard consumes a signed intent and inserts exactly one new card in the same
// transaction. Neither dedup refresh nor external-id upsert is reachable through this path.
func FileApprovedCard(ctx context.Context, pool *pgxpool.Pool, boardID, account, intent, digest string, draft boardfiling.Draft, ev EventMeta) (FilingReceipt, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return FilingReceipt{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `INSERT INTO filed_card_intents(intent,account_id,board_id,digest) VALUES($1,$2,$3,$4) ON CONFLICT(intent) DO NOTHING`, intent, account, boardID, digest)
	if err != nil {
		return FilingReceipt{}, false, err
	}
	if tag.RowsAffected() == 0 {
		var priorAccount, priorDigest string
		var id *string
		var number *int
		var receipt FilingReceipt
		err := tx.QueryRow(ctx, `SELECT account_id::text,board_id::text,digest,card_id::text,number FROM filed_card_intents WHERE intent=$1`, intent).Scan(&priorAccount, &receipt.BoardID, &priorDigest, &id, &number)
		if err != nil {
			return FilingReceipt{}, false, err
		}
		if priorAccount != account || receipt.BoardID != boardID || priorDigest != digest || id == nil || number == nil {
			return FilingReceipt{}, false, ErrFilingIntentUsed
		}
		receipt.ID, receipt.Number = *id, *number
		return receipt, true, nil
	}
	if err := lockBoard(ctx, tx, boardID); err != nil {
		return FilingReceipt{}, false, err
	}
	board, err := scanBoard(tx.QueryRow(ctx, `SELECT `+boardCols+` FROM boards WHERE id=$1`, boardID))
	if err != nil {
		return FilingReceipt{}, false, err
	}
	origin := draft.Origin
	if !board.IncludeComponentVersion {
		origin.ComponentVersion, origin.ComponentCommit = "", ""
	}
	rawOrigin, err := json.Marshal(origin)
	if err != nil {
		return FilingReceipt{}, false, err
	}
	params := CardParams{Title: &draft.Title, Type: &draft.Type}
	if !draft.Sensitive {
		params.Body = &draft.Body
	}
	// Target repository defaults are board-owned; the originating repository is separate metadata.
	if len(draft.Repos) > 0 {
		params.Repos = &draft.Repos
	} else if board.RequireRepo {
		rows, err := tx.Query(ctx, `SELECT repo FROM board_repos WHERE board_id=$1 ORDER BY position, repo`, boardID)
		if err != nil {
			return FilingReceipt{}, false, err
		}
		var repos []string
		for rows.Next() {
			var repo string
			if err := rows.Scan(&repo); err != nil {
				rows.Close()
				return FilingReceipt{}, false, err
			}
			repos = append(repos, repo)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return FilingReceipt{}, false, err
		}
		if len(repos) != 1 {
			return FilingReceipt{}, false, &FieldError{Key: "repos", Msg: "choose the target repository before filing on this board"}
		}
		params.Repos = &repos
	}

	result, err := createCardTx(ctx, tx, boardID, params, ev)
	if err != nil {
		return FilingReceipt{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE cards SET sensitive=$2,origin=$3 WHERE id=$1`, result.Card.ID, draft.Sensitive, rawOrigin); err != nil {
		return FilingReceipt{}, false, err
	}
	if draft.Sensitive {
		if _, err := tx.Exec(ctx, `INSERT INTO sensitive_card_details(card_id,body) VALUES($1,$2)`, result.Card.ID, draft.Body); err != nil {
			return FilingReceipt{}, false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE filed_card_intents SET card_id=$2,number=$3 WHERE intent=$1`, intent, result.Card.ID, result.Card.Number); err != nil {
		return FilingReceipt{}, false, err
	}
	receipt := FilingReceipt{ID: result.Card.ID, BoardID: boardID, Number: result.Card.Number}
	return receipt, false, tx.Commit(ctx)
}

// SensitiveCardBody is intentionally separate from all general card/search/event readers.
// Callers must positively authorize board administration before invoking it.
func SensitiveCardBody(ctx context.Context, pool *pgxpool.Pool, cardID string) (*string, error) {
	var body string
	err := pool.QueryRow(ctx, `SELECT body FROM sensitive_card_details WHERE card_id=$1`, cardID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &body, err
}
