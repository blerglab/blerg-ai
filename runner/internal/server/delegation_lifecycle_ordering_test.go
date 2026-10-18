package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDelegationRealEndStillCleansOldChildrenAfterParentRevives(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	err := fx.api.withDelegationLock(t.Context(), "owner-1", func() error {
		// The account fence keeps real-end cleanup behind a native same-ID resume.
		HandleSessionEnded(t.Context(), fx.hub, fx.pool, protocol.SessionEnded{SessionID: fx.parent})
		HandleSessionStarted(t.Context(), fx.hub, fx.pool, fx.dc.ID, protocol.SessionStarted{SessionID: fx.parent, Kind: "agent"})
		if db.TerminallyEnded(fx.row(fx.parent)) {
			t.Fatal("control: parent could not resume")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitLifecycle(t, func() bool {
		fx.api.delegationLife.mu.Lock()
		defer fx.api.delegationLife.mu.Unlock()
		return fx.api.delegationLife.pending[fx.parent] == nil
	})
	if !db.TerminallyEnded(fx.row(child)) {
		t.Fatal("real terminal end was erased by same-ID parent revival before cleanup")
	}
	if derefOrEmpty(fx.row(child).EndReason) != db.EndReasonParentEnded {
		t.Fatal("old child lost parent-ended attribution")
	}
}

func TestDelegationDelayedPauseDoesNotExpirePostResumeRequest(t *testing.T) {
	fx := newDelegFx(t)
	first := fx.ask(fx.parent, fx.parentTok, codexBody())
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	cfg := fx.pool.Config()
	cfg.PrepareConn = func(ctx context.Context, _ *pgx.Conn) (bool, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	gated, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gated.Close()
	defer unblock()
	// A distinct API models the delayed pause worker, while the ordinary fixture
	// can process a resumed parent's new request on the same database.
	pausedAPI := &API{hub: NewHub(), dbPool: gated, delegationWaiters: &delegationWaiters{}}
	pausedAPI.onParentPaused(fx.parent)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		unblock()
		t.Fatal("pause worker did not reach its controlled database checkout")
	}
	laterBody := codexBody()
	laterBody["prompt"] = "new work after the parent resumed"
	second := fx.ask(fx.parent, fx.parentTok, laterBody)
	unblock()
	awaitLifecycle(t, func() bool { return fx.request(first).Status == "expired" })
	awaitLifecycle(t, func() bool {
		pausedAPI.delegationLife.mu.Lock()
		defer pausedAPI.delegationLife.mu.Unlock()
		return pausedAPI.delegationLife.pending[fx.parent] == nil
	})
	if fx.request(second).Status != "pending" {
		t.Fatal("delayed old pause expired a post-resume request")
	}
}

func TestDelegationStalePrecreateCannotCrossParentEndAndRevive(t *testing.T) {
	fx := newDelegFx(t)
	prior := fx.row(fx.parent)
	var freshChild string
	err := fx.api.withDelegationLock(t.Context(), "owner-1", func() error {
		HandleSessionEnded(t.Context(), fx.hub, fx.pool, protocol.SessionEnded{SessionID: fx.parent})
		HandleSessionStarted(t.Context(), fx.hub, fx.pool, fx.dc.ID, protocol.SessionStarted{SessionID: fx.parent, Kind: "agent"})
		cs := childStart{Engine: "codex", Runtime: "docker", Repo: "app", Prompt: "work admitted before the parent ended"}
		if _, problem := fx.api.startChild(t.Context(), prior, cs, "rule:"+newUUID(), newUUID()); problem == nil {
			t.Fatal("a stale admitted start created a child in the resumed parent's lifetime")
		}
		if got := fx.count(`SELECT count(*) FROM sessions WHERE parent_session_id=$1`, fx.parent); got != 0 {
			t.Fatal("refused stale start left a child record")
		}
		// A truly new start must still work in the resumed parent lifetime.
		cs.Prompt = "work admitted after the parent resumed"
		response, problem := fx.api.startChild(t.Context(), fx.row(fx.parent), cs, "rule:"+newUUID(), newUUID())
		if problem != nil {
			t.Fatalf("fresh parent-lifetime start refused: %v", problem)
		}
		freshChild = response.SessionID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitLifecycle(t, func() bool {
		fx.api.delegationLife.mu.Lock()
		defer fx.api.delegationLife.mu.Unlock()
		return fx.api.delegationLife.pending[fx.parent] == nil
	})
	if db.TerminallyEnded(fx.row(freshChild)) {
		t.Fatal("old cleanup stopped a child from the resumed lifetime")
	}
}

func TestDelegationCoalescedTerminalAndNewPauseExpireNewRequest(t *testing.T) {
	fx := newDelegFx(t)
	oldRequest := fx.ask(fx.parent, fx.parentTok, codexBody())
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), fx.pool, fx.parent, "ended", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	cfg := fx.pool.Config()
	cfg.PrepareConn = func(ctx context.Context, _ *pgx.Conn) (bool, error) {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	gated, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gated.Close()
	defer unblock()
	delayed := &API{hub: NewHub(), dbPool: gated, delegationWaiters: &delegationWaiters{}}
	delayed.onParentTerminalEnd(fx.parent)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal worker never reached its controlled checkout")
	}
	if err := db.ReviveSessionAs(t.Context(), fx.pool, fx.parent, fx.dc.ID, "idle", "/repos/app", "app", "Parent", "", db.SessionOrigin{}); err != nil {
		t.Fatal(err)
	}
	// The resumed lifetime's child and grant survive both the old end and new pause.
	freshChild, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	freshGrant := fx.grant(fx.parent, "claude", "docker", "app", false)
	body := codexBody()
	body["prompt"] = "request from resumed turn, before its new pause"
	newRequest := fx.ask(fx.parent, fx.parentTok, body)
	delayed.onParentPaused(fx.parent)
	unblock()
	awaitLifecycle(t, func() bool {
		delayed.delegationLife.mu.Lock()
		defer delayed.delegationLife.mu.Unlock()
		return delayed.delegationLife.pending[fx.parent] == nil
	})
	for _, id := range []string{oldRequest, newRequest} {
		if fx.request(id).Status != "expired" {
			t.Errorf("coalesced lifecycle left request pending: %s", id)
		}
	}
	if db.TerminallyEnded(fx.row(freshChild)) {
		t.Error("coalesced pause incorrectly ended a new-lifetime child")
	}
	if n := fx.count(`SELECT count(*) FROM delegation_grants WHERE id=$1 AND revoked_at IS NULL`, freshGrant); n != 1 {
		t.Error("coalesced pause revoked a new-lifetime grant")
	}
}
