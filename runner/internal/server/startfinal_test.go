package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A cluster pod that reports "error" while its session is starting (a failed
// clone, a missing credential) is ending the session for good: the row gets
// ended_at, the session's tokens are revoked, the completion webhook fires,
// and the start attempt is closed.
func TestClusterStartFailureFromPodIsFinal(t *testing.T) {
	f := newReconcileFixture(t, "starting")
	ctx := context.Background()
	raw, err := db.MintSessionToken(ctx, f.pool, f.sessionID, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	announceClusterStart(ctx, f.api.hub, f.pool, f.sessionID, "org/proj", true, nil)
	msg := "Git authentication failed"
	if err := HandleSessionStateChanged(ctx, f.api.hub, f.pool, protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: f.sessionID, Status: "error", Message: &msg,
	}); err != nil {
		t.Fatal(err)
	}
	status, reason := f.status(t)
	if status != "error" || reason != msg {
		t.Fatalf("status/reason = %q/%q", status, reason)
	}
	row, _ := db.GetSession(ctx, f.pool, f.sessionID)
	if row.EndedAt == nil {
		t.Error("ended_at not set")
	}
	if _, err := db.ValidateBoardToken(ctx, f.pool, raw); err == nil {
		t.Error("session token still valid after the start failed")
	}
	if f.api.hub.starts.isOpen(f.sessionID) {
		t.Error("start attempt still open")
	}
	f.waitForWebhook(t, 1)
}

// Stop on a cluster session still "starting" (no pod has connected, so no
// daemon owns it) deletes the Job and ends the session, instead of 404ing in
// exactly the stuck cases Stop exists for.
func TestDeleteStartingClusterSessionKillsTheJob(t *testing.T) {
	f := newReconcileFixture(t, "starting")
	ctx := context.Background()
	announceClusterStart(ctx, f.api.hub, f.pool, f.sessionID, "org/proj", true, nil)
	tok := enableBrowserAuth(t, f.api)("session.start")
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+f.sessionID, nil)
	req.SetPathValue("id", f.sessionID)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	f.api.HandleDeleteSession(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete starting cluster session = %d %s, want 204", rec.Code, rec.Body.String())
	}
	row, _ := db.GetSession(ctx, f.pool, f.sessionID)
	if row.Status != "stopped" || row.EndedAt == nil {
		t.Fatalf("after delete: status=%s ended_at=%v", row.Status, row.EndedAt)
	}
	f.fake.mu.Lock()
	deleted := append([]string(nil), f.fake.deleted...)
	f.fake.mu.Unlock()
	found := false
	for _, d := range deleted {
		if d == f.jobName || strings.HasPrefix(d, f.jobName) {
			found = true
		}
	}
	if !found {
		t.Fatalf("job %s not deleted (deleted: %v)", f.jobName, deleted)
	}
	if f.api.hub.starts.isOpen(f.sessionID) {
		t.Error("start attempt still open after stop")
	}
	f.waitForWebhook(t, 1)
}

// After a server restart the reconciler picks an in-progress start back up —
// a resume (session "disconnected") as well as a fresh start — and a Job
// that ends cleanly closes the attempt.
func TestReconcilerAdoptsOpenStartsAndClosesEnded(t *testing.T) {
	for _, status := range []string{"starting", "disconnected"} {
		t.Run(status, func(t *testing.T) {
			f := newReconcileFixture(t, status)
			ctx := context.Background()
			announceClusterStart(ctx, f.api.hub, f.pool, f.sessionID, "org/proj", true, nil)
			f.api.hub.starts.close(f.sessionID) // the restart forgets it
			f.api.reconcileClusterJobsOnce(ctx)
			if !f.api.hub.starts.isOpen(f.sessionID) {
				t.Fatal("open start was not re-adopted")
			}
			if status == "starting" {
				f.fake.setJobStatus(f.jobName, map[string]any{"succeeded": 1})
				f.api.reconcileClusterJobsOnce(ctx)
				if f.api.hub.starts.isOpen(f.sessionID) {
					t.Fatal("attempt still open after the Job ended")
				}
			}
		})
	}
}

// A desktop daemon's "error" is not final (reconcile may revive the session),
// so no webhook fires for it here.
func TestDaemonStartErrorIsNotFinalised(t *testing.T) {
	f := newReconcileFixture(t, "starting")
	ctx := context.Background()
	if err := db.SetSessionPosture(ctx, f.pool, f.sessionID, "daemon", false); err != nil {
		t.Fatal(err)
	}
	msg := "boom"
	if err := HandleSessionStateChanged(ctx, f.api.hub, f.pool, protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: f.sessionID, Status: "error", Message: &msg,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if f.hits() != 0 {
		t.Fatalf("webhook deliveries = %d, want 0 for a revivable daemon error", f.hits())
	}
}
