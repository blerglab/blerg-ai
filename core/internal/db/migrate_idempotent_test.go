package db

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Core applies a migration and records it in two steps that are not atomic, so a crash in between
// re-runs the file on the next start. Every migration must therefore be safe to run twice; this
// runs each file again over a fully migrated schema (the state a crash between apply and record
// leaves behind) and fails on any error. It pins 015 onwards, which are the ones written
// idempotently; the older ones predate the rule.
func TestMigrationsCanBeReapplied(t *testing.T) {
	ctx := context.Background()
	const schema = "test_blerg_core_db_reapply"
	setup, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	setup.Close()
	cfg, err := pgxpool.ParseConfig(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		pool.Close()
	})
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	reapplied := 0
	for _, e := range entries {
		if e.Name() < "015" {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := pool.Exec(ctx, string(body)); err != nil {
				t.Fatalf("re-applying %s (run %d): %v", e.Name(), i+1, err)
			}
		}
		if strings.HasPrefix(e.Name(), "015") || strings.HasPrefix(e.Name(), "016") || strings.HasPrefix(e.Name(), "017") {
			reapplied++
		}
	}
	if reapplied < 3 {
		t.Fatalf("re-applied only %d of the 015-017 migrations", reapplied)
	}
}
