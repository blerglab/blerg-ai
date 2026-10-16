package server

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestMaintenanceExpiresAndPrunesToolChanges(t *testing.T) {
	fx := newTCFx(t)
	ctx := context.Background()
	c := fx.drift("v2")
	if _, err := fx.pool.Exec(ctx, `UPDATE mcp_tool_changes SET created_at = now() - interval '8 days' WHERE id = $1::uuid`, c.ID); err != nil {
		t.Fatal(err)
	}
	fx.api.maintainProposals(ctx)
	row, _ := db.GetOwnedToolChange(ctx, fx.pool, mcpAcct, c.ID)
	if row.State != db.ToolChangeExpired {
		t.Fatalf("state = %s", row.State)
	}
	if evs, _ := db.ListPinEventsForSession(ctx, fx.pool, mcpAcct, fx.sessionID, 10); len(evs) != 1 || evs[0].Action != db.PinExpired {
		t.Fatalf("expiry event = %+v", evs)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE mcp_tool_changes SET decided_at = now() - interval '31 days'; UPDATE mcp_pin_events SET created_at = now() - interval '181 days'`); err != nil {
		t.Fatal(err)
	}
	fx.api.maintainProposals(ctx)
	if _, err := db.GetOwnedToolChange(ctx, fx.pool, mcpAcct, c.ID); err == nil {
		t.Fatal("decided row not pruned")
	}
	if evs, _ := db.ListPinEventsForSession(ctx, fx.pool, mcpAcct, fx.sessionID, 10); len(evs) != 0 {
		t.Fatalf("events not pruned: %d", len(evs))
	}
}
