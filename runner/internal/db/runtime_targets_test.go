package db_test

import (
	"os"
	"testing"
)

func TestRuntimeTargetsDefaultAndLegacyWrites(t *testing.T) {
	p := connect(t)
	ctx := t.Context()
	var id, kind, name string
	if err := p.QueryRow(ctx, `SELECT id,kind,name FROM runtime_targets WHERE is_default`).Scan(&id, &kind, &name); err != nil {
		t.Fatal(err)
	}
	if id != "cluster" || kind != "cluster" || name != "Cluster" {
		t.Fatalf("target: %q %q %q", id, kind, name)
	}
	var target *string
	// Old writers omit the target. Posture updates must fill it atomically too.
	_, err := p.Exec(ctx, `INSERT INTO daemons(id,name,mode,repos_root) VALUES('00000000-0000-4000-8000-000000000001','fixture','cluster','');
 INSERT INTO sessions(id,daemon_id,status,project_path,repo) VALUES('00000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000001','running','','');
 UPDATE sessions SET runtime='cluster' WHERE id='00000000-0000-4000-8000-000000000002'`)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `SELECT runtime_target_id FROM sessions LIMIT 1`).Scan(&target); err != nil || target == nil || *target != id {
		t.Fatalf("legacy target %v: %v", target, err)
	}
	if _, err := p.Exec(ctx, `UPDATE sessions SET runtime_target_id=''`); err != nil {
		t.Fatalf("empty legacy target must select default: %v", err)
	}
	if err := p.QueryRow(ctx, `SELECT runtime_target_id FROM sessions LIMIT 1`).Scan(&target); err != nil || target == nil || *target != id {
		t.Fatalf("empty target did not select default: %v %v", target, err)
	}
	if _, err := p.Exec(ctx, `UPDATE sessions SET runtime_target_id='missing'`); err == nil {
		t.Fatal("unknown target accepted")
	}
	if _, err := p.Exec(ctx, `UPDATE runtime_targets SET is_default=false WHERE is_default`); err == nil {
		t.Fatal("default target unset")
	}
	if _, err := p.Exec(ctx, `DELETE FROM runtime_targets WHERE is_default`); err == nil {
		t.Fatal("default target deleted")
	}
}

func TestRuntimeTargetsUpgradeBackfill(t *testing.T) {
	p := connect(t)
	ctx := t.Context()
	// Recreate the schema immediately before migration 122, retaining old rows.
	_, err := p.Exec(ctx, `DROP TABLE runtime_targets CASCADE;
 ALTER TABLE sessions DROP COLUMN runtime_target_id;
 ALTER TABLE crons DROP COLUMN runtime_target_id;

 ALTER TABLE cluster_workspace_storage DROP COLUMN runtime_target_id;
 ALTER TABLE shell_names DROP COLUMN runtime_target_id;
 ALTER TABLE session_storage_audit DROP COLUMN runtime_target_id;
 ALTER TABLE session_file_deletions DROP COLUMN runtime_target_id;
 DROP FUNCTION fill_cluster_runtime_target() CASCADE;
 DROP FUNCTION fill_shell_runtime_target() CASCADE;
 DROP FUNCTION retain_default_runtime_target() CASCADE;
 INSERT INTO daemons(id,name,mode,repos_root) VALUES('00000000-0000-4000-8000-000000000001','fixture','cluster','');
 INSERT INTO sessions(id,daemon_id,status,project_path,repo,runtime) VALUES
 ('00000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000001','running','','','cluster');
 INSERT INTO cluster_workspace_storage(session_id,launch_generation,owner_id,claim_name,claim_uid,contexts)
 VALUES('00000000-0000-4000-8000-000000000002','launch','owner','claim','uid','[]');
 INSERT INTO shell_names(shell_id,session_id,owner_id,login_id,name)
 VALUES('shell','00000000-0000-4000-8000-000000000002','owner','login','Shell');
 INSERT INTO crons(owner_account_id,name,schedule,timezone,prompt,runtime,token_id,token_expires_at,next_run_at)
 VALUES('owner','Schedule','0 * * * *','UTC','Work','cluster','fixture',now()+interval '1 day',now());
 INSERT INTO boards(name) VALUES('Board');
 INSERT INTO session_storage_audit(session_id,actor,previous_bytes,new_bytes)
 VALUES('00000000-0000-4000-8000-000000000002','owner',1,2);
 INSERT INTO session_file_deletions(session_id,artifact_id,name,origin,version,size,actor,actor_admin,via)
 VALUES('00000000-0000-4000-8000-000000000002','file','file.txt','agent',1,1,'owner',false,'browser')`)

	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/122_runtime_targets.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "crons", "cluster_workspace_storage", "shell_names", "session_storage_audit", "session_file_deletions"} {
		var id string
		if err := p.QueryRow(ctx, "SELECT runtime_target_id FROM "+table+" LIMIT 1").Scan(&id); err != nil || id != "cluster" {
			t.Fatalf("%s backfill: %q %v", table, id, err)
		}
	}
}

func TestRuntimeTargetsDoNotPinAutoOrRunnerBoards(t *testing.T) {
	p := connect(t)
	ctx := t.Context()
	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name='boards' AND column_name='default_runtime_target_id'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("dead boards column: %d %v", n, err)
	}
	var target *string
	if err := p.QueryRow(ctx, `INSERT INTO crons(owner_account_id,name,schedule,timezone,prompt,runtime,token_id,token_expires_at,next_run_at) VALUES('owner','Schedule','0 * * * *','UTC','Work','auto','fixture',now()+interval '1 day',now()) RETURNING runtime_target_id`).Scan(&target); err != nil || target != nil {
		t.Fatalf("auto target: %v %v", target, err)
	}
}
