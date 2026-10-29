package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ColumnActor is who made a column change: the principal's kind, the person it acts for, its token,
// and (for a runner session's board access) the session.
type ColumnActor struct {
	Actor, Account, Token, RunnerSession string
}

// ColumnAudit asks a column change to record its audit row in the change's own transaction: the
// change commits only together with its audit.
type ColumnAudit struct {
	Who ColumnActor
}

// ColumnEvent is one row of a board's column audit.
type ColumnEvent struct {
	ID            int64           `json:"id"`
	BoardID       string          `json:"board_id"`
	ColumnID      string          `json:"column_id"`
	Type          string          `json:"type"`
	Actor         string          `json:"actor"`
	ActorAccount  string          `json:"actor_account,omitempty"`
	ActorToken    string          `json:"actor_token,omitempty"`
	RunnerSession string          `json:"runner_session,omitempty"`
	Data          json.RawMessage `json:"data"`
	CreatedAt     time.Time       `json:"created_at"`
}

// recordColumnAudit writes the audit row of a column change inside its transaction (nothing when
// the caller asked for no audit).
func recordColumnAudit(ctx context.Context, tx pgx.Tx, boardID, columnID, typ string, audit []ColumnAudit, data map[string]any) error {
	if len(audit) == 0 {
		return nil
	}
	who := audit[0].Who
	raw := []byte(`{}`)
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		raw = b
	}
	actor := who.Actor
	if actor == "" {
		actor = "human"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO column_events (board_id, column_id, type, actor, actor_account, actor_token, runner_session, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		boardID, columnID, typ, actor, who.Account, who.Token, who.RunnerSession, raw); err != nil {
		return fmt.Errorf("column audit: %w", err)
	}
	return nil
}

// ListColumnEvents is a board's column audit, oldest first.
func ListColumnEvents(ctx context.Context, pool *pgxpool.Pool, boardID string) ([]ColumnEvent, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, board_id, column_id, type, actor, actor_account, actor_token, runner_session, data, created_at
		FROM column_events WHERE board_id = $1 ORDER BY id`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ColumnEvent{}
	for rows.Next() {
		var e ColumnEvent
		if err := rows.Scan(&e.ID, &e.BoardID, &e.ColumnID, &e.Type, &e.Actor, &e.ActorAccount, &e.ActorToken,
			&e.RunnerSession, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateColumn changes a column's name and/or terminal flag (nil leaves it as it is), with its audit
// in the same transaction.
func UpdateColumn(ctx context.Context, pool *pgxpool.Pool, columnID string, name *string, isTerminal *bool, audit ...ColumnAudit) (Column, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Column{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	var before Column
	if err := tx.QueryRow(ctx, `SELECT id, board_id, rank, name, is_terminal FROM board_columns WHERE id = $1 FOR UPDATE`, columnID).
		Scan(&before.ID, &before.BoardID, &before.Rank, &before.Name, &before.IsTerminal); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Column{}, ErrNotFound
		}
		return Column{}, err
	}
	var c Column
	if err := tx.QueryRow(ctx, `
		UPDATE board_columns SET name = coalesce($2, name), is_terminal = coalesce($3, is_terminal)
		WHERE id = $1 RETURNING id, board_id, rank, name, is_terminal`,
		columnID, name, isTerminal).Scan(&c.ID, &c.BoardID, &c.Rank, &c.Name, &c.IsTerminal); err != nil {
		return Column{}, err
	}
	if err := recordColumnAudit(ctx, tx, c.BoardID, c.ID, "updated", audit, map[string]any{
		"from": map[string]any{"name": before.Name, "is_terminal": before.IsTerminal},
		"to":   map[string]any{"name": c.Name, "is_terminal": c.IsTerminal}}); err != nil {
		return Column{}, err
	}
	return c, tx.Commit(ctx)
}

// ErrMoveTargetIsColumn: a column's cards cannot be moved into the column being removed.
var ErrMoveTargetIsColumn = errors.New("move the cards to another column, not the one being removed")

// ErrColumnCardsChanged: the column's cards changed between the admission checks and the removal.
var ErrColumnCardsChanged = errors.New("the column's cards changed while it was being removed; try again")

// ColumnCardIDs are the cards in a column, in their order.
func ColumnCardIDs(ctx context.Context, pool *pgxpool.Pool, columnID string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM cards WHERE column_id = $1 ORDER BY rank, id`, columnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteColumnMovingCards removes a column, first moving its cards, in their order, to the end of
// toColumnID on the same board, exactly as a card move does: a "moved" card event each (with its
// admission review from reviews), and the standing agents of the destination queued. One
// transaction under the board lock, with the column's audit row: either every card moves, the
// column goes and the change is audited, or nothing changes. gated are the cards the caller
// admitted; if the column now holds any others, nothing changes (ErrColumnCardsChanged).
func DeleteColumnMovingCards(ctx context.Context, pool *pgxpool.Pool, columnID, toColumnID string, gated []string, ev EventMeta, reviews map[string]*string, audit ...ColumnAudit) ([]string, error) {
	if columnID == toColumnID {
		return nil, ErrMoveTargetIsColumn
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	var boardID, name string
	if err := tx.QueryRow(ctx, `SELECT board_id, name FROM board_columns WHERE id = $1`, columnID).Scan(&boardID, &name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := lockBoard(ctx, tx, boardID); err != nil {
		return nil, err
	}
	var target string
	if err := tx.QueryRow(ctx, `SELECT id FROM board_columns WHERE id = $1 AND board_id = $2`, toColumnID, boardID).Scan(&target); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("column not on this board")
		}
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM cards WHERE column_id = $1 ORDER BY rank, id`, columnID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !slices.Contains(gated, id) {
			return nil, ErrColumnCardsChanged
		}
	}
	var maxRank *string
	if err := tx.QueryRow(ctx, `SELECT max(rank) FROM cards WHERE column_id = $1`, target).Scan(&maxRank); err != nil {
		return nil, err
	}
	prev := ""
	if maxRank != nil {
		prev = *maxRank
	}
	from := columnID
	for _, id := range ids {
		rank, err := RankBetween(prev, "")
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE cards SET column_id = $2, rank = $3, version = version + 1, updated_at = now() WHERE id = $1`,
			id, target, rank); err != nil {
			return nil, err
		}
		cardEv := ev
		cardEv.ReviewID = reviews[id]
		if err := appendEvent(ctx, tx, id, "moved", cardEv, &from, &target, map[string]any{"reason": "column removed"}); err != nil {
			return nil, err
		}
		if err := enqueueStandingAgents(ctx, tx, target, id, ev.ActorTokenID); err != nil {
			return nil, err
		}
		prev = rank
	}
	tag, err := tx.Exec(ctx, `DELETE FROM board_columns WHERE id = $1`, columnID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	if err := recordColumnAudit(ctx, tx, boardID, columnID, "deleted", audit,
		map[string]any{"name": name, "moved_cards_to": target, "moved_cards": len(ids)}); err != nil {
		return nil, err
	}
	return ids, tx.Commit(ctx)
}
