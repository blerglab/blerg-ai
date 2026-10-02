package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// proposalTestPool is a pool on its own schema with every migration applied. Like the other
// database tests of this package it needs TEST_DATABASE_URL.
func proposalTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	ctx := context.Background()
	const schema = "t31_dbproposals"
	boot, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema),
		fmt.Sprintf("CREATE SCHEMA %s", schema),
	} {
		if _, err := boot.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 16
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = boot.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
		boot.Close()
	})
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return pool
}

const (
	propConn1 = "11111111-1111-4111-8111-111111111111"
	propConn2 = "22222222-2222-4222-8222-222222222222"
)

func newProposal(account, conn string) db.Proposal {
	return db.Proposal{
		AccountID: account, ConnectionID: conn, ConnectionName: "mail",
		URLSnapshot: "https://mcp.example/mcp", Tool: "send", ToolHash: "hash-1",
		Arguments: []byte(`{"to":"a@example.com"}`), AgentSummary: "send: to=a@example.com",
	}
}

func TestProposalInsertAndOwnerScopedRead(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	p, err := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	if err != nil {
		t.Fatalf("InsertProposal: %v", err)
	}
	if p.ID == "" || p.State != db.ProposalPending || p.CreatedAt.IsZero() || p.DecidedAt != nil {
		t.Fatalf("unexpected row: %+v", p)
	}
	got, err := db.GetOwnedProposal(ctx, pool, "acct-a", p.ID)
	if err != nil || got.Tool != "send" || string(got.Arguments) != `{"to": "a@example.com"}` {
		t.Fatalf("GetOwnedProposal = %+v, %v", got, err)
	}
	if _, err := db.GetOwnedProposal(ctx, pool, "acct-b", p.ID); !errors.Is(err, db.ErrProposalNotFound) {
		t.Errorf("another account's proposal must look absent, got %v", err)
	}
	if _, err := db.GetOwnedProposal(ctx, pool, "acct-a", "not-a-uuid"); !errors.Is(err, db.ErrProposalNotFound) {
		t.Errorf("a malformed id must look absent, got %v", err)
	}
}

func TestProposalPendingCapIsPerAccountAndAtomic(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	const limit = 5
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, capped := 0, 0
	for range 20 { // twenty concurrent inserts race for five places
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), limit)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, db.ErrProposalCap):
				capped++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != limit || capped != 20-limit {
		t.Fatalf("inserted %d, capped %d; want %d and %d", ok, capped, limit, 20-limit)
	}
	if _, err := db.InsertProposal(ctx, pool, newProposal("acct-b", propConn1), limit); err != nil {
		t.Errorf("another account has its own cap: %v", err)
	}
	// A decided proposal no longer counts.
	list, _ := db.ListOwnedProposals(ctx, pool, "acct-a", "", 100)
	if _, err := db.RejectProposal(ctx, pool, "acct-a", list[0].ID, "acct-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), limit); err != nil {
		t.Errorf("a rejected proposal frees a place: %v", err)
	}
}

func TestProposalArgumentsCapRefused(t *testing.T) {
	pool := proposalTestPool(t)
	p := newProposal("acct-a", propConn1)
	big := make([]byte, 0, 70<<10)
	big = append(big, `{"x":"`...)
	for range 70 << 10 {
		big = append(big, 'a')
	}
	big = append(big, `"}`...)
	p.Arguments = big
	// The column's own CHECK is the last line of defence; the sink validates earlier.
	if _, err := db.InsertProposal(context.Background(), pool, p, 50); !errors.Is(err, db.ErrProposalArguments) {
		t.Fatalf("err = %v, want ErrProposalArguments", err)
	}
}

