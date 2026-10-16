package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestNativeHistoryBindingIsOwnerEngineAndClaimScoped(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	const id = "00000000-0000-4000-8000-0000000000e1"
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
	h := db.SessionNativeHistory{SessionID: id, AccountID: account, Engine: "codex", ClaimName: "fixture-claim", ClaimUID: "fixture-uid"}
	for _, mutate := range []func(*db.SessionNativeHistory){
		func(h *db.SessionNativeHistory) { h.AccountID = "another-account" },
		func(h *db.SessionNativeHistory) { h.Engine = "claude" },
	} {
		bad := h
		mutate(&bad)
		if err := db.BindSessionNativeHistory(ctx, p, bad); !errors.Is(err, db.ErrNativeHistoryBinding) {
			t.Fatalf("foreign binding accepted: %v", err)
		}
	}
	if err := db.BindSessionNativeHistory(ctx, p, h); err != nil {
		t.Fatal(err)
	}
	if err := db.BindSessionNativeHistory(ctx, p, h); err != nil {
		t.Fatalf("matching retry refused: %v", err)
	}
	for _, mutate := range []func(*db.SessionNativeHistory){func(h *db.SessionNativeHistory) { h.ClaimUID = "replacement-uid" }, func(h *db.SessionNativeHistory) { h.ClaimName = "replacement-claim" }} {
		bad := h
		mutate(&bad)
		if err := db.BindSessionNativeHistory(ctx, p, bad); !errors.Is(err, db.ErrNativeHistoryBinding) {
			t.Fatalf("history identity replaced: %v", err)
		}
	}
	got, err := db.GetSessionNativeHistory(ctx, p, id)
	if err != nil || got == nil || got.ClaimUID != h.ClaimUID || got.AccountID != account {
		t.Fatalf("retained binding changed: %+v err=%v", got, err)
	}
	// A separate lock connection leaves the single-connection data pool usable.
	if err = db.WithSessionNativeHistoryLock(ctx, p, id, func() error { _, e := db.GetSessionNativeHistory(ctx, p, id); return e }); err != nil {
		t.Fatal(err)
	}
}

func TestNativeHistoryCleanupRequiresTrueEndAndCannotRemoveActiveSession(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	const id = "00000000-0000-4000-8000-0000000000e2"
	const account = "00000000-0000-4000-8000-0000000000a1"
	endTestSession(t, p, id, "disconnected")
	if err := db.SetSessionSpawningAccount(ctx, p, id, account); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionPosture(ctx, p, id, "cluster", false); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionEngine(ctx, p, id, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindSessionNativeHistory(ctx, p, db.SessionNativeHistory{SessionID: id, AccountID: account, Engine: "codex", ClaimName: "fixture-claim", ClaimUID: "fixture-uid"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if _, err := p.Exec(ctx, `UPDATE sessions SET status_changed_at=$2 WHERE id=$1`, id, old); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	ids, err := db.ListExpiredNativeHistory(ctx, p, cutoff)
	if err != nil || len(ids) != 0 {
		t.Fatalf("paused data offered for cleanup: %v %v", ids, err)
	}
	if _, err = p.Exec(ctx, `UPDATE sessions SET status='ended',ended_at=$2 WHERE id=$1`, id, old); err != nil {
		t.Fatal(err)
	}
	ids, err = db.ListExpiredNativeHistory(ctx, p, cutoff)
	if err != nil || len(ids) != 1 {
		t.Fatalf("terminal cleanup: %v %v", ids, err)
	}
	// The state can change between the scan and locking the actual claim.
	if _, err = p.Exec(ctx, `UPDATE sessions SET status='running',ended_at=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	calls := 0
	remove := func(h db.SessionNativeHistory) error {
		calls++
		if h.ClaimUID != "fixture-uid" {
			t.Error("wrong immutable claim")
		}
		return nil
	}
	if err = db.ReclaimExpiredNativeHistory(ctx, p, id, cutoff, remove); err != nil || calls != 0 {
		t.Fatalf("active cleanup: %v calls=%d", err, calls)
	}
	if _, err = p.Exec(ctx, `UPDATE sessions SET status='ended',ended_at=$2 WHERE id=$1`, id, old); err != nil {
		t.Fatal(err)
	}
	if err = db.ReclaimExpiredNativeHistory(ctx, p, id, cutoff, remove); err != nil || calls != 1 {
		t.Fatalf("terminal cleanup: %v calls=%d", err, calls)
	}
	if err = db.ReclaimExpiredNativeHistory(ctx, p, id, cutoff, remove); err != nil || calls != 1 {
		t.Fatalf("repeated cleanup: %v calls=%d", err, calls)
	}
	h, err := db.GetSessionNativeHistory(ctx, p, id)
	if err != nil || h == nil {
		t.Fatal("cleanup erased expected-history tombstone")
	}
}
