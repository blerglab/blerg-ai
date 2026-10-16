package db_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func change(kind, oldHash, newHash string) db.ToolChange {
	return db.ToolChange{
		AccountID: "acct-a", SessionID: pinSess1, ConnectionID: pinConn1, ConnectionName: "cal",
		Tool: "read", Kind: kind, OldHash: oldHash, NewHash: newHash, Mode: "allow",
	}
}

var noLimits = db.ToolChangeLimits{}

func TestRecordToolChangeOncePerHashAndSupersede(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	out, err := db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
	if err != nil || !out.Inserted || out.ID == "" {
		t.Fatalf("first: %+v err=%v", out, err)
	}
	for i := 0; i < 10; i++ {
		out, err = db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
		if err != nil || !out.AlreadyPending || out.Inserted {
			t.Fatalf("repeat %d: %+v err=%v", i, out, err)
		}
	}
	open, _ := db.ListOpenToolChanges(ctx, pool, "acct-a", "")
	if len(open) != 1 {
		t.Fatalf("open rows = %d, want 1", len(open))
	}
	// A newer hash supersedes the pending row and opens a new one.
	out, err = db.RecordToolChange(ctx, pool, change("changed", "h1", "h3"), noLimits)
	if err != nil || !out.Inserted {
		t.Fatalf("h3: %+v err=%v", out, err)
	}
	open, _ = db.ListOpenToolChanges(ctx, pool, "acct-a", "")
	if len(open) != 1 || open[0].NewHash != "h3" {
		t.Fatalf("open after supersede: %+v", open)
	}
	decided, _, _ := db.ListDecidedToolChanges(ctx, pool, "acct-a", "", "", 50)
	if len(decided) != 1 || decided[0].State != db.ToolChangeSuperseded || decided[0].DecidedAt == nil || *decided[0].DecidedBy != "system:superseded" {
		t.Fatalf("superseded row: %+v", decided)
	}
	// A -> B -> A: the earlier hash can be opened again.
	out, err = db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
	if err != nil || !out.Inserted {
		t.Fatalf("reopen h2: %+v err=%v", out, err)
	}
}

// Review Focus 3: a declined hash is never re-asked; a different hash is recorded muted.
func TestRecordToolChangeDeclinedHashIsBlockedAndLaterHashesMuted(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	out, _ := db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
	if _, err := db.DeclineToolChange(ctx, pool, "acct-a", out.ID, "acct-a"); err != nil {
		t.Fatal(err)
	}
	out, err := db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
	if err != nil || !out.Blocked || out.Inserted {
		t.Fatalf("declined hash: %+v err=%v", out, err)
	}
	out, err = db.RecordToolChange(ctx, pool, change("changed", "h1", "h3"), noLimits)
	if err != nil || !out.Inserted {
		t.Fatalf("other hash: %+v err=%v", out, err)
	}
	open, _ := db.ListOpenToolChanges(ctx, pool, "acct-a", "")
	if len(open) != 1 || !open[0].Muted {
		t.Fatalf("later hash must be muted: %+v", open)
	}
	if n, _ := db.CountPendingToolChanges(ctx, pool, "acct-a"); n != 0 {
		t.Fatalf("muted rows must not count for the badge: %d", n)
	}
}

func TestToolChangeHashBlocked(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	out, _ := db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
	if b, err := db.ToolChangeHashBlocked(ctx, pool, pinSess1, pinConn1, "read", "h2"); err != nil || b {
		t.Fatalf("pending hash blocked = %v err=%v", b, err)
	}
	if _, err := db.DeclineToolChange(ctx, pool, "acct-a", out.ID, "acct-a"); err != nil {
		t.Fatal(err)
	}
	if b, err := db.ToolChangeHashBlocked(ctx, pool, pinSess1, pinConn1, "read", "h2"); err != nil || !b {
		t.Fatalf("declined hash blocked = %v err=%v", b, err)
	}
	if b, err := db.ToolChangeHashBlocked(ctx, pool, pinSess1, pinConn1, "read", "h3"); err != nil || b {
		t.Fatalf("other hash blocked = %v err=%v", b, err)
	}
}

func TestRecordToolChangeExpiredHashIsBlocked(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	out, _ := db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
	if _, err := db.ExpireToolChange(ctx, pool, "acct-a", out.ID); err != nil {
		t.Fatal(err)
	}
	out, err := db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
	if err != nil || !out.Blocked {
		t.Fatalf("expired hash: %+v err=%v", out, err)
	}
}

