package server

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParentDiscardPendingRefusesTerminalHeldQuestionAndModelControls(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	ids := []string{fx.parent, child}
	asks := map[string][]string{}
	for _, id := range ids {
		fx.hub.SetSessionOwner(id, fx.dc.ID)
		for _, kind := range []string{"ask", "note"} {
			ask, err := db.CreateMessage(t.Context(), fx.pool, id, kind, "fixture question")
			if err != nil {
				t.Fatal(err)
			}
			asks[id] = append(asks[id], ask)
		}
	}
	for len(fx.dc.send) < cap(fx.dc.send) {
		fx.dc.send <- []byte(`{"type":"fixture"}`)
	}
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 503 {
		t.Fatal(code)
	}
	for len(fx.dc.send) > 0 {
		<-fx.dc.send
	}
	bc := &BrowserConn{ID: "fixture-browser", AccountID: "owner-1"}
	for _, id := range ids {
		if err := handleBrowserTerminalInput(t.Context(), fx.hub, bc, fx.pool, protocol.SendInput{SessionID: id, Data: "echo fixture\n"}); !errors.Is(err, db.ErrDelegationEnded) {
			t.Fatalf("terminalinput=%v", err)
		}
		if p := handleReleaseHeldPrompt(t.Context(), fx.hub, bc, fx.pool, protocol.ReleaseHeldPrompt{SessionID: id}); p == nil || p.Status != 409 {
			t.Fatalf("heldprompt=%v", p)
		}
		if _, p := fx.api.SetSessionModel(t.Context(), RunnerPrincipal{Kind: "human", Sub: "owner-1", Sid: testSID}, id, "fixture", ""); p == nil || p.Status != 409 {
			t.Fatalf("modelcontrol=%v", p)
		}
		for _, ask := range asks[id] {
			waiter := fx.api.waiters.channel(ask)
			r := httptest.NewRequest("POST", "/api/messages/"+ask+"/reply", strings.NewReader(`{"answer":"continue"}`))
			r.SetPathValue("id", ask)
			r.Header.Set("Authorization", "Bearer "+fx.human("owner-1"))
			w := httptest.NewRecorder()
			fx.api.HandlePostMessageReply(w, r)
			if w.Code != 409 {
				t.Fatalf("questionreply=%d", w.Code)
			}
			row, err := db.GetMessage(t.Context(), fx.pool, ask)
			if err != nil || row.Answer != nil {
				t.Fatal("discarded question answered")
			}
			select {
			case <-waiter:
				t.Fatal("discarded question waiter woke")
			default:
			}
		}
	}
	if len(fx.dc.send) != 0 {
		t.Fatal("stale controls reached runtime")
	}
}
func TestParentDiscardAttemptsEveryReachableMemberBeforeRetry(t *testing.T) {
	fx := newDelegFx(t)
	offline, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	online, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	fx.hub.SetSessionOwner(online, fx.dc.ID)
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 503 {
		t.Fatal(code)
	}
	if fx.row(offline).ArchivedAt != nil || fx.row(fx.parent).ArchivedAt != nil {
		t.Fatal("incomplete tree archived")
	}
	if fx.row(online).ArchivedAt == nil || fx.row(fx.parent).EndedAt == nil {
		t.Fatal("reachable members were not stopped")
	}
	fx.hub.SetSessionOwner(offline, fx.dc.ID)
	code, _, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 200 {
		t.Fatalf("retry=%d", code)
	}
}
func TestParentDiscardWaitsForKeptInputAndNativeRecovery(t *testing.T) {
	for _, mode := range []string{"kept-input", "native-recovery", "root-only-recovery"} {
		t.Run(mode, func(t *testing.T) {
			fx := newDelegFx(t)
			child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
			fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
			fx.hub.SetSessionOwner(child, fx.dc.ID)
			if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, child); err != nil {
				t.Fatal(err)
			}
			entered, release, guardDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			callback := func() error { close(entered); <-release; return nil }
			go func() {
				if mode != "kept-input" {
					ids := []string{fx.parent, child}
					if mode == "root-only-recovery" {
						ids = []string{fx.parent}
					}
					guardDone <- db.WithDelegationRecoveryGuard(t.Context(), fx.pool, ids, func([]string) error { return callback() })
				} else {
					guardDone <- withParentInputGuard(t.Context(), fx.pool, child, callback)
				}
			}()
			select {
			case <-entered:
			case <-time.After(15 * time.Second):
				t.Fatal("guard did not enter")
			}
			done := make(chan int, 1)
			go func() {
				code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
				done <- code
			}()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			awaitLifecycle(t, func() bool {
				var waiting bool
				err := fx.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR UPDATE%')`).Scan(&waiting)
				return err == nil && waiting
			})
			if fx.row(fx.parent).DiscardRequestedAt != nil {
				t.Fatal("discard crossed admitted work")
			}
			close(release)
			if err := <-guardDone; err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != 200 {
					t.Fatal(code)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("discard remained blocked")
			}
			if err := withParentInputGuard(context.Background(), fx.pool, child, func() error { t.Error("kept child resumed"); return nil }); !errors.Is(err, db.ErrDelegationEnded) {
				t.Fatal(err)
			}
		})
	}
}

