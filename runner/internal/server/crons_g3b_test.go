package server

// Round-2 fixes (G3b) in the crons API and lifecycle: a delete racing a renewal revokes the new
// token too, the revocation outbox backs off and drops a poisoned row, and an open manual run
// makes a scheduled slot skip.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// MINOR 29 (d): a renewal lands between the delete handler's read of the cron and its delete. The
// token the renewal minted must be revoked as well, not orphaned for its lifetime.
func TestDeleteRacingARenewRevokesTheNewTokenToo(t *testing.T) {
	fx := newCronsFx(t)
	ctx := context.Background()
	_, row := fx.create(nil)
	const renewed = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0d9"
	fx.core.live[renewed] = true
	var once bool
	fx.core.onRevoke = func() {
		if once {
			return
		}
		once = true
		// The renewal's swap, after the handler read the row and revoked its token, before the delete.
		if _, err := fx.pool.Exec(ctx, `UPDATE crons SET token_id = $2 WHERE id = $1`, row.ID, renewed); err != nil {
			t.Error(err)
		}
	}
	if rec := fx.call(http.MethodDelete, "/api/crons/"+row.ID, fx.human(), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	got := fx.core.revokedList()
	if !slices.Contains(got, mcpAcct+":"+row.TokenID) || !slices.Contains(got, mcpAcct+":"+renewed) {
		t.Fatalf("revoked = %v, want both the old and the renewed token", got)
	}
	if owed, _ := db.ListTokenRevocations(ctx, fx.pool, 10); len(owed) != 0 {
		t.Errorf("still owed after the delete: %+v", owed)
	}
	if fx.core.live[renewed] {
		t.Error("the renewed token is still live at core")
	}
}

// When core cannot take the new token's revocation at delete time, it stays owed.
func TestDeleteKeepsTheCurrentTokenOwedWhenCoreIsDown(t *testing.T) {
	fx := newCronsFx(t)
	ctx := context.Background()
	_, row := fx.create(nil)
	const renewed = "c0c0c0c0-c0c0-4c0c-8c0c-c0c0c0c0c0d8"
	fx.core.live[renewed] = true
	var calls int
	fx.core.onRevoke = func() {
		calls++
		if calls == 1 { // the handler's own revoke of the old token goes through; the renewal lands
			if _, err := fx.pool.Exec(ctx, `UPDATE crons SET token_id = $2 WHERE id = $1`, row.ID, renewed); err != nil {
				t.Error(err)
			}
			return
		}
		fx.core.mu.Lock()
		fx.core.revokeErr = errors.New("core is down")
		fx.core.mu.Unlock()
	}
	if rec := fx.call(http.MethodDelete, "/api/crons/"+row.ID, fx.human(), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	owed, _ := db.ListTokenRevocations(ctx, fx.pool, 10)
	if len(owed) != 1 || owed[0].TokenID != renewed {
		t.Fatalf("owed = %+v, want the renewed token kept for a retry", owed)
	}
}

// MINOR 29 (c): a revocation core keeps refusing backs off, stops blocking the rest, and is dropped.
func TestRevocationRetryBacksOffAndDropsAPoisonedRow(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	cf.svc.cfg.Now = cf.clock.Now
	cf.core.live["poison"], cf.core.live["fine"] = true, true
	for _, id := range []string{"poison", "fine"} {
		if err := db.EnqueueTokenRevocation(ctx, cf.pool, mcpAcct, id); err != nil {
			t.Fatal(err)
		}
	}
	cf.core.revokeFor = map[string]error{"poison": errors.New("core refuses this one")}
	if n := cf.svc.RetryRevocations(ctx); n != 1 {
		t.Fatalf("first pass cleared %d, want the healthy row only", n)
	}
	if n := cf.svc.RetryRevocations(ctx); n != 0 {
		t.Fatalf("an immediate second pass retried the poisoned row (%d cleared): it must back off", n)
	}
	for range maxRevocationAttempts {
		cf.clock.Advance(2 * time.Hour) // past any backoff
		cf.svc.RetryRevocations(ctx)
	}
	if owed, _ := db.ListTokenRevocations(ctx, cf.pool, 10); len(owed) != 0 {
		t.Errorf("the poisoned row was not dropped after %d attempts: %+v", maxRevocationAttempts, owed)
	}
}
