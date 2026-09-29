package db

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EventMeta identifies the principal behind a write. Exactly one of
// ActorTokenID / ActorSessionID is set for agent-or-service vs human writes;
// ReviewID links curator-mediated writes back to their admission review.
type EventMeta struct {
	Actor          string // human|agent|service|curator
	ActorTokenID   *string
	ActorSessionID *string
	ReviewID       *string
}

// CardEvent is one row of a card's trail. Data carries whatever the event
// type needs: comment → {"text"}, dependency_* → the other card, refreshed →
// the key that matched, updated → {"fields": [...]}, the card fields that
// write actually changed (see changedFields; an empty list is a no-op write,
// and no key at all means the event predates the list).
type CardEvent struct {
	ID           int64           `json:"id"`
	CardID       string          `json:"card_id"`
	Type         string          `json:"type"`
	Actor        string          `json:"actor"`
	ActorTokenID *string         `json:"actor_token_id"`
	ReviewID     *string         `json:"review_id"`
	FromColumnID *string         `json:"from_column_id"`
	ToColumnID   *string         `json:"to_column_id"`
	Data         json.RawMessage `json:"data"`
	CreatedAt    string          `json:"created_at"`
}

func appendEvent(ctx context.Context, tx pgx.Tx, cardID, typ string, ev EventMeta, fromCol, toCol *string, data map[string]any) error {
	raw := json.RawMessage(`{}`)
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		raw = b
	}
	actor := ev.Actor
	if actor == "" {
		actor = "human"
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO card_events (card_id, type, actor, actor_token_id, actor_session_id,
			review_id, from_column_id, to_column_id, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		cardID, typ, actor, ev.ActorTokenID, ev.ActorSessionID, ev.ReviewID, fromCol, toCol, raw)
	return err
}

func ListCardEvents(ctx context.Context, pool *pgxpool.Pool, cardID string, afterID int64, limit int) ([]CardEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := pool.Query(ctx, `
		SELECT id, card_id, type, actor, actor_token_id, review_id,
			from_column_id, to_column_id, data,
			to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM card_events WHERE card_id = $1 AND id > $2 ORDER BY id LIMIT $3`,
		cardID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CardEvent
	for rows.Next() {
		var e CardEvent
		if err := rows.Scan(&e.ID, &e.CardID, &e.Type, &e.Actor, &e.ActorTokenID,
			&e.ReviewID, &e.FromColumnID, &e.ToColumnID, &e.Data, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AppendComment adds a comment event — the engineer's running log on a card.
// Comments bump the card's version like every mutation.
func AppendComment(ctx context.Context, pool *pgxpool.Pool, cardID, text string, ev EventMeta) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned
	if err := AppendCommentTx(ctx, tx, cardID, text, ev); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AppendCommentTx is AppendComment scoped to a caller-managed transaction —
// for callers that must hold a row lock across the comment and other reads
// or writes on the same card (see the staleness sweep, which re-checks
// eligibility under FOR UPDATE before nudging so a concurrent write can't
// land invisibly between the check and the comment).
func AppendCommentTx(ctx context.Context, tx pgx.Tx, cardID, text string, ev EventMeta) error {
	if _, err := tx.Exec(ctx,
		`UPDATE cards SET version = version + 1, updated_at = now() WHERE id = $1`, cardID); err != nil {
		return err
	}
	return appendEvent(ctx, tx, cardID, "comment", ev, nil, nil,
		map[string]any{"text": text})
}
