package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestFailedClusterJobCleanup(t *testing.T) {
	for _, mode := range []string{"reported_start_failure", "submission_failure", "submission_canceled", "delete_error", "discard_error"} {
		t.Run(mode, func(t *testing.T) {
			f := newOwnedEndFixture(t, "starting", 4)
			kube := &fakeK8s{}
			f.hub.SetJobManager(newTestJobManager(t, kube))
			if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET runtime='cluster' WHERE id=$1`, f.sessionID); err != nil {
				t.Fatal(err)
			}
			reason := "Repository clone failed (deadline)"
			switch mode {
			case "reported_start_failure":
				if err := HandleSessionStateChanged(t.Context(), f.hub, f.pool, protocol.SessionStateChanged{Type: "session_state_changed", SessionID: f.sessionID, Status: "error", Message: &reason}); err != nil {
					t.Fatal(err)
				}
			case "submission_failure":
				f.api.markClusterStartFailed(t.Context(), f.sessionID, reason)
			case "submission_canceled":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				f.api.markClusterStartFailed(ctx, f.sessionID, reason)
			default:
				if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET status='error', ended_at=now(), end_reason='start_failed' WHERE id=$1`, f.sessionID); err != nil {
					t.Fatal(err)
				}
				if mode == "delete_error" {
					if code := f.delete(t); code != 204 {
						t.Fatalf("delete=%d", code)
					}
				} else {
					req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+f.sessionID+"/discard", nil)
					req.SetPathValue("id", f.sessionID)
					req.Header.Set("Authorization", "Bearer "+f.bearer)
					rec := httptest.NewRecorder()
					f.api.handleSessionDiscard(rec, req)
					if rec.Code != 200 {
						t.Fatalf("discard=%d %s", rec.Code, rec.Body.String())
					}
				}
			}
			if mode == "reported_start_failure" || mode == "submission_failure" || mode == "submission_canceled" {
				row, err := db.GetSession(t.Context(), f.pool, f.sessionID)
				if err != nil || row == nil || derefOrEmpty(row.ErrorReason) != reason {
					t.Fatalf("clone cause not persisted: %v", err)
				}
			}
			kube.mu.Lock()
			defer kube.mu.Unlock()
			if len(kube.deleted) != 1 {
				t.Fatalf("orphan job not deleted: %v", kube.deleted)
			}
		})
	}
}

func TestDiscardFailedClusterChildDeletesJob(t *testing.T) {
	cx := newClusterDelegFx(t)
	child := cx.startClusterChildViaRule()
	if _, err := cx.pool.Exec(t.Context(), `UPDATE sessions SET status='error', ended_at=now(), end_reason='start_failed' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	code, out, _ := cx.do("POST", "/api/sessions/"+cx.cparent+"/children/"+child+"/discard", cx.cparentTk, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("discard=%d %v", code, out)
	}
	if !cx.jobDeleted(child) {
		t.Fatal("failed child Job survived discard")
	}
}

func TestFailedStartRetriesJobDeletion(t *testing.T) {
	f := newOwnedEndFixture(t, "starting", 4)
	kube := &fakeK8s{jobDeleteFailures: 1}
	jm := newTestJobManager(t, kube)
	f.hub.SetJobManager(jm)
	waits := 0
	jm.failedCleanupWait = func(context.Context, time.Duration) error { waits++; return nil }
	f.api.markClusterStartFailed(t.Context(), f.sessionID, "Repository clone failed (deadline)")
	kube.mu.Lock()
	defer kube.mu.Unlock()
	if len(kube.deleted) != 1 || kube.jobDeleteAttempts != 2 || waits != 1 {
		t.Fatalf("failed cleanup not retried: deleted=%d attempts=%d waits=%d", len(kube.deleted), kube.jobDeleteAttempts, waits)
	}
}

func TestFailedStartCleanupStopsAfterBoundedRetries(t *testing.T) {
	kube := &fakeK8s{jobDeleteFailures: 10}
	jm := newTestJobManager(t, kube)
	waits := 0
	jm.failedCleanupWait = func(context.Context, time.Duration) error { waits++; return nil }
	if err := jm.cleanupFailedSessionJob(t.Context(), "failed-session"); err == nil {
		t.Fatal("API outage hidden")
	}
	kube.mu.Lock()
	defer kube.mu.Unlock()
	if kube.jobDeleteAttempts != 3 || waits != 2 {
		t.Fatalf("unbounded cleanup: attempts=%d waits=%d", kube.jobDeleteAttempts, waits)
	}
}
