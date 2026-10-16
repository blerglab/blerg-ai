package db_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func pinEvent(action, oldHash, newHash, actor string) db.PinEvent {
	return db.PinEvent{AccountID: "acct-a", SessionID: pinSess1, ConnectionID: pinConn1, ConnectionName: "cal",
		Tool: "read", Action: action, OldHash: oldHash, NewHash: newHash, Mode: "allow", Actor: actor}
}

func TestPinEventsListAndPrune(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	if err := db.InsertPinEvent(ctx, pool, pinEvent(db.PinRepinned, "h1", "h2", "acct-a")); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertPinEvent(ctx, pool, pinEvent(db.PinDeclined, "h1", "h3", "acct-a")); err != nil {
		t.Fatal(err)
	}
	bySess, err := db.ListPinEventsForSession(ctx, pool, "acct-a", pinSess1, 200)
	if err != nil || len(bySess) != 2 || bySess[0].Action != db.PinDeclined {
		t.Fatalf("by session: %+v err=%v", bySess, err)
	}
	if other, _ := db.ListPinEventsForSession(ctx, pool, "acct-b", pinSess1, 200); len(other) != 0 {
		t.Fatal("another account can read the events")
	}
	byConn, _ := db.ListPinEventsForConnection(ctx, pool, "acct-a", pinConn1, 1)
	if len(byConn) != 1 {
		t.Fatalf("limit ignored: %d", len(byConn))
	}
	if _, err := pool.Exec(ctx, `UPDATE mcp_pin_events SET created_at = now() - interval '181 days'`); err != nil {
		t.Fatal(err)
	}
	if n, err := db.PrunePinEvents(ctx, pool, db.PinEventRetention); err != nil || n != 2 {
		t.Fatalf("pruned %d err=%v", n, err)
	}
}

func TestInsertPinEventValidates(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	if err := db.InsertPinEvent(ctx, pool, pinEvent("bogus", "", "", "x")); err == nil {
		t.Fatal("an unknown action must be refused")
	}
	e := pinEvent(db.PinGranted, "", "h1", "")
	if err := db.InsertPinEvent(ctx, pool, e); err == nil {
		t.Fatal("an empty actor must be refused")
	}
}

func TestRepinToolWithEventIsAtomic(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"read": {Mode: "allow", Hash: "h1"}}, 11)
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	// The event the caller passes is deliberately wrong: the stored one comes from the write.
	wrong := pinEvent(db.PinDeclined, "x1", "x2", "policy:auto_accept")
	wrong.Tool, wrong.Mode = "other", "propose"
	ok, err := db.RepinToolWithEvent(ctx, pool, pinSess1, pinConn1, "read", "h1", "h2", "", wrong)
	if err != nil || !ok {
		t.Fatalf("repin: ok=%v err=%v", ok, err)
	}
	// The stale writer: no grant change, and NO event either.
	ok, err = db.RepinToolWithEvent(ctx, pool, pinSess1, pinConn1, "read", "h1", "h9", "", pinEvent(db.PinRepinned, "h1", "h9", "policy:auto_accept"))
	if err != nil || ok {
		t.Fatalf("stale repin: ok=%v err=%v", ok, err)
	}
	evs, _ := db.ListPinEventsForSession(ctx, pool, "acct-a", pinSess1, 10)
	if len(evs) != 1 || evs[0].NewHash != "h2" || evs[0].Actor != "policy:auto_accept" {
		t.Fatalf("events: %+v", evs)
	}
	if e := evs[0]; e.Tool != "read" || e.OldHash != "h1" || e.Action != db.PinRepinned || e.Mode != "allow" {
		t.Fatalf("repin event does not carry the write: %+v", e)
	}
	wrong = pinEvent(db.PinRepinned, "x1", "x2", "policy:auto_accept")
	wrong.Tool, wrong.Mode = "other", "allow"
	ok, err = db.GrantToolWithEvent(ctx, pool, pinSess1, pinConn1, "new", "n1", "propose", wrong)
	if err != nil || !ok {
		t.Fatalf("grant: ok=%v err=%v", ok, err)
	}
	evs, _ = db.ListPinEventsForSession(ctx, pool, "acct-a", pinSess1, 10)
	if e := evs[0]; e.Tool != "new" || e.OldHash != "" || e.NewHash != "n1" || e.Action != db.PinGranted || e.Mode != "propose" {
		t.Fatalf("grant event does not carry the write: %+v", e)
	}
	if ok, _ := db.GrantToolWithEvent(ctx, pool, pinSess1, pinConn1, "new", "n2", "allow", pinEvent(db.PinGranted, "", "n2", "acct-a")); ok {
		t.Fatal("a second grant of the same tool must not win")
	}
	evs, _ = db.ListPinEventsForSession(ctx, pool, "acct-a", pinSess1, 10)
	if len(evs) != 2 {
		t.Fatalf("events after grant: %d", len(evs))
	}
	got, _ := db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	if got.Tools["new"].Mode != "propose" || got.Tools["read"].Hash != "h2" {
		t.Fatalf("grant row: %+v", got.Tools)
	}
}
