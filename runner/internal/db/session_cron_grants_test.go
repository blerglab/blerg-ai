package db_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestMCPGrantRefsAndDelete(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-0000000000d1"
	endTestSession(t, pool, sid, "running")
	mk := func(conn string, hash byte) db.MCPGrant {
		return db.MCPGrant{
			SessionID: sid, ConnectionID: conn, Name: "n" + conn[len(conn)-1:], AccountID: scAcct,
			Tools: map[string]db.MCPGrantTool{"echo": {Mode: "allow", Hash: "h"}}, URLSnapshot: "https://x.example/mcp",
			ProofKind: "token_id", ProofValue: "tok", TokenHash: []byte{hash, 9, 9}, CallBudget: 10,
		}
	}
	c1, c2 := "00000000-0000-4000-8000-0000000000e1", "00000000-0000-4000-8000-0000000000e2"
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{mk(c1, 1), mk(c2, 2)}); err != nil {
		t.Fatal(err)
	}
	refs, err := db.ListMCPGrantRefs(ctx, pool)
	if err != nil || len(refs) != 2 || refs[0].AccountID != scAcct || refs[0].ProofKind != "token_id" || refs[0].ProofValue != "tok" {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
	if ok, err := db.DeleteMCPGrant(ctx, pool, sid, c1); err != nil || !ok {
		t.Fatalf("delete = %v, %v", ok, err)
	}
	if ok, _ := db.DeleteMCPGrant(ctx, pool, sid, c1); ok {
		t.Error("deleting twice reported a row")
	}
	if g, _ := db.ListMCPGrantsForSession(ctx, pool, sid); len(g) != 1 || g[0].ConnectionID != c2 {
		t.Errorf("the other connection's grant must remain: %+v", g)
	}
}
