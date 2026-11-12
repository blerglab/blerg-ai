package db_test

import (
	"context"
	"encoding/hex"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"strings"
	"testing"
)

func TestSessionScopedGrantAndProposalKeepFrozenProof(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"send": {Mode: "propose", Hash: "hash-1"}}, 1)
	g.AccountID = "00000000-0000-4000-8000-000000000145"
	g.ProofValue = "00000000-0000-4000-8000-000000000146"
	g.SessionScope = &db.MCPSessionScope{AccountID: g.AccountID, RunnerInstallationID: "synthetic-runner", RunnerSessionID: g.SessionID, HumanSessionID: g.ProofValue, LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: hex.EncodeToString(g.TokenHash)}
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	stored, err := db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SessionScope == nil || *stored.SessionScope != *g.SessionScope {
		t.Fatal("gateway grant lost its frozen scoped proof")
	}
	p := newProposal(g.AccountID, g.ConnectionID)
	p.SessionID = g.SessionID
	p.SessionScope = g.SessionScope
	proposal, err := db.InsertProposal(ctx, pool, p, 50)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := db.GetOwnedProposal(ctx, pool, g.AccountID, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SessionScope == nil || *loaded.SessionScope != *g.SessionScope {
		t.Fatal("proposal lost the original scoped grant issuance")
	}
}

func TestSessionScopedProofValidation(t *testing.T) {
	s := db.MCPSessionScope{AccountID: "00000000-0000-4000-8000-000000000145", RunnerInstallationID: "synthetic-runner", RunnerSessionID: pinSess1, HumanSessionID: "00000000-0000-4000-8000-000000000146", LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: strings.Repeat("a", 64)}
	if !s.Valid() {
		t.Fatal("complete scoped proof refused")
	}
	for _, mutate := range []func(*db.MCPSessionScope){func(s *db.MCPSessionScope) { s.RunnerSessionID = "" }, func(s *db.MCPSessionScope) { s.LaunchGeneration = 0 }, func(s *db.MCPSessionScope) { s.CredentialGeneration = 0 }, func(s *db.MCPSessionScope) { s.GrantTokenHash = "invalid" }, func(s *db.MCPSessionScope) { s.HumanSessionID = "" }, func(s *db.MCPSessionScope) { s.RunnerInstallationID = "" }} {
		bad := s
		mutate(&bad)
		if bad.Valid() {
			t.Fatal("partial scoped proof accepted")
		}
	}
}

