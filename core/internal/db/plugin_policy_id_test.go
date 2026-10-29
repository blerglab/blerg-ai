package db

import (
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/testsupport"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMarketplacePolicyIDMigrationPreservesExistingPolicy(t *testing.T) {
	ctx := t.Context()
	pool, err := pgxpool.NewWithConfig(ctx, testsupport.Empty(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the pre-ID row as it appears in an older backup.
	if _, err := pool.Exec(ctx, `ALTER TABLE plugin_marketplace_policy DROP COLUMN policy_id;
 INSERT INTO plugin_marketplace_policy(singleton,revision,marketplaces) VALUES(true,7,'[]')`); err != nil {
		t.Fatal(err)
	}
	body, err := migrationsFS.ReadFile("migrations/029_plugin_marketplace_policy_id.sql")
	if err != nil {
		t.Fatal(err)
	}
	var originalID string
	for run := range 2 {
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
		var id string
		var revision int64
		if err := pool.QueryRow(ctx, `SELECT policy_id::text,revision FROM plugin_marketplace_policy WHERE singleton`).Scan(&id, &revision); err != nil {
			t.Fatal(err)
		}
		if len(id) != 36 || revision != 7 || (run > 0 && id != originalID) {
			t.Fatal("migration replaced policy or identity", id, revision)
		}
		originalID = id
	}
}