func TestProposalStateMachine(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	mk := func() string {
		p, err := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
		if err != nil {
			t.Fatal(err)
		}
		return p.ID
	}

	// approve: pending -> executing exactly once, only for the owner.
	id := mk()
	if _, err := db.BeginProposalExecution(ctx, pool, "acct-b", id, "acct-b"); !errors.Is(err, db.ErrProposalNotFound) {
		t.Errorf("another account: %v", err)
	}
	p, err := db.BeginProposalExecution(ctx, pool, "acct-a", id, "acct-a")
	if err != nil || p.State != db.ProposalExecuting || p.DecidedBy == nil || *p.DecidedBy != "acct-a" || p.DecidedAt == nil {
		t.Fatalf("Begin = %+v, %v", p, err)
	}
	if _, err := db.BeginProposalExecution(ctx, pool, "acct-a", id, "acct-a"); !errors.Is(err, db.ErrProposalState) {
		t.Errorf("a second approval must lose: %v", err)
	}
	if _, err := db.RejectProposal(ctx, pool, "acct-a", id, "acct-a"); !errors.Is(err, db.ErrProposalState) {
		t.Errorf("an executing proposal cannot be rejected: %v", err)
	}
	if _, err := db.FinishProposal(ctx, pool, id, db.ProposalDone, []byte(`{"content":[]}`)); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, err := db.FinishProposal(ctx, pool, id, db.ProposalFailed, nil); !errors.Is(err, db.ErrProposalState) {
		t.Errorf("a finished proposal cannot be finished again: %v", err)
	}
	if _, err := db.FinishProposal(ctx, pool, mk(), db.ProposalDone, nil); !errors.Is(err, db.ErrProposalState) {
		t.Errorf("a pending proposal cannot be finished without executing: %v", err)
	}
	if _, err := db.FinishProposal(ctx, pool, id, db.ProposalRejected, nil); err == nil {
		t.Error("Finish accepts only done, failed or unknown")
	}

	// reject
	rid := mk()
	if p, err := db.RejectProposal(ctx, pool, "acct-a", rid, "acct-a"); err != nil || p.State != db.ProposalRejected || p.DecidedAt == nil {
		t.Fatalf("Reject = %+v, %v", p, err)
	}
	if _, err := db.BeginProposalExecution(ctx, pool, "acct-a", rid, "acct-a"); !errors.Is(err, db.ErrProposalState) {
		t.Errorf("a rejected proposal cannot be approved: %v", err)
	}

	// resolve: only an unknown outcome, only by the owner, only to done or failed.
	uid := mk()
	_, _ = db.BeginProposalExecution(ctx, pool, "acct-a", uid, "acct-a")
	if _, err := db.FinishProposal(ctx, pool, uid, db.ProposalUnknown, []byte(`{"error":"timed out"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveProposal(ctx, pool, "acct-b", uid, db.ProposalDone, "acct-b"); !errors.Is(err, db.ErrProposalNotFound) {
		t.Errorf("another account: %v", err)
	}
	if _, err := db.ResolveProposal(ctx, pool, "acct-a", uid, db.ProposalRejected, "acct-a"); err == nil {
		t.Error("resolve accepts done or failed only")
	}
	if p, err := db.ResolveProposal(ctx, pool, "acct-a", uid, db.ProposalDone, "acct-a"); err != nil || p.State != db.ProposalDone {
		t.Fatalf("Resolve = %+v, %v", p, err)
	}
	if _, err := db.ResolveProposal(ctx, pool, "acct-a", id, db.ProposalDone, "acct-a"); !errors.Is(err, db.ErrProposalState) {
		t.Errorf("a done proposal is not unknown: %v", err)
	}

	// release: a refused approval attempt puts executing back to pending without a decision.
	xid := mk()
	_, _ = db.BeginProposalExecution(ctx, pool, "acct-a", xid, "acct-a")
	if p, err := db.ReleaseProposal(ctx, pool, xid); err != nil || p.State != db.ProposalPending || p.DecidedAt != nil || p.DecidedBy != nil {
		t.Fatalf("Release = %+v, %v", p, err)
	}
}

func TestProposalConcurrentBeginHasOneWinner(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	p, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := db.BeginProposalExecution(ctx, pool, "acct-a", p.ID, "acct-a"); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d approvals won, want 1", won)
	}
}

func TestProposalListFiltersByStateAndOwner(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	a, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	b, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	_, _ = db.InsertProposal(ctx, pool, newProposal("acct-b", propConn1), 50)
	_, _ = db.RejectProposal(ctx, pool, "acct-a", a.ID, "acct-a")

	all, err := db.ListOwnedProposals(ctx, pool, "acct-a", "", 100)
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %d, %v", len(all), err)
	}
	if all[0].ID != b.ID {
		t.Errorf("newest first: got %s first, want %s", all[0].ID, b.ID)
	}
	pend, _ := db.ListOwnedProposals(ctx, pool, "acct-a", db.ProposalPending, 100)
	if len(pend) != 1 || pend[0].ID != b.ID {
		t.Errorf("pending = %+v", pend)
	}
	if n, _ := db.CountPendingProposals(ctx, pool, "acct-a"); n != 1 {
		t.Errorf("pending count = %d", n)
	}
}

func TestProposalExpiryAndPrune(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	old, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	fresh, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	if _, err := pool.Exec(ctx, `UPDATE mcp_proposals SET created_at = now() - interval '8 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	n, err := db.ExpirePendingProposals(ctx, pool, 7*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("expired %d, %v; want 1", n, err)
	}
	if p, _ := db.GetOwnedProposal(ctx, pool, "acct-a", old.ID); p.State != db.ProposalExpired || p.DecidedAt == nil {
		t.Errorf("old = %+v", p)
	}
	if p, _ := db.GetOwnedProposal(ctx, pool, "acct-a", fresh.ID); p.State != db.ProposalPending {
		t.Errorf("fresh = %+v", p)
	}

	// A crashed approval leaves a row executing: it becomes unknown (ambiguous), never pending.
	ex, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	_, _ = db.BeginProposalExecution(ctx, pool, "acct-a", ex.ID, "acct-a")
	if n, _ := db.UnknownStaleExecuting(ctx, pool, time.Hour); n != 0 {
		t.Errorf("a running approval is not stale: %d", n)
	}
	if _, err := pool.Exec(ctx, `UPDATE mcp_proposals SET decided_at = now() - interval '2 hours' WHERE id = $1`, ex.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.UnknownStaleExecuting(ctx, pool, time.Hour); n != 1 {
		t.Errorf("stale executing = %d, want 1", n)
	}
	if p, _ := db.GetOwnedProposal(ctx, pool, "acct-a", ex.ID); p.State != db.ProposalUnknown {
		t.Errorf("ex = %+v", p)
	}

	// Prune: decided rows go 30 days after the decision; pending rows never.
	if _, err := pool.Exec(ctx, `UPDATE mcp_proposals SET decided_at = now() - interval '31 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := db.PruneDecidedProposals(ctx, pool, 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want 1", n, err)
	}
	if _, err := db.GetOwnedProposal(ctx, pool, "acct-a", old.ID); !errors.Is(err, db.ErrProposalNotFound) {
		t.Error("the old decided proposal should be gone")
	}
	if _, err := db.GetOwnedProposal(ctx, pool, "acct-a", fresh.ID); err != nil {
		t.Error("a pending proposal is never pruned")
	}
}

func TestProposalFailPendingForConnections(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	a, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	b, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn2), 50)
	c, _ := db.InsertProposal(ctx, pool, newProposal("acct-b", propConn1), 50)
	n, err := db.FailPendingProposals(ctx, pool, "acct-a", []string{propConn1}, "the connection was deleted")
	if err != nil || n != 1 {
		t.Fatalf("failed %d, %v; want 1", n, err)
	}
	if p, _ := db.GetOwnedProposal(ctx, pool, "acct-a", a.ID); p.State != db.ProposalFailed || len(p.Result) == 0 {
		t.Errorf("a = %+v", p)
	}
	if p, _ := db.GetOwnedProposal(ctx, pool, "acct-a", b.ID); p.State != db.ProposalPending {
		t.Errorf("another connection must stay pending: %+v", p)
	}
	if p, _ := db.GetOwnedProposal(ctx, pool, "acct-b", c.ID); p.State != db.ProposalPending {
		t.Errorf("another account must stay pending: %+v", p)
	}
}
