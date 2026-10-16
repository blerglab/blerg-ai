package db_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestRepinGrantToolIsCompareAndSet(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"read": {Mode: "allow", Hash: "h1"}, "send": {Mode: "propose", Hash: "s1"}}, 5)
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	ok, err := db.RepinGrantTool(ctx, pool, pinSess1, pinConn1, "read", "h1", "h2", "")
	if err != nil || !ok {
		t.Fatalf("repin h1->h2: ok=%v err=%v", ok, err)
	}
	// A second caller still holding h1 loses.
	ok, err = db.RepinGrantTool(ctx, pool, pinSess1, pinConn1, "read", "h1", "h3", "")
	if err != nil || ok {
		t.Fatalf("stale repin must not win: ok=%v err=%v", ok, err)
	}
	got, _ := db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	e := got.Tools["read"]
	if e.Hash != "h2" || e.Mode != "allow" || e.IsListed() {
		t.Fatalf("after repin: %+v (want h2, allow kept, listed false)", e)
	}
	if got.Tools["send"].Hash != "s1" {
		t.Fatal("another entry was touched")
	}
}

func TestRepinGrantToolMayTightenNeverWiden(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"read": {Mode: "allow", Hash: "h1"}, "send": {Mode: "propose", Hash: "s1"}}, 6)
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.RepinGrantTool(ctx, pool, pinSess1, pinConn1, "read", "h1", "h2", "propose"); err != nil || !ok {
		t.Fatalf("tighten allow->propose: ok=%v err=%v", ok, err)
	}
	if ok, err := db.RepinGrantTool(ctx, pool, pinSess1, pinConn1, "send", "s1", "s2", "allow"); err != nil || ok {
		t.Fatalf("widen propose->allow must be refused: ok=%v err=%v", ok, err)
	}
	got, _ := db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	if got.Tools["read"].Mode != "propose" || got.Tools["send"].Hash != "s1" {
		t.Fatalf("modes: %+v", got.Tools)
	}
}

// Review Focus 2: the tool name is a bound path element, never SQL.
func TestRepinGrantToolWithHostileName(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	name := `a,b{c}"d'e\f`
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{name: {Mode: "allow", Hash: "h1"}}, 7)
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.RepinGrantTool(ctx, pool, pinSess1, pinConn1, name, "h1", "h2", ""); err != nil || !ok {
		t.Fatalf("repin hostile name: ok=%v err=%v", ok, err)
	}
	got, _ := db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	if got.Tools[name].Hash != "h2" || len(got.Tools) != 1 {
		t.Fatalf("tools after repin: %+v", got.Tools)
	}
}

func TestGrantToolNeverOverwrites(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"send": {Mode: "propose", Hash: "s1"}}, 8)
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.GrantTool(ctx, pool, pinSess1, pinConn1, "new", "n1", "allow"); err != nil || !ok {
		t.Fatalf("grant new: ok=%v err=%v", ok, err)
	}
	if ok, err := db.GrantTool(ctx, pool, pinSess1, pinConn1, "send", "s1", "allow"); err != nil || ok {
		t.Fatalf("grant over an existing entry must be refused: ok=%v err=%v", ok, err)
	}
	got, _ := db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	if got.Tools["new"].Mode != "allow" || got.Tools["new"].IsListed() || got.Tools["send"].Mode != "propose" {
		t.Fatalf("tools: %+v", got.Tools)
	}
}

func TestMarkListedMatchesTheHashServed(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"read": {Mode: "allow", Hash: "h1"}}, 9)
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RepinGrantTool(ctx, pool, pinSess1, pinConn1, "read", "h1", "h2", ""); err != nil {
		t.Fatal(err)
	}
	// A list that served the OLD hash (it raced the re-pin) must not mark the new one listed.
	if ok, err := db.MarkListed(ctx, pool, pinSess1, pinConn1, "read", "h1"); err != nil || ok {
		t.Fatalf("stale MarkListed must not match: ok=%v err=%v", ok, err)
	}
	got, _ := db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	if got.Tools["read"].IsListed() {
		t.Fatal("listed cleared by a stale list")
	}
	if ok, err := db.MarkListed(ctx, pool, pinSess1, pinConn1, "read", "h2"); err != nil || !ok {
		t.Fatalf("MarkListed h2: ok=%v err=%v", ok, err)
	}
	got, _ = db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	if !got.Tools["read"].IsListed() || got.Tools["read"].Listed != nil {
		t.Fatalf("after MarkListed the key must be gone: %+v", got.Tools["read"])
	}
}