func TestRecordToolChangeCaps(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	lim := db.ToolChangeLimits{Session: 2, Account: 3}
	for i, tool := range []string{"a", "b"} {
		c := change("new", "", "n")
		c.Tool = tool
		if out, err := db.RecordToolChange(ctx, pool, c, lim); err != nil || !out.Inserted {
			t.Fatalf("%d: %+v err=%v", i, out, err)
		}
	}
	c := change("new", "", "n")
	c.Tool = "c"
	if out, err := db.RecordToolChange(ctx, pool, c, lim); err != nil || !out.Capped {
		t.Fatalf("session cap: %+v err=%v", out, err)
	}
	// Another session of the same account: the account cap (3) bites after one more.
	c.SessionID = "aaaaaaaa-2222-4111-8111-111111111111"
	if out, err := db.RecordToolChange(ctx, pool, c, lim); err != nil || !out.Inserted {
		t.Fatalf("other session first: %+v err=%v", out, err)
	}
	c.Tool = "d"
	if out, err := db.RecordToolChange(ctx, pool, c, lim); err != nil || !out.Capped {
		t.Fatalf("account cap: %+v err=%v", out, err)
	}
}

// Past a cap nothing is recorded and nothing is superseded: the tool's old pending row stays.
func TestRecordToolChangeCappedKeepsOldPending(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	lim := db.ToolChangeLimits{Session: 2}
	// Three pending rows, recorded without a cap; then the cap (2) is below what other tools fill.
	for _, tool := range []string{"a", "b", "c"} {
		c := change("changed", "h0", "h1")
		c.Tool = tool
		if out, err := db.RecordToolChange(ctx, pool, c, noLimits); err != nil || !out.Inserted {
			t.Fatalf("%s: %+v err=%v", tool, out, err)
		}
	}
	c := change("changed", "h0", "h2")
	c.Tool = "a"
	if out, err := db.RecordToolChange(ctx, pool, c, lim); err != nil || !out.Capped {
		t.Fatalf("capped: %+v err=%v", out, err)
	}
	open, _ := db.ListOpenToolChanges(ctx, pool, "acct-a", "")
	found := false
	for _, o := range open {
		if o.Tool == "a" {
			found = true
			if o.NewHash != "h1" {
				t.Fatalf("tool a pending hash = %q, want h1", o.NewHash)
			}
		}
	}
	if !found || len(open) != 3 {
		t.Fatalf("the capped record must leave all rows pending: %+v", open)
	}
}

func TestRecordToolChangeRemovedWithHashRefused(t *testing.T) {
	pool := proposalTestPool(t)
	if _, err := db.RecordToolChange(context.Background(), pool, change("removed", "h1", "h2"), noLimits); err == nil {
		t.Fatal("a removed tool with a new hash must be refused")
	}
}

func TestDeclineAndExpireWithEventWriteEvents(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	out, err := db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
	if err != nil || !out.Inserted {
		t.Fatalf("record: %+v err=%v", out, err)
	}
	c, err := db.DeclineToolChangeWithEvent(ctx, pool, "acct-a", out.ID, "acct-a")
	if err != nil || c.State != db.ToolChangeDeclined {
		t.Fatalf("decline: %+v err=%v", c, err)
	}
	evs, _ := db.ListPinEventsForSession(ctx, pool, "acct-a", pinSess1, 10)
	if len(evs) != 1 || evs[0].Action != db.PinDeclined || evs[0].Actor != "acct-a" || evs[0].Tool != "read" || evs[0].OldHash != "h1" || evs[0].NewHash != "h2" {
		t.Fatalf("declined event: %+v", evs)
	}
	if _, err := db.DeclineToolChangeWithEvent(ctx, pool, "acct-a", out.ID, "acct-a"); !errors.Is(err, db.ErrToolChangeState) {
		t.Fatalf("second decline: %v", err)
	}
	if evs, _ = db.ListPinEventsForSession(ctx, pool, "acct-a", pinSess1, 10); len(evs) != 1 {
		t.Fatalf("a failed decline must write no event: %d", len(evs))
	}
	other := change("changed", "g1", "g2")
	other.Tool = "write"
	out, err = db.RecordToolChange(ctx, pool, other, noLimits)
	if err != nil || !out.Inserted {
		t.Fatalf("record other: %+v err=%v", out, err)
	}
	if c, err = db.ExpireToolChangeWithEvent(ctx, pool, "acct-a", out.ID); err != nil || c.State != db.ToolChangeExpired {
		t.Fatalf("expire: %+v err=%v", c, err)
	}
	evs, _ = db.ListPinEventsForSession(ctx, pool, "acct-a", pinSess1, 10)
	if len(evs) != 2 || evs[0].Action != db.PinExpired || evs[0].Actor != "system" || evs[0].Tool != "write" {
		t.Fatalf("expired event: %+v", evs)
	}
	if _, err := db.ExpireToolChangeWithEvent(ctx, pool, "acct-a", "not-a-uuid"); !errors.Is(err, db.ErrToolChangeNotFound) {
		t.Fatalf("bad id: %v", err)
	}
}

// Review Focus 4: two concurrent recordings of different hashes leave exactly one pending row.
func TestRecordToolChangeConcurrentDifferentHashes(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := db.RecordToolChange(ctx, pool, change("changed", "h1", "h"+string(rune('a'+i))), noLimits)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent record: %v", err)
		}
	}
	open, _ := db.ListOpenToolChanges(ctx, pool, "acct-a", "")
	if len(open) != 1 {
		t.Fatalf("pending rows = %d, want exactly 1", len(open))
	}
}