func TestSessionScopedGrantLiveRequiresCurrentRuntime(t *testing.T) {
	pool := proposalTestPool(t)
	// Keep a deliberately stale grant to prove every admission predicate itself.
	// Runtime trigger detachment is independently covered by lifecycle tests.
	if _, err := pool.Exec(t.Context(), `ALTER TABLE sessions DISABLE TRIGGER mcp_binding_runtime_update`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var daemon string
	if err := pool.QueryRow(ctx, `INSERT INTO daemons(name,mode,repos_root) VALUES('synthetic','host','/workspace') RETURNING id::text`).Scan(&daemon); err != nil {
		t.Fatal(err)
	}
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{}, 1)
	g.AccountID = "00000000-0000-4000-8000-000000000145"
	g.ProofValue = "00000000-0000-4000-8000-000000000146"
	s := db.MCPSessionScope{AccountID: g.AccountID, RunnerInstallationID: "synthetic-runner", RunnerSessionID: g.SessionID, HumanSessionID: g.ProofValue, LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: hex.EncodeToString(g.TokenHash)}
	g.SessionScope = &s
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,spawning_account_id,mcp_launch_generation) VALUES($1,$2,'running','/workspace','synthetic',$3,1)`, g.SessionID, daemon, g.AccountID); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		got, err := db.MCPScopedGrantLive(ctx, pool, s, g.ConnectionID)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("live=%v want %v", got, want)
		}
	}
	check(true)
	for _, change := range []string{
		"mcp_launch_generation=0", "mcp_launch_generation=2", "status='completed'", "ended_at=now()", "discard_requested_at=now()", "delegation_ended_at=now()", "paused_reason='synthetic pause'", "spawning_account_id='other-owner'",
	} {
		t.Run(change, func(t *testing.T) {
			if _, err := pool.Exec(ctx, "UPDATE sessions SET "+change+" WHERE id=$1", g.SessionID); err != nil {
				t.Fatal(err)
			}
			check(false)
			if _, err := pool.Exec(ctx, `UPDATE sessions SET mcp_launch_generation=1,status='running',ended_at=NULL,discard_requested_at=NULL,delegation_ended_at=NULL,paused_reason=NULL,spawning_account_id=$2 WHERE id=$1`, g.SessionID, g.AccountID); err != nil {
				t.Fatal(err)
			}
			check(true)
		})
	}
	for _, mutate := range []func(*db.MCPSessionScope){
		func(s *db.MCPSessionScope) { s.RunnerInstallationID = "other-runner" },
		func(s *db.MCPSessionScope) { s.AccountID = "00000000-0000-4000-8000-000000000199" },
		func(s *db.MCPSessionScope) { s.RunnerSessionID = "00000000-0000-4000-8000-000000000199" },
		func(s *db.MCPSessionScope) { s.HumanSessionID = "00000000-0000-4000-8000-000000000199" },
		func(s *db.MCPSessionScope) { s.GrantTokenHash = strings.Repeat("b", 64) },
	} {
		original := s
		mutate(&s)
		check(false)
		s = original
	}
	if got, err := db.MCPScopedGrantLive(ctx, pool, s, "00000000-0000-4000-8000-000000000199"); err != nil || got {
		t.Fatal("foreign connection accepted", err)
	}
	old := s.CredentialGeneration
	s.CredentialGeneration++
	check(false)
	s.CredentialGeneration = old
	if _, err := pool.Exec(ctx, `DELETE FROM session_mcp_grants WHERE session_id=$1`, g.SessionID); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestSessionScopedSQLRequiresCompleteProof(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{}, 1)
	g.AccountID = "00000000-0000-4000-8000-000000000145"
	g.ProofValue = "00000000-0000-4000-8000-000000000146"
	g.SessionScope = &db.MCPSessionScope{AccountID: g.AccountID, RunnerInstallationID: "synthetic-runner", RunnerSessionID: g.SessionID, HumanSessionID: g.ProofValue, LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: hex.EncodeToString(g.TokenHash)}
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	p := newProposal(g.AccountID, g.ConnectionID)
	p.SessionID = g.SessionID
	p.SessionScope = g.SessionScope
	stored, err := db.InsertProposal(ctx, pool, p, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"session_mcp_grants", "mcp_proposals"} {
		where := "session_id=$1"
		id := g.SessionID
		if table == "mcp_proposals" {
			where = "id=$1"
			id = stored.ID
		}
		for _, key := range []string{"account_id", "runner_installation_id", "runner_session_id", "human_session_id", "launch_generation", "credential_generation", "grant_token_hash"} {
			if _, err := pool.Exec(ctx, "UPDATE "+table+" SET session_scope=session_scope-$2::text WHERE "+where, id, key); err == nil {
				t.Fatalf("%s accepted partial scope without %s", table, key)
			}
		}
		if _, err := pool.Exec(ctx, "UPDATE "+table+" SET session_scope='null'::jsonb WHERE "+where, id); err == nil {
			t.Fatalf("%s accepted JSON null scope", table)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE session_mcp_grants SET token_hash=decode(repeat('b',64),'hex') WHERE session_id=$1`, g.SessionID); err == nil {
		t.Fatal("grant hash could diverge from scope")
	}
}

func TestSessionScopedProposalSurvivesAccountConnectionCleanup(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	scope := db.MCPSessionScope{AccountID: "00000000-0000-4000-8000-000000000145", RunnerInstallationID: "synthetic-runner", RunnerSessionID: pinSess1, HumanSessionID: "00000000-0000-4000-8000-000000000146", LaunchGeneration: 1, CredentialGeneration: 2, GrantTokenHash: strings.Repeat("a", 64)}
	legacy, err := db.InsertProposal(ctx, pool, newProposal(scope.AccountID, pinConn1), 50)
	if err != nil {
		t.Fatal(err)
	}
	scoped := newProposal(scope.AccountID, pinConn1)
	scoped.SessionID = scope.RunnerSessionID
	scoped.SessionScope = &scope
	stored, err := db.InsertProposal(ctx, pool, scoped, 50)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := db.FailPendingProposals(ctx, pool, scope.AccountID, []string{pinConn1}, "synthetic account connection removed")
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("account cleanup changed %d proposals; wanted only the account-scoped row", changed)
	}
	for _, check := range []struct{ id, state string }{{legacy.ID, db.ProposalFailed}, {stored.ID, db.ProposalPending}} {
		p, err := db.GetOwnedProposal(ctx, pool, scope.AccountID, check.id)
		if err != nil || p.State != check.state {
			t.Fatalf("scope boundary lost: want=%s err=%v", check.state, err)
		}
	}
}