func TestParentDiscardRestartHelloRetainsCleanupTargetWithoutRevival(t *testing.T) {
	fx := newDelegFx(t)
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 503 {
		t.Fatal(code)
	}
	// The new server hub knows the authenticated daemon, but no session owner yet.
	for len(fx.dc.send) < cap(fx.dc.send) {
		fx.dc.send <- []byte(`{"type":"fixture"}`)
	}
	HandleSessionStarted(t.Context(), fx.hub, fx.pool, fx.dc.ID, protocol.SessionStarted{SessionID: fx.parent, ProjectPath: "/fixture", Kind: "agent"})
	if fx.row(fx.parent).EndedAt != nil || fx.row(fx.parent).ArchivedAt != nil {
		t.Fatal("hello fabricated actual stop evidence")
	}
	if fx.hub.FindDaemonForSession(fx.parent) != fx.dc {
		t.Fatal("verified reconnect cleanup target was lost")
	}
	code, _, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 503 || fx.row(fx.parent).ArchivedAt != nil {
		t.Fatal("queuefull reconnect falsely archived")
	}
	for len(fx.dc.send) > 0 {
		<-fx.dc.send
	}
	code, _, _ = fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 200 {
		t.Fatalf("reconnectretry=%d", code)
	}
}
func TestParentDiscardDoesNotChangeOrdinaryStopKeepRunning(t *testing.T) {
	fx := newDelegFx(t)
	fx.hub.SetSessionOwner(fx.parent, fx.dc.ID)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if _, p := fx.api.Stop(t.Context(), RunnerPrincipal{Kind: "human", Sub: "owner-1", Sid: testSID}, fx.parent); p != nil {
		t.Fatal(p)
	}
	if row := fx.row(child); row.EndedAt != nil || row.DiscardRequestedAt != nil || row.ArchivedAt != nil {
		t.Fatal("ordinary stop discarded kept child")
	}
	if row := fx.row(fx.parent); row.DiscardRequestedAt != nil || row.ArchivedAt != nil {
		t.Fatal("ordinary stop became discard")
	}
}

func TestParentDiscardOldChildRouteCannotBypassStrictDelivery(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	code, _, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/discard", fx.human("owner-1"), nil, nil)
	if code != 503 {
		t.Fatal(code)
	}
	at := time.Now()
	if err := db.EndSessionStatus(t.Context(), fx.pool, child, "ended", &at, systemEnd(db.EndReasonParentEnded)); err != nil {
		t.Fatal(err)
	}
	code, response, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.human("owner-1"), nil, nil)
	if asString(response["error"]) != "this child is part of a pending parent discard; retry discarding the parent" {
		t.Fatal("missing parent retry guidance")
	}
	if code != 409 || fx.row(child).ArchivedAt != nil {
		t.Fatal("old child route bypassed parent strict delivery")
	}
}