func TestSupersedePendingToolChange(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	if _, err := db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SupersedePendingToolChange(ctx, pool, pinSess1, pinConn1, "read"); err != nil || !ok {
		t.Fatalf("supersede: ok=%v err=%v", ok, err)
	}
	if ok, _ := db.SupersedePendingToolChange(ctx, pool, pinSess1, pinConn1, "read"); ok {
		t.Fatal("nothing left to supersede")
	}
	if n, _ := db.CountPendingToolChanges(ctx, pool, "acct-a"); n != 0 {
		t.Fatalf("pending = %d", n)
	}
	// A removed row is informational: pending, listed, but not counted for the badge.
	if out, err := db.RecordToolChange(ctx, pool, change("removed", "h1", ""), noLimits); err != nil || !out.Inserted {
		t.Fatalf("removed: %+v %v", out, err)
	}
	if n, _ := db.CountPendingToolChanges(ctx, pool, "acct-a"); n != 0 {
		t.Fatalf("removed rows must not count: %d", n)
	}
	if open, _ := db.ListOpenToolChanges(ctx, pool, "acct-a", ""); len(open) != 1 {
		t.Fatalf("removed row must still be listed: %d", len(open))
	}
}

func TestToolChangeTransitionsAreOwnerScopedCAS(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	out, _ := db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
	if _, err := db.AcceptToolChange(ctx, pool, "acct-b", out.ID, "acct-b"); !errors.Is(err, db.ErrToolChangeNotFound) {
		t.Fatalf("another account: %v", err)
	}
	got, err := db.AcceptToolChange(ctx, pool, "acct-a", out.ID, "acct-a")
	if err != nil || got.State != db.ToolChangeAccepted || got.DecidedBy == nil || *got.DecidedBy != "acct-a" {
		t.Fatalf("accept: %+v err=%v", got, err)
	}
	if _, err := db.AcceptToolChange(ctx, pool, "acct-a", out.ID, "acct-a"); !errors.Is(err, db.ErrToolChangeState) {
		t.Fatalf("second accept: %v", err)
	}
	if _, err := db.GetOwnedToolChange(ctx, pool, "acct-a", "not-a-uuid"); !errors.Is(err, db.ErrToolChangeNotFound) {
		t.Fatalf("bad id: %v", err)
	}
}

func TestExpireAndPruneToolChanges(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	out, _ := db.RecordToolChange(ctx, pool, change("changed", "h1", "h2"), noLimits)
	if _, err := pool.Exec(ctx, `UPDATE mcp_tool_changes SET created_at = now() - interval '8 days' WHERE id = $1::uuid`, out.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := db.ExpirePendingToolChanges(ctx, pool, db.ToolChangeExpiry); err != nil || n != 1 {
		t.Fatalf("expired %d err=%v", n, err)
	}
	got, _ := db.GetOwnedToolChange(ctx, pool, "acct-a", out.ID)
	if got.State != db.ToolChangeExpired || got.DecidedAt == nil {
		t.Fatalf("expired row: %+v", got)
	}
	// The expiry is a pin event (Task 5 adds the table; this test runs after it).
	var evs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mcp_pin_events WHERE session_id = $1::uuid AND action = 'expired' AND actor = 'system' AND new_hash = 'h2'`, pinSess1).Scan(&evs); err != nil || evs != 1 {
		t.Fatalf("expired events = %d err=%v", evs, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE mcp_tool_changes SET decided_at = now() - interval '31 days' WHERE id = $1::uuid`, out.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := db.PruneDecidedToolChanges(ctx, pool, 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("pruned %d err=%v", n, err)
	}
}

func TestListDecidedToolChangesPages(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		c := change("changed", "h1", "h2")
		c.Tool = "t" + string(rune('a'+i))
		out, _ := db.RecordToolChange(ctx, pool, c, noLimits)
		if _, err := db.DeclineToolChange(ctx, pool, "acct-a", out.ID, "acct-a"); err != nil {
			t.Fatal(err)
		}
	}
	page1, next, err := db.ListDecidedToolChanges(ctx, pool, "acct-a", "", "", 2)
	if err != nil || len(page1) != 2 || next == "" {
		t.Fatalf("page1: %d next=%q err=%v", len(page1), next, err)
	}
	page2, next2, err := db.ListDecidedToolChanges(ctx, pool, "acct-a", "", next, 2)
	if err != nil || len(page2) != 1 || next2 != "" {
		t.Fatalf("page2: %d next=%q err=%v", len(page2), next2, err)
	}
	if _, _, err := db.ListDecidedToolChanges(ctx, pool, "acct-a", "", "garbage", 2); !errors.Is(err, db.ErrToolChangeCursor) {
		t.Fatalf("bad cursor: %v", err)
	}
}
