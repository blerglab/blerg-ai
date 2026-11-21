package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UsageWindowSample is one minute-rounded reading of a provider's weekly usage window, kept so
// Insights can draw a burndown of the current window and show the previous one faintly.
type UsageWindowSample struct {
	Provider    string    `json:"provider"`
	WindowID    string    `json:"window_id"`
	UsedPercent float64   `json:"used_percent"`
	WindowStart time.Time `json:"window_start"`
	ResetsAt    time.Time `json:"resets_at"`
	SampledAt   time.Time `json:"sampled_at"`
}

// RecordUsageWindowSample stores (or refreshes) a daemon's reading of one window, minute-rounded
// so routine 10-minute polling and turn-triggered reads do not grow the table without bound.
func RecordUsageWindowSample(ctx context.Context, pool *pgxpool.Pool, daemon, provider, windowID string, usedPercent float64, windowStart, resetsAt, sampledAt time.Time) error {
	_, err := pool.Exec(ctx, `INSERT INTO usage_window_samples(daemon_id,provider,window_id,used_percent,window_start,resets_at,sampled_at)
  VALUES($1,$2,$3,$4,$5,$6,$7)
  ON CONFLICT(daemon_id,provider,window_id,sampled_at)
  DO UPDATE SET used_percent=excluded.used_percent, window_start=excluded.window_start, resets_at=excluded.resets_at`,
		daemon, provider, windowID, usedPercent, windowStart.UTC(), resetsAt.UTC(), sampledAt.UTC())
	return err
}

// PruneUsageWindowSamples removes a daemon's samples older than maxAge (kept short: only the
// current and previous window are ever shown).
func PruneUsageWindowSamples(ctx context.Context, pool *pgxpool.Pool, daemon string, maxAge time.Duration) error {
	_, err := pool.Exec(ctx, `DELETE FROM usage_window_samples WHERE daemon_id=$1 AND sampled_at<$2`, daemon, time.Now().UTC().Add(-maxAge))
	return err
}

// ListOwnerUsageWindowSamples returns an account's samples (across every daemon it owns) for
// provider/windowID, newest first, since since. Scoped by desktop_owner_id: the same ownership
// Insights already uses for everything else it reports.
func ListOwnerUsageWindowSamples(ctx context.Context, pool *pgxpool.Pool, owner, provider, windowID string, since time.Time) ([]UsageWindowSample, error) {
	rows, err := pool.Query(ctx, `SELECT s.provider, s.window_id, s.used_percent, s.window_start, s.resets_at, s.sampled_at
  FROM usage_window_samples s JOIN daemons d ON d.id = s.daemon_id
  WHERE d.desktop_owner_id=$1 AND s.provider=$2 AND s.window_id=$3 AND s.sampled_at>=$4
  ORDER BY s.sampled_at ASC`, owner, provider, windowID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageWindowSample
	for rows.Next() {
		var s UsageWindowSample
		if err := rows.Scan(&s.Provider, &s.WindowID, &s.UsedPercent, &s.WindowStart, &s.ResetsAt, &s.SampledAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
