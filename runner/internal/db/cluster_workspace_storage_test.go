package db_test

import (
	"os"
	"testing"
)

func TestClusterWorkspaceStorageModeMigration(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	// Reconstruct a pre-095 row, then apply the additive migration.
	if _, err := pool.Exec(ctx, `ALTER TABLE cluster_workspace_storage DROP COLUMN IF EXISTS storage_mode;
 INSERT INTO cluster_workspace_storage(session_id,launch_generation,owner_id,claim_name,claim_uid,contexts)
 VALUES('00000000-0000-0000-0000-000000000001','launch','owner','claim','uid','[]')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/095_cluster_workspace_storage_mode.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err = pool.QueryRow(ctx, `SELECT storage_mode FROM cluster_workspace_storage`).Scan(&mode); err != nil || mode != "bounded" {
		t.Fatalf("existing row mode=%q: %v", mode, err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO cluster_workspace_storage(session_id,launch_generation,owner_id,claim_name,claim_uid,contexts)
 VALUES('00000000-0000-0000-0000-000000000002','launch','owner','claim','uid','[]') RETURNING storage_mode`).Scan(&mode); err != nil || mode != "bounded" {
		t.Fatalf("fresh row mode=%q: %v", mode, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE cluster_workspace_storage SET storage_mode='unknown'`); err == nil {
		t.Fatal("unknown storage mode accepted")
	}
}
