package identity_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testSchema is a fixed schema name reused (dropped+recreated) by every test
// in this package. Tests here don't run in parallel with each other, so one
// name is enough — matching the established pattern in
// runner/internal/db/queries_test.go's setupSchema.
const testSchema = "test_blerg_core_identity"

// testPool connects to DATABASE_URL and gives the caller a fresh, isolated
// schema: dropped and recreated before the test runs, and dropped again on
// cleanup. This is required because this package's tests share one live
// Postgres database with no per-row cleanup (e.g. TestAccountRoleResolvesFromDB
// leaves an admin-role row behind) — without schema isolation, tests that
// count rows (like authprovider's bootstrap-admin test, run against the same
// DATABASE_URL) become order-dependent on what ran before them.
//
// A plain "SET search_path" via pool.Exec is NOT enough: pgxpool can hand
// out any of several underlying connections, and SET only affects the one
// connection it ran on. Instead the schema is baked into every connection
// the returned pool ever opens, via ConnConfig.RuntimeParams — the same
// fix already established in runner/internal/db/ticket_graph_test.go's
// connectWithSchema.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()

	// Phase 1: a plain, default-search-path connection to drop/recreate the
	// schema itself (DROP/CREATE SCHEMA are schema-qualified statements, so
	// which connection runs them doesn't matter).
	setupPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", testSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", testSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	setupPool.Close()

	// Phase 2: the pool the test actually uses, with search_path baked into
	// every connection it opens.
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = testSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		pool.Exec(bg, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", testSchema))
		pool.Close()
	})

	return pool
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
