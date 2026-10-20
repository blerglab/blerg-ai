package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestDiscardSecurityStrictStopRefusesMissingDeliveryOwner(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if err := fx.api.stopSessionWithDelivery(t.Context(), child, systemEnd(db.EndReasonStoppedByUser), true); err == nil {
		t.Fatal("strict stop marked a live child ended without a daemon delivery owner")
	}
	row := fx.row(child)
	if row.EndedAt != nil || row.ArchivedAt != nil {
		t.Fatal("failed stop mutated lifecycle")
	}
}

func TestDiscardSecurityCredentialClassesAndForeignChildOwner(t *testing.T) {
	fx := newDelegFx(t)
	child, childToken := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	_, siblingToken := fx.session(sessOpts{owner: "owner-1"})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	noSid := fx.coreToken(identity.Claims{Kind: "human", Sub: "owner-1", Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}})
	agentBrowser := fx.coreToken(identity.Claims{Kind: agentPrincipalKind, Sub: "agent-owner", OnBehalfOf: "owner-1", Sid: testSID, Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}})
	for _, token := range []string{"", childToken, siblingToken, delegDaemonToken, fx.agentToken("owner-1"), noSid, agentBrowser} {
		code, _, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), token, nil, nil)
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("unauthorized credential accepted or leaked target: %d", code)
		}
	}
	foreignChild, _ := fx.session(sessOpts{owner: "owner-2", parent: fx.parent})
	for _, token := range []string{fx.parentTok, fx.human("owner-1")} {
		code, _, _ := fx.do("POST", childRoute(fx.parent, foreignChild, "/discard"), token, nil, nil)
		if code != http.StatusNotFound {
			t.Fatalf("parent authority crossed child ownership: %d", code)
		}
	}
	if row := fx.row(child); row.EndedAt != nil || row.ArchivedAt != nil {
		t.Fatal("unauthorized caller stopped or archived child")
	}
	fx.noSpawn()
}

func TestDiscardSecurityPrivateArchiveEventsStayWithOwner(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if err := fx.api.MarkPrivate(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	owner := &BrowserConn{ID: "discard-owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	other := &BrowserConn{ID: "discard-other", AccountID: "owner-2", send: make(chan []byte, 32)}
	fx.hub.RegisterBrowser(owner)
	fx.hub.RegisterBrowser(other)
	code, _, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.human("owner-1"), nil, nil)
	if code != http.StatusOK {
		t.Fatalf("owner discard: %d", code)
	}
	sawArchive := false
	for len(owner.send) > 0 {
		raw := <-owner.send
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event["type"] == "session_archived" {
			sawArchive = true
		}
	}
	if !sawArchive {
		t.Fatal("owner did not receive durable archive notification")
	}
	for len(other.send) > 0 {
		if raw := <-other.send; strings.Contains(string(raw), child) {
			t.Fatal("private child identity escaped through archive or children-change broadcast")
		}
	}
}

func TestDiscardSecurityHumanCanArchiveAfterParentEndedButDelegateCannot(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "stopped"})
	if err := db.EndSessionStatus(t.Context(), fx.pool, fx.parent, "ended", ptrTime(time.Now()), systemEnd(db.EndReasonStoppedByUser)); err != nil {
		t.Fatal(err)
	}
	code, _, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.parentTok, nil, nil)
	if code != http.StatusConflict {
		t.Fatalf("ended parent retained delegate authority: %d", code)
	}
	code, first, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.human("owner-1"), nil, nil)
	if code != http.StatusOK {
		t.Fatalf("owner could not discard offline stopped worker: %d", code)
	}
	code, second, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.human("owner-1"), nil, nil)
	if code != http.StatusOK || first["archived_at"] != second["archived_at"] {
		t.Fatalf("idempotent discard changed timestamp: %d", code)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSessionStatusGuarded(t.Context(), fx.pool, child, "running", nil); !errors.Is(err, db.ErrDelegationEnded) {
		t.Fatalf("kept archived child revived by native status: %v", err)
	}
}

func TestDiscardSecurityConcurrentMessageCannotRunAfterArchive(t *testing.T) {
	fx := newDelegFx(t)
	for range 8 {
		child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
		fx.hub.SetSessionOwner(child, fx.dc.ID)
		var wg sync.WaitGroup
		start := make(chan struct{})
		var discardCode, messageCode int
		wg.Go(func() {
			<-start
			discardCode, _, _ = fx.do("POST", childRoute(fx.parent, child, "/discard"), fx.parentTok, nil, nil)
		})
		wg.Go(func() {
			<-start
			messageCode, _, _ = fx.do("POST", childRoute(fx.parent, child, "/message"), fx.parentTok, map[string]any{"text": "race message"}, nil)
		})
		close(start)
		wg.Wait()
		if discardCode != http.StatusOK || (messageCode != http.StatusAccepted && messageCode != http.StatusConflict) {
			t.Fatalf("race result: discard=%d message=%d", discardCode, messageCode)
		}
		if row := fx.row(child); row.ArchivedAt == nil || row.EndedAt == nil || !db.TerminallyEnded(row) {
			t.Fatal("race left hidden live worker")
		}
		killed := false
		for len(fx.dc.send) > 0 {
			raw := <-fx.dc.send
			var frame struct {
				Type      string `json:"type"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.SessionID != child {
				continue
			}
			if frame.Type == "kill_session" {
				killed = true
			}
			if killed && frame.Type == "agent_user_message" {
				t.Fatal("input enqueued after the discard stop")
			}
		}
		if !killed {
			t.Fatal("discard did not enqueue a stop")
		}
	}
}

// Block the cancellation write after initial route authorization but before the
// fenced callback. No production hooks or wall-clock guess about admission.
func TestDiscardSecurityRechecksParentAfterCancellationWait(t *testing.T) {
	for _, change := range []string{"owner changed", "delegate parent ended"} {
		t.Run(change, func(t *testing.T) {
			fx := newDelegFx(t)
			child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
			fx.hub.SetSessionOwner(child, fx.dc.ID)
			if err := db.CancelLimitResume(t.Context(), fx.pool, child); err != nil {
				t.Fatal(err)
			}
			blocker, err := fx.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if _, err := blocker.Exec(t.Context(), `SELECT session_id FROM session_limit_resume WHERE session_id=$1 FOR UPDATE`, child); err != nil {
				t.Fatal(err)
			}
			token := fx.human("owner-1")
			if change == "delegate parent ended" {
				token = fx.parentTok
			}
			done := make(chan int, 1)
			go func() {
				code, _, _ := fx.do("POST", childRoute(fx.parent, child, "/discard"), token, nil, nil)
				done <- code
			}()
			until := time.Now().Add(15 * time.Second)
			for {
				var waiting bool
				if err := fx.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%INSERT INTO session_limit_resume%')`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(until) {
					t.Fatal("discard did not reach the cancellation fence")
				}
				time.Sleep(time.Millisecond)
			}
			if change == "owner changed" {
				if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='owner-next' WHERE id=$1`, fx.parent); err != nil {
					t.Fatal(err)
				}
			} else if err := db.EndSessionStatus(t.Context(), fx.pool, fx.parent, "ended", ptrTime(time.Now()), systemEnd(db.EndReasonStoppedByUser)); err != nil {
				t.Fatal(err)
			}
			if err := blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != http.StatusServiceUnavailable {
					t.Fatalf("stale parent authorization discarded a child: %d", code)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("discard did not finish")
			}
			if row := fx.row(child); row.ArchivedAt != nil || row.EndedAt != nil {
				t.Fatal("stale parent authority mutated the worker")
			}
		})
	}
}
