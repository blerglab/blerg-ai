package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestToolDefinitionUpsertAndGet(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	d := db.ToolDefinition{Hash: "h1", Name: "read", Canonical: `{"description":"d","inputSchema":null,"name":"read"}`, Size: 46}
	if err := db.UpsertToolDefinition(ctx, pool, d); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertToolDefinition(ctx, pool, d); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := db.GetToolDefinition(ctx, pool, "h1")
	if err != nil || got.Canonical != d.Canonical || got.Unsafe != "" {
		t.Fatalf("got %+v err=%v", got, err)
	}
	if _, err := db.GetToolDefinition(ctx, pool, "nope"); !errors.Is(err, db.ErrToolDefinitionNotFound) {
		t.Fatalf("missing: %v", err)
	}
	u := db.ToolDefinition{Hash: "h2", Name: "big", Canonical: "", Size: 300000, Unsafe: db.UnsafeTooLarge}
	if err := db.UpsertToolDefinition(ctx, pool, u); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetToolDefinition(ctx, pool, "h2")
	if got.Unsafe != db.UnsafeTooLarge || got.Canonical != "" {
		t.Fatalf("unsafe row: %+v", got)
	}
}

func TestPruneToolDefinitionsKeepsReferenced(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	for _, h := range []string{"old-unref", "old-grant", "old-change", "old-event", "fresh"} {
		if err := db.UpsertToolDefinition(ctx, pool, db.ToolDefinition{Hash: h, Name: "t", Canonical: "{}", Size: 2}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE mcp_tool_definitions SET last_seen = now() - interval '200 days' WHERE hash <> 'fresh'`); err != nil {
		t.Fatal(err)
	}
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"read": {Mode: "allow", Hash: "old-grant"}}, 10)
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO mcp_tool_changes (account_id, session_id, connection_id, connection_name, tool, kind, old_hash, new_hash)
		VALUES ('acct-a', $1, $2, 'cal', 'read', 'changed', 'x', 'old-change')`, pinSess1, pinConn1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO mcp_pin_events (account_id, session_id, connection_id, connection_name, tool, action, old_hash, new_hash, actor)
		VALUES ('acct-a', $1, $2, 'cal', 'read', 'repinned', 'old-event', 'y', 'acct-a')`, pinSess1, pinConn1); err != nil {
		t.Fatal(err)
	}
	n, err := db.PruneToolDefinitions(ctx, pool, 180*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d err=%v, want 1", n, err)
	}
	if _, err := db.GetToolDefinition(ctx, pool, "old-unref"); !errors.Is(err, db.ErrToolDefinitionNotFound) {
		t.Fatal("unreferenced old definition survived")
	}
	for _, h := range []string{"old-grant", "old-change", "old-event", "fresh"} {
		if _, err := db.GetToolDefinition(ctx, pool, h); err != nil {
			t.Fatalf("%s was pruned: %v", h, err)
		}
	}
}

func TestUpsertToolDefinitionEnforcesUnsafeInvariant(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	big := db.ToolDefinition{Hash: "hbig", Name: "big", Canonical: "oversized text", Size: 300000, Unsafe: db.UnsafeTooLarge}
	if err := db.UpsertToolDefinition(ctx, pool, big); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetToolDefinition(ctx, pool, "hbig")
	if err != nil || got.Canonical != "" || got.Unsafe != db.UnsafeTooLarge {
		t.Fatalf("too_large must store no text: %+v err=%v", got, err)
	}
	bad := db.ToolDefinition{Hash: "hbad", Name: "bad", Canonical: "x", Size: 1, Unsafe: "weird"}
	if err := db.UpsertToolDefinition(ctx, pool, bad); err == nil {
		t.Fatal("an unknown unsafe value must be refused")
	}
}
