package db

import (
	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
	"testing"
)

func TestReadinessRequiresEveryMigrationAndDatabase(t *testing.T) {
	ctx := t.Context()
	pool := testsupport.Open(t, "TEST_DATABASE_URL", "readiness", RunMigrations)
	if err := Ready(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Replace one required entry with a future migration: a count-only check would pass.
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET filename = 'future.sql' WHERE filename = (SELECT filename FROM schema_migrations ORDER BY filename LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if err := Ready(ctx, pool); err == nil {
		t.Fatal("ready with missing required migration")
	}
	if _, err := pool.Exec(ctx, `DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := Ready(ctx, pool); err == nil {
		t.Fatal("ready without migration table")
	}
	pool.Close()
	if err := Ready(ctx, pool); err == nil {
		t.Fatal("ready with unavailable database")
	}
	if err := Ready(ctx, nil); err == nil {
		t.Fatal("ready without database")
	}
}
func TestReadinessAllowsNewerReplicaMigrations(t *testing.T) {
	pool := testsupport.Open(t, "TEST_DATABASE_URL", "readiness", RunMigrations)
	if _, err := pool.Exec(t.Context(), `INSERT INTO schema_migrations (filename) VALUES ('future.sql')`); err != nil {
		t.Fatal(err)
	}
	if err := Ready(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
}
