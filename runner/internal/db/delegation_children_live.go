package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ChildLiveRow is metadata only. Visibility is checked independently for each
// child: a public parent does not make a subsequently private child public.
type ChildLiveRow struct {
	ChildSession
	ArchivedAt *time.Time
	Owner      string
	MaxSeq     int64
}

func ListVisibleChildLiveRows(ctx context.Context, pool *pgxpool.Pool, parent, account string) ([]ChildLiveRow, error) {
	rows, err := pool.Query(ctx, `SELECT s.id::text,COALESCE(s.title,''),COALESCE(s.engine,''),COALESCE(s.model,''),
		COALESCE(s.effort,''),COALESCE(s.runtime,''),s.status,COALESCE(s.delegation_via,''),s.started_at,s.ended_at,s.keep_running,
		(SELECT count(*) FROM messages m WHERE m.session_id=s.id AND m.kind='ask' AND m.status='open'),
		COALESCE(s.spawning_account_id,''),COALESCE((SELECT max(e.seq) FROM agent_events e WHERE e.session_id=s.id),0), s.archived_at
		FROM sessions s WHERE s.parent_session_id=$1 AND (NOT s.private OR s.spawning_account_id=$2)
		ORDER BY s.started_at,s.id`, parent, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChildLiveRow{}
	for rows.Next() {
		var c ChildLiveRow
		if err := rows.Scan(&c.ID, &c.Title, &c.Engine, &c.Model, &c.Effort, &c.Runtime, &c.Status, &c.DelegationVia,
			&c.StartedAt, &c.EndedAt, &c.KeepRunning, &c.OpenAsks, &c.Owner, &c.MaxSeq, &c.ArchivedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
