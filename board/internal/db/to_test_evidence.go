package db

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ToTestAddedAt returns the latest proven addition, not a later comment's
// timestamp. Legacy events with no before-state cannot prove an addition.
func ToTestAddedAt(ctx context.Context, pool *pgxpool.Pool, card string) (*string, error) {
	var at string
	err := pool.QueryRow(ctx, `WITH states AS (
 SELECT id,type,created_at,(data->'burn_after'->>'to_test')::boolean tagged,
 lag((data->'burn_after'->>'to_test')::boolean) OVER(ORDER BY id) previous
 FROM card_events WHERE card_id=$1
 ) SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') FROM states
 WHERE tagged AND (previous=false OR (previous IS NULL AND type='created')) ORDER BY id DESC LIMIT 1`, card).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &at, nil
}

// AddReconciliationEvidence projects all cards on a search page in one query.
// It never exposes comment text, actor IDs or another board's audit history.
func AddReconciliationEvidence(ctx context.Context, pool *pgxpool.Pool, cards []Card) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]string, len(cards))
	positions := map[string]int{}
	for i, c := range cards {
		ids[i] = c.ID
		positions[c.ID] = i
	}
	rows, err := pool.Query(ctx, `WITH states AS (
 SELECT card_id,id,type,created_at,(data->'burn_after'->>'to_test')::boolean tagged,
 lag((data->'burn_after'->>'to_test')::boolean) OVER(PARTITION BY card_id ORDER BY id) previous
 FROM card_events WHERE card_id=ANY($1::uuid[])
 ) SELECT c.id,
 EXISTS(SELECT 1 FROM card_events e WHERE e.card_id=c.id AND e.type='comment' AND e.actor='human' AND btrim(e.data->>'text') ~ '^Done reason:[[:space:]]*[^[:space:]]'),
 CASE WHEN EXISTS(SELECT 1 FROM card_tags t WHERE t.card_id=c.id AND t.tag='to-test') THEN
 (SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') FROM states s WHERE s.card_id=c.id AND tagged AND (previous=false OR (previous IS NULL AND type='created')) ORDER BY id DESC LIMIT 1) END,
 (SELECT CASE WHEN e.to_column_id=c.column_id THEN to_char(e.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END FROM card_events e WHERE e.card_id=c.id AND e.type='moved' ORDER BY e.id DESC LIMIT 1)
 FROM cards c WHERE c.id=ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var reason bool
		var added, moved *string
		if err := rows.Scan(&id, &reason, &added, &moved); err != nil {
			return err
		}
		if i, ok := positions[id]; ok {
			cards[i].HasHumanDoneReason = &reason
			cards[i].ToTestAddedAt = added
			cards[i].DoneMovedAt = moved
		}
	}
	return rows.Err()
}
