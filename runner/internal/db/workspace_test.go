package db_test

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"testing"
)

func TestWorkspaceMigrationScalarAndFencing(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	const daemon = "00000000-0000-0000-0000-000000000001"
	const id = "00000000-0000-0000-0000-000000000002"
	if err := db.UpsertDaemon(ctx, pool, daemon, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, id, daemon, "running", "/repos/one", "org/one", "", ""); err != nil {
		t.Fatal(err)
	}
	w, err := db.GetSessionWorkspace(ctx, pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != "repo" || w.Manifest != nil {
		t.Fatal("legacy session changed")
	}
	row, err := db.GetSession(ctx, pool, id)
	if err != nil || row.WorkspaceKind != "repo" || row.Repo != "org/one" {
		t.Fatal("scalar read changed", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id='owner',runtime='daemon' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}, Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "one", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "example-org/one"}}}
	if err := db.InsertSessionWorkspace(ctx, pool, id, m); err != nil {
		t.Fatal(err)
	}
	if db.InsertSessionWorkspace(ctx, pool, id, m) == nil {
		t.Fatal("manifest replaced")
	}
	if db.SetWorkspaceResults(ctx, pool, id, "old", nil) == nil {
		t.Fatal("stale generation accepted")
	}
	if err := db.SetWorkspaceResults(ctx, pool, id, "launch", nil); err != nil {
		t.Fatal(err)
	}
	results := []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: "launch"}, Head: "head"}}
	if err := db.SetWorkspaceResults(ctx, pool, id, "launch", results); err != nil {
		t.Fatal(err)
	}
	w, err = db.GetSessionWorkspace(ctx, pool, id)
	if err != nil || w.Kind != "folder" || len(w.Results) != 1 {
		t.Fatal("workspace read", err)
	}
	var repo string
	if err := pool.QueryRow(ctx, `UPDATE sessions SET title='old writer' WHERE id=$1 RETURNING repo`, id).Scan(&repo); err != nil || repo != "" {
		t.Fatal("old query incompatible", err)
	}
}

// Simulate a pre-076 installation containing sessions, then apply the additive
// migration. Downgrade compatibility means old SQL continues working, not loss
// of persisted workspace state through a destructive down migration.
func TestWorkspaceUpgradeExistingSession(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DROP TABLE session_workspaces; ALTER TABLE sessions DROP COLUMN workspace_kind`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO daemons(id,name,mode,repos_root,status) VALUES('00000000-0000-0000-0000-000000000001','old','local','/repos','connected'); INSERT INTO sessions(id,daemon_id,status,project_path,repo) VALUES('00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000001','running','/repos/one','org/one')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/076_workspace.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var kind, repo string
	if err = tx.QueryRow(ctx, `SELECT workspace_kind,repo FROM sessions WHERE id='00000000-0000-0000-0000-000000000002'`).Scan(&kind, &repo); err != nil || kind != "repo" || repo != "org/one" {
		t.Fatal("upgrade changed existing session", err)
	}
}
