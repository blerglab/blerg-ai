package db_test

import (
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestInboxWorkspaceScope(t *testing.T) {
	pool := endTestPool(t)
	ctx := t.Context()
	for _, sid := range []string{"00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000b1"} {
		endTestSession(t, pool, sid, "running")
		if _, _, err := db.InsertInboxItem(ctx, pool, db.InboxItem{ID: sid, AccountID: "owner", SessionID: sid, Subject: sid, Body: "body", Kind: "summary"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, sid := range []string{"00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000b1"} {
		items, err := db.ListInboxItems(ctx, pool, "owner", "", sid)
		if err != nil || len(items) != 1 || items[0].SessionID != sid {
			t.Errorf("list %s: %v %v", sid, items, err)
		}
		count, err := db.CountInboxUnread(ctx, pool, "owner", sid)
		if err != nil || count != 1 {
			t.Errorf("count: %d %v", count, err)
		}
		foreign := "00000000-0000-4000-8000-0000000000a1"
		if sid == foreign {
			foreign = "00000000-0000-4000-8000-0000000000b1"
		}
		page, err := db.ListInboxItems(ctx, pool, "owner", foreign, sid)
		if err != nil || len(page) != 0 {
			t.Errorf("foreign cursor: %v %v", page, err)
		}
		item, err := db.GetInboxItem(ctx, pool, "owner", foreign, sid)
		if err != nil || item != nil {
			t.Errorf("foreign get: %v %v", item, err)
		}
		item, err = db.SetInboxRead(ctx, pool, "owner", foreign, true, sid)
		if err != nil || item != nil {
			t.Errorf("foreign read: %v %v", item, err)
		}
		deleted, err := db.DeleteInboxItem(ctx, pool, "owner", foreign, sid)
		if err != nil || deleted {
			t.Errorf("foreign delete: %v %v", deleted, err)
		}
	}
	for _, sid := range []string{"00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000b1"} {
		item, err := db.GetInboxItem(ctx, pool, "owner", sid, sid)
		if err != nil || item == nil || item.Read {
			t.Fatalf("own get: %v %v", item, err)
		}
		item, err = db.SetInboxRead(ctx, pool, "owner", sid, true, sid)
		if err != nil || item == nil || !item.Read {
			t.Fatalf("own read: %v %v", item, err)
		}
		count, err := db.CountInboxUnread(ctx, pool, "owner", sid)
		if err != nil || count != 0 {
			t.Errorf("read count: %d %v", count, err)
		}
		deleted, err := db.DeleteInboxItem(ctx, pool, "owner", sid, sid)
		if err != nil || !deleted {
			t.Errorf("own delete: %v %v", deleted, err)
		}
	}
}
