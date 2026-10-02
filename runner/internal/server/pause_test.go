package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func pauseRequest(t *testing.T, f *reconcileFixture, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	tok := enableBrowserAuth(t, f.api)("session.start")
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/pause", nil)
	req.SetPathValue("id", sessionID)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	f.api.HandlePauseSession(rec, req)
	return rec
}

// Pausing frees the pod but leaves the session resumable: "disconnected", Job deleted, not ended.
func TestPauseFreesThePodAndKeepsTheSessionResumable(t *testing.T) {
	f := newReconcileFixture(t, "idle")
	rec := pauseRequest(t, f, f.sessionID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("pause = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if status, _ := f.status(t); status != "disconnected" {
		t.Errorf("status = %q, want disconnected", status)
	}
	if len(f.fake.deleted) != 1 || f.fake.deleted[0] != f.jobName {
		t.Errorf("deleted jobs = %v, want [%s]", f.fake.deleted, f.jobName)
	}
	row, _ := db.GetSession(context.Background(), f.pool, f.sessionID)
	if row.EndedAt != nil {
		t.Errorf("ended_at = %v, want unset: a paused session has not ended", row.EndedAt)
	}
	// Pausing again is a no-op, not an error.
	if rec := pauseRequest(t, f, f.sessionID); rec.Code != http.StatusNoContent {
		t.Errorf("second pause = %d, want 204", rec.Code)
	}
}

// Only a live cluster session can be paused: an ended one stays ended.
func TestPauseRefusesAnEndedSession(t *testing.T) {
	f := newReconcileFixture(t, "stopped")
	if rec := pauseRequest(t, f, f.sessionID); rec.Code != http.StatusConflict {
		t.Fatalf("pause of a stopped session = %d, want 409", rec.Code)
	}
	if status, _ := f.status(t); status != "stopped" {
		t.Errorf("status = %q, want it left stopped", status)
	}
	if len(f.fake.deleted) != 0 {
		t.Errorf("deleted jobs = %v, want none", f.fake.deleted)
	}
}

func TestPauseRefusesANonClusterSession(t *testing.T) {
	f := newReconcileFixture(t, "idle")
	if err := db.SetSessionPosture(context.Background(), f.pool, f.sessionID, "daemon", false); err != nil {
		t.Fatal(err)
	}
	if rec := pauseRequest(t, f, f.sessionID); rec.Code != http.StatusConflict {
		t.Fatalf("pause of a desktop session = %d, want 409", rec.Code)
	}
	if status, _ := f.status(t); status != "idle" {
		t.Errorf("status = %q, want it left idle", status)
	}
}
