package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestNativeHistoryHardDeletionRetainsReclaimableClaimIdentity(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	const id = "00000000-0000-4000-8000-0000000000e3"
	const account = "00000000-0000-4000-8000-0000000000a1"
	endTestSession(t, p, id, "starting")
	if err := db.SetSessionSpawningAccount(ctx, p, id, account); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionPosture(ctx, p, id, "cluster", false); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionEngine(ctx, p, id, "codex"); err != nil {
		t.Fatal(err)
	}
	h := db.SessionNativeHistory{SessionID: id, AccountID: account, Engine: "codex", ClaimName: "fixture-orphan-claim", ClaimUID: "fixture-orphan-uid"}
	if err := db.BindSessionNativeHistory(ctx, p, h); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSession(ctx, p, id); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetSessionNativeHistory(ctx, p, id)
	if err != nil || got == nil || got.ClaimUID != h.ClaimUID {
		t.Fatalf("hard deletion lost the only reclaimable PVC identity: got=%+v err=%v", got, err)
	}
	ids, err := db.ListExpiredNativeHistory(ctx, p, time.Now().Add(-7*24*time.Hour))
	if err != nil || len(ids) != 0 {
		t.Fatalf("newly detached history reclaimed before retention: %v %v", ids, err)
	}
	removed := 0
	remove := func(bound db.SessionNativeHistory) error {
		removed++
		if bound.SessionID != id || bound.ClaimUID != h.ClaimUID || bound.AccountID != account {
			t.Fatal("orphan cleanup lost immutable owner/claim binding")
		}
		return nil
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	if err = db.ReclaimExpiredNativeHistory(ctx, p, id, cutoff, remove); err != nil || removed != 0 {
		t.Fatalf("new orphan reclaimed: %v calls=%d", err, removed)
	}
	if _, err = p.Exec(ctx, `UPDATE session_native_history SET detached_at=now()-interval '8 days' WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	ids, err = db.ListExpiredNativeHistory(ctx, p, cutoff)
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatalf("expired orphan disappeared from cleanup: %v %v", ids, err)
	}
	if err = db.ReclaimExpiredNativeHistory(ctx, p, id, cutoff, remove); err != nil || removed != 1 {
		t.Fatalf("expired orphan not reclaimed: %v calls=%d", err, removed)
	}
	if err = db.ReclaimExpiredNativeHistory(ctx, p, id, cutoff, remove); err != nil || removed != 1 {
		t.Fatalf("orphan reclaimed twice: %v calls=%d", err, removed)
	}
	// Reusing the deleted row's UUID must never reattach its old volume.
	endTestSession(t, p, id, "starting")
	if err = db.SetSessionSpawningAccount(ctx, p, id, account); err != nil {
		t.Fatal(err)
	}
	if err = db.SetSessionPosture(ctx, p, id, "cluster", false); err != nil {
		t.Fatal(err)
	}
	if err = db.SetSessionEngine(ctx, p, id, "codex"); err != nil {
		t.Fatal(err)
	}
	if err = db.BindSessionNativeHistory(ctx, p, h); !errors.Is(err, db.ErrNativeHistoryBinding) {
		t.Fatalf("deleted identity was resurrected: %v", err)
	}
}
