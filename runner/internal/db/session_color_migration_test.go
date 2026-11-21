package db_test

import (
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestSessionColorPaletteUpgradePreservesSavedKeys(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	daemon := mustDaemon(t, pool)
	keys := []string{"", "blue", "orange", "purple", "teal"}
	ids := make([]string, len(keys))
	// Reconstruct the old constraint and a database which has not applied 127.
	if _, err := pool.Exec(ctx, `ALTER TABLE sessions DROP CONSTRAINT sessions_color_check;
 ALTER TABLE sessions ADD CONSTRAINT sessions_color_check CHECK (color IN ('', 'blue', 'orange', 'purple', 'teal'));
 DELETE FROM schema_migrations WHERE filename='127_session_color_palette.sql'`); err != nil {
		t.Fatal(err)
	}
	for i, key := range keys {
		ids[i] = mustSession(t, pool, daemon, "palette-owner")
		if _, err := pool.Exec(ctx, `UPDATE sessions SET color=$1 WHERE id=$2`, key, ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for i, key := range keys {
		var got string
		if err := pool.QueryRow(ctx, `SELECT color FROM sessions WHERE id=$1`, ids[i]).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != key {
			t.Fatalf("upgrade changed %q to %q", key, got)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET color='rose' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET color='arbitrary-paint' WHERE id=$1`, ids[0]); err == nil {
		t.Fatal("constraint accepted an unknown color")
	}
}
