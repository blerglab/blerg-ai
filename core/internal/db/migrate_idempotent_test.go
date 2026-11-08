package db

import (
	"context"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/testsupport"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Core applies a migration and records it in two steps that are not atomic, so a crash in between
// re-runs the file on the next start. Every migration must therefore be safe to run twice; this
// runs each file again over a fully migrated schema (the state a crash between apply and record
// leaves behind) and fails on any error. It pins 015 onwards, which are the ones written
// idempotently; the older ones predate the rule.
func TestMigrationsCanBeReapplied(t *testing.T) {
	ctx := context.Background()
	// Preserve migration coverage outside public, independently of fixture cloning.
	cfg := testsupport.Empty(t)
	setup, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, "CREATE SCHEMA migration_fixture"); err != nil {
		setup.Close()
		t.Fatal(err)
	}
	setup.Close()
	cfg.ConnConfig.RuntimeParams["search_path"] = "migration_fixture"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
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

// Reapplying the upgrade must not revoke approvals created after the upgrade.
func TestLiveDataMigrationPreservesApprovedGrant(t *testing.T) {
	pool := testsupport.Pool(t, Migrate)
	ctx := t.Context()
	_, err := pool.Exec(ctx, `
 INSERT INTO accounts(id,provider,provider_subject,email,role) VALUES('00000000-0000-4000-8000-000000000001','local','migration-live','migration@example.com','member');
 INSERT INTO mcp_connections(id,account_id,name,url,auth_kind) VALUES('00000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000001','market','https://market.example/mcp','none');
 INSERT INTO live_data_grants(binding_id,revision,owner_id,connection_id,scope,arguments,approved_sid,approved_at,expires_at,approved_configuration,runner_committed)
 VALUES('00000000-0000-4000-8000-000000000003',1,'00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000002','{}','{}','fixture',clock_timestamp(),clock_timestamp()+interval '1 day',decode('01','hex'),true);`)
	if err != nil {
		t.Fatal(err)
	}
	body, err := migrationsFS.ReadFile("migrations/155_live_data_approved_configuration.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	var active bool
	if err := pool.QueryRow(ctx, `SELECT revoked_at IS NULL AND runner_committed AND approved_configuration=decode('01','hex') FROM live_data_grants WHERE binding_id='00000000-0000-4000-8000-000000000003'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("migration invalidated a complete approved grant")
	}
}
