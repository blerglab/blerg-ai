package db_test

// Child sessions on the cluster (0.4.3): rules and grants name the cluster runtime, a cluster
// child row carries its parent link in the insert, and a cluster session remembers the owner's
// launch proof so a child it starts can be authorized with it.

import (
	"context"
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testClusterDaemon is the server's synthetic cluster daemon id (server.clusterDaemonID).
const testClusterDaemon = "00000000-0000-4000-8000-00000000f00d"

func mustClusterParent(t *testing.T, pool *pgxpool.Pool, account string) string {
	t.Helper()
	ctx := context.Background()
	if err := db.UpsertDaemon(ctx, pool, testClusterDaemon, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	id := newUUID(t, pool)
	if err := db.InsertClusterSessionAs(ctx, pool, id, testClusterDaemon, "running", "/workspace/app", "app", "Parent", "",
		db.SessionOrigin{SpawningAccount: account}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClusterDelegationRulesAndGrantsNameTheRuntime(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	parent := mustClusterParent(t, pool, "owner-c")
	rule, err := db.InsertDelegationRule(ctx, pool, db.DelegationRule{AccountID: "owner-c", Engine: "claude",
		Runtime: "cluster", DaemonID: testClusterDaemon, CreatedVia: "card"})
	if err != nil {
		t.Fatalf("a cluster rule is refused: %v", err)
	}
	if rule.Runtime != "cluster" || rule.DaemonName != "cluster" {
		t.Fatalf("rule = %+v", rule)
	}
	if got, err := db.CandidateDelegationRules(ctx, pool, "owner-c", "claude", "cluster", testClusterDaemon); err != nil || len(got) != 1 {
		t.Fatalf("cluster candidates = %v %v", got, err)
	}
	if got, err := db.CandidateDelegationRules(ctx, pool, "owner-c", "claude", "docker", testClusterDaemon); err != nil || len(got) != 0 {
		t.Fatalf("a cluster rule matched docker: %v %v", got, err)
	}
	g := scopeGrant(t, pool, db.DelegationGrant{ParentSessionID: parent, AccountID: "owner-c", Engine: "codex",
		Runtime: "cluster", DaemonID: testClusterDaemon, Repo: "app"})
	if found, err := db.FindDelegationGrant(ctx, pool, parent, "codex", "cluster", testClusterDaemon); err != nil || found == nil || found.ID != g.ID {
		t.Fatalf("cluster grant lookup = %v %v", found, err)
	}
	if found, err := db.FindDelegationGrant(ctx, pool, parent, "codex", "docker", testClusterDaemon); err != nil || found != nil {
		t.Fatalf("a cluster grant matched docker: %v %v", found, err)
	}
	if _, err := db.InsertDelegationRule(ctx, pool, db.DelegationRule{AccountID: "owner-c", Engine: "claude",
		Runtime: "pod", DaemonID: testClusterDaemon, CreatedVia: "card"}); err == nil {
		t.Fatal("a rule with an unknown runtime was stored")
	}
}

func TestInsertClusterChildCarriesItsParent(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	parent := mustClusterParent(t, pool, "owner-c")
	child := newUUID(t, pool)
	if err := db.InsertClusterSessionAs(ctx, pool, child, testClusterDaemon, "starting", "/workspace/app", "app", "Child", "",
		db.SessionOrigin{SpawningAccount: "owner-c", ParentSessionID: parent, DelegationVia: "rule:x", KeepRunning: true,
			RequireParentEpoch: true}); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(ctx, pool, child)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if derefS(row.ParentSessionID) != parent || row.DelegationVia != "rule:x" || !row.KeepRunning || derefS(row.Runtime) != "cluster" {
		t.Fatalf("child row = parent %v via %q keep %v runtime %v", row.ParentSessionID, row.DelegationVia, row.KeepRunning, row.Runtime)
	}

	// A parent whose delegation ended since the start read it gets no child.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET delegation_ended_at = now() WHERE id = $1`, parent); err != nil {
		t.Fatal(err)
	}
	late := newUUID(t, pool)
	err = db.InsertClusterSessionAs(ctx, pool, late, testClusterDaemon, "starting", "/workspace/app", "app", "Late", "",
		db.SessionOrigin{SpawningAccount: "owner-c", ParentSessionID: parent, DelegationVia: "rule:x", RequireParentEpoch: true})
	if !errors.Is(err, db.ErrDelegationEnded) {
		t.Fatalf("late child insert = %v, want ErrDelegationEnded", err)
	}
	if got, _ := db.GetSession(ctx, pool, late); got != nil {
		t.Fatal("a refused child left a row")
	}
}

func TestSessionLaunchProof(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	parent := mustClusterParent(t, pool, "owner-c")
	if tok, sid, err := db.GetSessionLaunchProof(ctx, pool, parent); err != nil || tok != "" || sid != "" {
		t.Fatalf("fresh proof = %q %q %v", tok, sid, err)
	}
	if err := db.SetSessionLaunchAuthSession(ctx, pool, parent, "sid-1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionLaunchAuthSession(ctx, pool, parent, ""); err != nil {
		t.Fatal(err)
	}
	if _, sid, err := db.GetSessionLaunchProof(ctx, pool, parent); err != nil || sid != "sid-1" {
		t.Fatalf("an empty sid erased the proof: %q %v", sid, err)
	}
	if err := db.SetSessionTokenID(ctx, pool, parent, "tok-1"); err != nil {
		t.Fatal(err)
	}
	if tok, sid, err := db.GetSessionLaunchProof(ctx, pool, parent); err != nil || tok != "tok-1" || sid != "sid-1" {
		t.Fatalf("proof = %q %q %v", tok, sid, err)
	}
	if _, _, err := db.GetSessionLaunchProof(ctx, pool, newUUID(t, pool)); err != nil {
		t.Fatalf("a missing session is not an error: %v", err)
	}
}

func derefS(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
