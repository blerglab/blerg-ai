package db

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// InsightCardMetadata contains only the fields used for aggregate Insights.
// Bodies, origins, private notes and audit history never cross this boundary.
func InsightCardMetadata(ctx context.Context, pool *pgxpool.Pool, board string, numbers []int) (json.RawMessage, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('board_id',board_id,'number',number,'title',left(title,160),'reference',(SELECT board_key FROM boards WHERE id=$1)||'-'||number,'size',size,'risk',risk,'complexity',complexity,'size_first_set_at',size_first_set_at,'updated_at',updated_at)),'[]'::jsonb) FROM cards WHERE board_id=$1 AND number=ANY($2::int[])`, board, numbers).Scan(&raw)
	return raw, err
}

// BoardTimeEvent is evidence of a deployment or released version. Deployment
// details and credentials never enter a chart response.
type BoardTimeEvent struct {
	At    time.Time `json:"at"`
	Label string    `json:"label"`
}

func ListBoardTimeEvents(ctx context.Context, pool *pgxpool.Pool, board string, from, to time.Time) ([]BoardTimeEvent, error) {
	rows, err := pool.Query(ctx, `SELECT at,label FROM (
 SELECT created_at AS at,CASE status WHEN 'ok' THEN 'Deployed '||left(env,60) WHEN 'failed' THEN 'Deploy failed · '||left(env,60) ELSE 'Deploy started · '||left(env,60) END AS label FROM deployments WHERE board_id=$1 AND created_at >= $2 AND created_at < $3
 UNION ALL SELECT released_at,'Release '||left(name,120) FROM board_releases WHERE board_id=$1 AND released_at >= $2 AND released_at < $3
 ) events ORDER BY at LIMIT 1000`, board, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []BoardTimeEvent{}
	for rows.Next() {
		var e BoardTimeEvent
		if err := rows.Scan(&e.At, &e.Label); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
