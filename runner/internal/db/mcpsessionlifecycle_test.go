package db_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"strings"
	"testing"
)

func TestScopedBindingLedgerSurvivesGrantRemoval(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{}, 1)
	g.AccountID = "00000000-0000-4000-8000-000000000145"
	g.ProofValue = "00000000-0000-4000-8000-000000000146"
	g.SessionScope = &db.MCPSessionScope{AccountID: g.AccountID, RunnerInstallationID: "synthetic-runner", RunnerSessionID: g.SessionID, HumanSessionID: g.ProofValue, LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: hex.EncodeToString(g.TokenHash)}
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mcp_session_bindings WHERE session_id=$1 AND connection_id=$2 AND account_id=$3`, g.SessionID, g.ConnectionID, g.AccountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("scoped grant lacked atomic durable binding record")
	}
	if _, err := db.DeleteMCPGrantsForSession(ctx, pool, g.SessionID); err != nil {
		t.Fatal(err)
	}
	var action string
	if err := pool.QueryRow(ctx, `SELECT desired_action FROM mcp_session_bindings WHERE session_id=$1`, g.SessionID).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "suspend" {
		t.Fatal("grant removal lost suspension delivery", action)
	}
}

func TestBindingLifecycleDeliveryUsesExactRevision(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	scope := db.MCPSessionScope{AccountID: "00000000-0000-4000-8000-000000000145", RunnerInstallationID: "synthetic-runner", RunnerSessionID: pinSess1, HumanSessionID: "00000000-0000-4000-8000-000000000146", LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: strings.Repeat("a", 64)}
	encoded, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO mcp_session_bindings(session_id,connection_id,account_id,scope,control_generation,desired_action) VALUES($1,$2,$3,$4,2,'suspend')`, scope.RunnerSessionID, pinConn1, scope.AccountID, encoded); err != nil {
		t.Fatal(err)
	}
	work, err := db.ListMCPBindingWork(ctx, pool)
	if err != nil || len(work) != 1 {
		t.Fatal("suspension delivery missing", len(work), err)
	}
	old := work[0]
	if _, err = pool.Exec(ctx, `UPDATE mcp_session_bindings SET revision=revision+1 WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteMCPBindingWork(ctx, pool, old, false, old.ControlGeneration); err != nil {
		t.Fatal(err)
	}
	var delivered bool
	if err = pool.QueryRow(ctx, `SELECT delivered FROM mcp_session_bindings WHERE id=$1`, old.ID).Scan(&delivered); err != nil || delivered {
		t.Fatal("stale revision acknowledged a newer same-action intent", err)
	}
	work, err = db.ListMCPBindingWork(ctx, pool)
	if err != nil || len(work) != 1 {
		t.Fatal("newer intent disappeared", err)
	}
	old = work[0]
	if _, err = pool.Exec(ctx, `UPDATE mcp_session_bindings SET control_generation=control_generation+1 WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteMCPBindingWork(ctx, pool, old, false, old.ControlGeneration); err != nil {
		t.Fatal(err)
	}
	var fence int64
	if err = pool.QueryRow(ctx, `SELECT control_generation FROM mcp_session_bindings WHERE id=$1`, old.ID).Scan(&fence); err != nil || fence != old.ControlGeneration+1 {
		t.Fatal("late ack rolled back an acknowledged generation", fence, err)
	}
	work, err = db.ListMCPBindingWork(ctx, pool)
	if err != nil || len(work) != 1 {
		t.Fatal("newer generation disappeared", err)
	}
	old = work[0]
	if old.Action != "suspend" || old.Scope != scope {
		t.Fatal("delivery lost exact scope", old)
	}
	if _, err = pool.Exec(ctx, `UPDATE mcp_session_bindings SET desired_action='close',revision=revision+1 WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteMCPBindingWork(ctx, pool, old, false, old.ControlGeneration); err != nil {
		t.Fatal(err)
	}
	work, err = db.ListMCPBindingWork(ctx, pool)
	if err != nil || len(work) != 1 || work[0].Action != "close" {
		t.Fatal("stale ack swallowed terminal close", work, err)
	}
	if err = db.CompleteMCPBindingWork(ctx, pool, work[0], true, work[0].ControlGeneration); err != nil {
		t.Fatal(err)
	}
	work, err = db.ListMCPBindingWork(ctx, pool)
	if err != nil || len(work) != 0 {
		t.Fatal("superseded delivery retried", work, err)
	}
}

func TestRuntimeLifecycleDetachesBeforeResumeAndClosesOnlyTerminal(t *testing.T) {
	for _, mode := range []string{"resume", "completion", "deletion", "disconnect", "daemon loss", "completion then resume", "daemon loss then resume", "superseded resume"} {
		t.Run(mode, func(t *testing.T) {
			pool := proposalTestPool(t)
			ctx := context.Background()
			var daemon string
			if err := pool.QueryRow(ctx, `INSERT INTO daemons(name,mode,repos_root) VALUES('synthetic','host','/fixture') RETURNING id::text`).Scan(&daemon); err != nil {
				t.Fatal(err)
			}
			g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{}, 1)
			g.AccountID = "00000000-0000-4000-8000-000000000145"
			g.ProofValue = "00000000-0000-4000-8000-000000000146"
			g.SessionScope = &db.MCPSessionScope{AccountID: g.AccountID, RunnerInstallationID: "synthetic-runner", RunnerSessionID: g.SessionID, HumanSessionID: g.ProofValue, LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: hex.EncodeToString(g.TokenHash)}
			if _, err := pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,spawning_account_id,mcp_launch_generation) VALUES($1,$2,'running','/fixture','synthetic',$3,1)`, g.SessionID, daemon, g.AccountID); err != nil {
				t.Fatal(err)
			}
			if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
				t.Fatal(err)
			}
			if mode == "superseded resume" {
				if _, err := pool.Exec(ctx, `UPDATE mcp_session_bindings SET desired_action='superseded' WHERE session_id=$1`, g.SessionID); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch mode {
			case "resume", "superseded resume":
				_, err = pool.Exec(ctx, `UPDATE sessions SET status='starting' WHERE id=$1`, g.SessionID)
			case "completion", "completion then resume":
				_, err = pool.Exec(ctx, `UPDATE sessions SET status='completed',ended_at=now(),end_reason='process_exited' WHERE id=$1`, g.SessionID)
			case "deletion":
				_, err = pool.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, g.SessionID)
			case "disconnect":
				_, err = pool.Exec(ctx, `UPDATE sessions SET status='disconnected' WHERE id=$1`, g.SessionID)
			case "daemon loss", "daemon loss then resume":
				_, err = pool.Exec(ctx, `UPDATE sessions SET status='error',ended_at=now(),end_reason='daemon_lost' WHERE id=$1`, g.SessionID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(mode, "then resume") {
				if _, err = pool.Exec(ctx, `UPDATE sessions SET status='starting',ended_at=NULL,end_reason=NULL WHERE id=$1`, g.SessionID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.ListMCPBindingWork(ctx, pool); err != nil {
				t.Fatal(err)
			}
			var action string
			if err = pool.QueryRow(ctx, `SELECT desired_action FROM mcp_session_bindings WHERE session_id=$1`, g.SessionID).Scan(&action); err != nil {
				t.Fatal(err)
			}
			want := "close"
			if mode == "resume" || mode == "disconnect" || strings.HasPrefix(mode, "daemon loss") {
				want = "suspend"
			}
			if mode == "superseded resume" {
				want = "superseded"
			}
			if action != want {
				t.Fatal("runtime lifecycle lost durable intent", action, want)
			}
			grants, err := db.ListMCPGrantsForSession(ctx, pool, g.SessionID)
			if err != nil || len(grants) != 0 {
				t.Fatal("runtime transition retained scoped gateway token", len(grants), err)
			}
			if mode == "resume" || mode == "superseded resume" || strings.HasSuffix(mode, "then resume") {
				var gen int64
				if err = pool.QueryRow(ctx, `SELECT mcp_launch_generation FROM sessions WHERE id=$1`, g.SessionID).Scan(&gen); err != nil || gen != 2 {
					t.Fatal("resume did not advance launch fence", gen, err)
				}
			}
		})
	}
}
