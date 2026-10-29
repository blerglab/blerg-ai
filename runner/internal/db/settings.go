package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Setting keys in runner_settings.
const (
	SettingPodIdleTimeoutSeconds = "pod_idle_timeout_seconds"
	SettingPodTTLSeconds         = "pod_ttl_seconds"
	SettingMaxSessions           = "max_sessions"
	// Owner-shell inactivity limits per runtime class, in seconds; 0 means until the session ends.
	SettingShellDesktopDetachedSeconds = "shell_desktop_detached_seconds"
	SettingShellDesktopIdleSeconds     = "shell_desktop_idle_seconds"
	SettingShellClusterDetachedSeconds = "shell_cluster_detached_seconds"
	SettingShellClusterIdleSeconds     = "shell_cluster_idle_seconds"
)

// GetSettings returns every stored override, keyed by setting name.
func GetSettings(ctx context.Context, pool *pgxpool.Pool) (map[string]int64, error) {
	rows, err := pool.Query(ctx, `SELECT key, value FROM runner_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// DeleteSetting removes an override, so the environment default applies again.
// Removing a setting that is not stored is not an error.
func DeleteSetting(ctx context.Context, pool *pgxpool.Pool, key string) error {
	_, err := pool.Exec(ctx, `DELETE FROM runner_settings WHERE key = $1`, key)
	return err
}

// SetSetting stores an override, recording who changed it.
func SetSetting(ctx context.Context, pool *pgxpool.Pool, key string, value int64, by string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO runner_settings (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by
	`, key, value, by)
	return err
}
