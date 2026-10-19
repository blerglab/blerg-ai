package db_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func scopeGrant(t *testing.T, pool *pgxpool.Pool, grant db.DelegationGrant) db.DelegationGrant {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	got, err := db.InsertDelegationGrantTx(t.Context(), tx, grant)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDelegationGrantScopeConcurrentApprovals(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-scope")
	const workers = 8
	var wg sync.WaitGroup
	results := make(chan db.DelegationGrant, workers)
	failures := make(chan error, workers)
	start := make(chan struct{})
	for i := range workers {
		wg.Go(func() {
			<-start
			tx, err := pool.Begin(t.Context())
			if err != nil {
				failures <- err
				return
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			repo := "first"
			if i%2 == 0 {
				repo = "other"
			}
			got, err := db.InsertDelegationGrantTx(t.Context(), tx, db.DelegationGrant{ParentSessionID: parent, AccountID: "owner-scope", Engine: "codex", Runtime: "docker", DaemonID: daemon, Repo: repo})
			if err == nil {
				err = tx.Commit(t.Context())
			}
			if err != nil {
				failures <- err
				return
			}
			results <- got
		})
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	id := ""
	for got := range results {
		if id == "" {
			id = got.ID
		}
		if got.ID != id {
			t.Fatal("concurrent approvals returned different live grants")
		}
	}
	live, err := db.ListLiveDelegationGrants(t.Context(), pool, parent)
	if err != nil || len(live) != 1 {
		t.Fatalf("live scopes=%d: %v", len(live), err)
	}
	if ok, err := db.RevokeDelegationGrant(t.Context(), pool, id, "owner-scope"); err != nil || !ok {
		t.Fatalf("revoke canonical=%v: %v", ok, err)
	}
	if live, err := db.ListLiveDelegationGrants(t.Context(), pool, parent); err != nil || len(live) != 0 {
		t.Fatalf("duplicate authority survived revoke: %d %v", len(live), err)
	}
}

func TestDelegationGrantScopeNewLifetimeReplacesUncleanedOldGrant(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-scope")
	grant := db.DelegationGrant{ParentSessionID: parent, AccountID: "owner-scope", Engine: "codex", Runtime: "docker", DaemonID: daemon, Repo: "first"}
	old := scopeGrant(t, pool, grant)
	var cutoff time.Time
	if err := pool.QueryRow(t.Context(), `UPDATE sessions SET delegation_ended_at=clock_timestamp() WHERE id=$1 RETURNING delegation_ended_at`, parent).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	grant.Repo = "second"
	fresh := scopeGrant(t, pool, grant)
	if old.ID == fresh.ID {
		t.Fatal("new lifetime reused an invalid old grant")
	}
	if err := db.RevokeParentDelegationGrantsThrough(t.Context(), pool, parent, cutoff); err != nil {
		t.Fatal(err)
	}
	live, err := db.ListLiveDelegationGrants(t.Context(), pool, parent)
	if err != nil || len(live) != 1 || live[0].ID != fresh.ID || live[0].Repo != "second" {
		t.Fatalf("new grant lost to delayed cleanup: %+v %v", live, err)
	}
	var revoked bool
	if err := pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM delegation_grants WHERE id=$1`, old.ID).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("old lifetime occupies unique scope: %v %v", revoked, err)
	}
	otherParent := mustSession(t, pool, daemon, "owner-scope")
	got, err := db.FindDelegationGrant(t.Context(), pool, otherParent, "codex", "docker", daemon)
	if err != nil || got != nil {
		t.Fatalf("grant crossed parent: %+v %v", got, err)
	}
}

func TestDelegationGrantScopeMigrationPreservesAudit(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	otherDaemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-scope")
	if _, err := pool.Exec(t.Context(), `DROP INDEX IF EXISTS delegation_grants_live_scope_idx; DELETE FROM schema_migrations WHERE filename='061_delegation_grant_scope.sql'`); err != nil {
		t.Fatal(err)
	}
	insert := func(engine, runtime, daemonID, repo, age string, noRepo, revoked bool) string {
		t.Helper()
		var id string
		err := pool.QueryRow(t.Context(), `INSERT INTO delegation_grants(parent_session_id,account_id,engine,runtime,daemon_id,repo,no_repo,created_at,revoked_at)
  VALUES($1,'owner-scope',$2,$3,$4,$5,$6,now()-$7::interval,CASE WHEN $8 THEN now() END) RETURNING id::text`, parent, engine, runtime, daemonID, repo, noRepo, age, revoked).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	revoked := insert("codex", "docker", daemon, "revoked", "5 hours", false, true)
	canonical := insert("codex", "docker", daemon, "first", "4 hours", false, false)
	duplicate := insert("codex", "docker", daemon, "second", "3 hours", false, false)
	noRepo := insert("codex", "docker", daemon, "", "2 hours", true, false)
	otherEngine := insert("claude", "docker", daemon, "first", "1 hour", false, false)
	otherRuntime := insert("codex", "daemon", daemon, "first", "1 hour", false, false)
	otherHost := insert("codex", "docker", otherDaemon, "first", "1 hour", false, false)
	if err := db.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	live, err := db.ListLiveDelegationGrants(t.Context(), pool, parent)
	if err != nil || len(live) != 4 {
		t.Fatalf("migration live scopes=%d: %v", len(live), err)
	}
	wanted := map[string]bool{canonical: true, otherEngine: true, otherRuntime: true, otherHost: true}
	for _, g := range live {
		if !wanted[g.ID] {
			t.Fatalf("wrong canonical grant %s", g.ID)
		}
	}
	var total int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM delegation_grants WHERE parent_session_id=$1`, parent).Scan(&total); err != nil || total != 7 {
		t.Fatalf("audit rows deleted: %d %v", total, err)
	}
	for _, id := range []string{revoked, duplicate, noRepo} {
		var isRevoked bool
		if err := pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM delegation_grants WHERE id=$1`, id).Scan(&isRevoked); err != nil || !isRevoked {
			t.Fatalf("redundant grant still live: %s %v", id, err)
		}
	}
	// Uniqueness must be enforced even for writers bypassing the helper.
	if _, err := pool.Exec(t.Context(), `INSERT INTO delegation_grants(parent_session_id,account_id,engine,runtime,daemon_id,repo) VALUES($1,'owner-scope','codex','docker',$2,'third')`, parent, daemon); err == nil {
		t.Fatal("duplicate live scope bypassed unique index")
	}
}

func TestDelegationGrantScopeCannotCrossParentOwnership(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-first")
	grant := db.DelegationGrant{ParentSessionID: parent, AccountID: "owner-first", Engine: "codex", Runtime: "docker", DaemonID: daemon, Repo: "first"}
	old := scopeGrant(t, pool, grant)
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='owner-next' WHERE id=$1`, parent); err != nil {
		t.Fatal(err)
	}
	got, err := db.FindDelegationGrant(t.Context(), pool, parent, "codex", "docker", daemon)
	if err != nil || got != nil {
		t.Fatalf("old owner's grant still authorizes the parent: %+v %v", got, err)
	}
	if live, err := db.ListLiveDelegationGrants(t.Context(), pool, parent); err != nil || len(live) != 0 {
		t.Fatalf("old owner's grant exposed as live: %d %v", len(live), err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.InsertDelegationGrantTx(t.Context(), tx, grant)
	_ = tx.Rollback(t.Context())
	if err == nil {
		t.Fatal("old owner inserted a grant after ownership changed")
	}
	grant.AccountID = "owner-next"
	grant.Repo = "second"
	fresh := scopeGrant(t, pool, grant)
	if fresh.ID == old.ID || fresh.AccountID != "owner-next" || fresh.Repo != "second" {
		t.Fatalf("approval reused foreign authority/audit: %+v", fresh)
	}
	var revoked bool
	if err := pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM delegation_grants WHERE id=$1`, old.ID).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("old account scope not revoked: %v %v", revoked, err)
	}
}

func TestDelegationGrantScopeMigrationRetainsOnlyCurrentAuthority(t *testing.T) {
	for _, reason := range []string{"owner changed", "parent ended"} {
		t.Run(reason, func(t *testing.T) {
			pool := connect(t)
			daemon := mustDaemon(t, pool)
			parent := mustSession(t, pool, daemon, "owner-first")
			if _, err := pool.Exec(t.Context(), `DROP INDEX delegation_grants_live_scope_idx; DELETE FROM schema_migrations WHERE filename='061_delegation_grant_scope.sql'`); err != nil {
				t.Fatal(err)
			}
			var oldID, freshID string
			if err := pool.QueryRow(t.Context(), `INSERT INTO delegation_grants(parent_session_id,account_id,engine,runtime,daemon_id,repo,created_at) VALUES($1,'owner-first','codex','docker',$2,'old',now()-interval '1 hour') RETURNING id::text`, parent, daemon).Scan(&oldID); err != nil {
				t.Fatal(err)
			}
			if reason == "owner changed" {
				if _, err := pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='owner-next' WHERE id=$1`, parent); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := pool.Exec(t.Context(), `UPDATE sessions SET delegation_ended_at=now()-interval '30 minutes' WHERE id=$1`, parent); err != nil {
					t.Fatal(err)
				}
			}
			if err := pool.QueryRow(t.Context(), `INSERT INTO delegation_grants(parent_session_id,account_id,engine,runtime,daemon_id,repo) SELECT id,spawning_account_id,'codex','docker',$2,'fresh' FROM sessions WHERE id=$1 RETURNING id::text`, parent, daemon).Scan(&freshID); err != nil {
				t.Fatal(err)
			}
			if err := db.RunMigrations(t.Context(), pool); err != nil {
				t.Fatal(err)
			}
			live, err := db.ListLiveDelegationGrants(t.Context(), pool, parent)
			if err != nil || len(live) != 1 || live[0].ID != freshID {
				t.Fatalf("migration retained obsolete authority: %+v %v", live, err)
			}
			var revoked bool
			if err := pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM delegation_grants WHERE id=$1`, oldID).Scan(&revoked); err != nil || !revoked {
				t.Fatalf("obsolete audit not revoked: %v %v", revoked, err)
			}
		})
	}
}
