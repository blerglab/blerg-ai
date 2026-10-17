package identity_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool provides a fresh database cloned from the migrated package template.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testsupport.Pool(t, db.Migrate)
}

func TestAccountsMigrationCreatesExpectedTables(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, table := range []string{"accounts", "human_sessions", "credential_access_log"} {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table,
		).Scan(&exists)
		if err != nil || !exists {
			t.Errorf("table %s missing after migrate: err=%v exists=%v", table, err, exists)
		}
	}
}
