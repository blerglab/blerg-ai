package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ObserveUsageNoticeWindow advances existing instances, including those belonging to idle
// sessions. Keep the original reset anchor through jitter; otherwise small shifts could
// accumulate indefinitely. Unknown resets re-arm through a below-floor reading as well.
func ObserveUsageNoticeWindow(ctx context.Context, pool *pgxpool.Pool, daemon, engine, window string, reset time.Time, belowFloor bool, tolerance time.Duration) error {
	_, err := pool.Exec(ctx, `UPDATE session_usage_notices SET
  instance_id=CASE WHEN threshold>0 AND ($5 OR $4>resets_at+($6 * interval '1 second')) THEN gen_random_uuid() ELSE instance_id END,
  threshold=CASE WHEN $5 OR $4>resets_at+($6 * interval '1 second') THEN 0 ELSE threshold END,
  resets_at=CASE WHEN $5 OR $4>resets_at+($6 * interval '1 second') THEN $4 ELSE resets_at END,
  -- Only a re-arm counts as use: a routine report must not keep ended sessions' rows alive past retention.
  updated_at=CASE WHEN $5 OR $4>resets_at+($6 * interval '1 second') THEN now() ELSE updated_at END
  WHERE daemon_id=$1 AND engine=$2 AND window_id=$3`, daemon, engine, window, reset, belowFloor, tolerance.Seconds())
	return err
}

func UsageNoticeState(ctx context.Context, pool *pgxpool.Pool, session, daemon, engine, window string, reset time.Time) (threshold int, instance string, err error) {
	err = pool.QueryRow(ctx, `INSERT INTO session_usage_notices(session_id,daemon_id,engine,window_id,resets_at)
  VALUES($1,$2,$3,$4,$5) ON CONFLICT(session_id,daemon_id,engine,window_id)
  DO UPDATE SET updated_at=now() RETURNING threshold,instance_id::text`, session, daemon, engine, window, reset).Scan(&threshold, &instance)
	return
}

func SetUsageNoticeThreshold(ctx context.Context, pool *pgxpool.Pool, session, daemon, engine, window, instance string, threshold int) error {
	_, err := pool.Exec(ctx, `UPDATE session_usage_notices SET threshold=GREATEST(threshold,$6),updated_at=now()
  WHERE session_id=$1 AND daemon_id=$2 AND engine=$3 AND window_id=$4 AND instance_id=$5`, session, daemon, engine, window, instance, threshold)
	return err
}

// PruneUsageNotices removes old unused or ended-session state. Preserve warned live
// sessions: retention alone must never re-arm a window with an unknown reset.
func PruneUsageNotices(ctx context.Context, pool *pgxpool.Pool, daemon string) error {
	_, err := pool.Exec(ctx, `DELETE FROM session_usage_notices n WHERE daemon_id=$1 AND updated_at<now()-interval '35 days'
 AND (threshold=0 OR EXISTS(SELECT 1 FROM sessions s WHERE s.id=n.session_id
  AND (s.ended_at IS NOT NULL OR s.delegation_ended_at IS NOT NULL OR s.status IN ('ended','stopped'))))`, daemon)
	return err
}

// UsageNoticeCandidates returns only working top-level agent IDs. Turn-start checks use
// GetSession directly instead of listing a desktop's history. Reloading these candidates
// before delivery still protects against state changes while the report is processed.
func UsageNoticeCandidates(ctx context.Context, pool *pgxpool.Pool, daemon string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT s.id FROM sessions s WHERE s.daemon_id=$1
  AND s.kind='agent' AND s.parent_session_id IS NULL AND COALESCE(s.delegation_via,'')=''
  AND COALESCE(s.started_by_kind,'')<>'session'
  AND s.ended_at IS NULL AND s.delegation_ended_at IS NULL AND s.archived_at IS NULL AND s.discard_requested_at IS NULL
  AND s.status NOT IN ('ended','stopped')
  AND (s.status='running' OR EXISTS(SELECT 1 FROM sessions c WHERE c.parent_session_id=s.id AND c.status='running' AND c.ended_at IS NULL))`, daemon)
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
