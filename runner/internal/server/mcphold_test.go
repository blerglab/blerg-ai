package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestMCPHoldReleaseOwnerOnlyAndIdempotent(t *testing.T) {
	a, dc, id := controlFixture(t)
	msg := protocol.ReleaseHeldPrompt{SessionID: id}
	for _, account := range []string{"foreign", ""} {
		problem := handleReleaseHeldPrompt(t.Context(), a.hub, &BrowserConn{AccountID: account}, a.dbPool, msg)
		if problem == nil || problem.Status != http.StatusForbidden || len(dc.send) != 0 {
			t.Fatalf("nonowner %q: %v queued=%d", account, problem, len(dc.send))
		}
	}
	for range 2 {
		if problem := handleReleaseHeldPrompt(t.Context(), a.hub, &BrowserConn{AccountID: "owner"}, a.dbPool, msg); problem != nil {
			t.Fatal(problem)
		}
		var got protocol.ReleaseHeldPrompt
		if err := json.Unmarshal(<-dc.send, &got); err != nil || got.Type != "release_held_prompt" || got.SessionID != id {
			t.Fatalf("release %+v err=%v", got, err)
		}
	}
	dc.BoundSessionID = "00000000-0000-4000-8000-00000000abcd"
	if problem := handleReleaseHeldPrompt(t.Context(), a.hub, &BrowserConn{AccountID: "owner"}, a.dbPool, msg); problem == nil || problem.Status != http.StatusConflict || len(dc.send) != 0 {
		t.Fatalf("wrong runtime: %v", problem)
	}
	dc.BoundSessionID = ""
	if err := db.UpdateSessionStatus(t.Context(), a.dbPool, id, "ended", nil); err != nil {
		t.Fatal(err)
	}
	if problem := handleReleaseHeldPrompt(t.Context(), a.hub, &BrowserConn{AccountID: "owner"}, a.dbPool, msg); problem == nil || problem.Status != http.StatusConflict || len(dc.send) != 0 {
		t.Fatalf("terminal session: %v", problem)
	}
	if err := db.UpdateSessionStatus(t.Context(), a.dbPool, id, "idle", nil); err != nil {
		t.Fatal(err)
	}
	a.hub.mu.Lock()
	delete(a.hub.daemons, dc.ID)
	a.hub.mu.Unlock()
	if problem := handleReleaseHeldPrompt(t.Context(), a.hub, &BrowserConn{AccountID: "owner"}, a.dbPool, msg); problem == nil || problem.Status != http.StatusConflict {
		t.Fatalf("missing daemon: %v", problem)
	}
	if _, err := a.dbPool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if problem := handleReleaseHeldPrompt(t.Context(), a.hub, &BrowserConn{AccountID: "owner"}, a.dbPool, msg); problem == nil || problem.Status != http.StatusForbidden {
		t.Fatalf("ownerless session: %v", problem)
	}
}

func TestMCPHoldReleaseNoDatabaseFailsClosed(t *testing.T) {
	if problem := handleReleaseHeldPrompt(t.Context(), NewHub(), &BrowserConn{AccountID: "owner"}, nil, protocol.ReleaseHeldPrompt{}); problem == nil || problem.Status != http.StatusServiceUnavailable {
		t.Fatalf("missing DB: %v", problem)
	}
}
