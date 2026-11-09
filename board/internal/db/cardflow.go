package db

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// CardFlowEntry preserves each entry, including reopened work. Unknown historical
// semantics stay unknown rather than borrowing a column's current name.
type CardFlowEntry struct {
	At         time.Time `json:"at"`
	ColumnID   string    `json:"column_id"`
	ColumnName *string   `json:"column_name"`
	InProgress *bool     `json:"in_progress"`
	Done       *bool     `json:"done"`
}

func CardFlow(ctx context.Context, pool *pgxpool.Pool, cardID string) ([]CardFlowEntry, error) {
	rows, err := pool.Query(ctx, `SELECT created_at,to_column_id::text,data->'flow'->>'column_name',
 (data->'flow'->>'in_progress')::boolean,(data->'flow'->>'done')::boolean
 FROM card_events WHERE card_id=$1 AND type IN ('created','moved')
 AND to_column_id IS NOT NULL AND to_column_id IS DISTINCT FROM from_column_id ORDER BY id`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CardFlowEntry{}
	for rows.Next() {
		var e CardFlowEntry
		if err := rows.Scan(&e.At, &e.ColumnID, &e.ColumnName, &e.InProgress, &e.Done); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
