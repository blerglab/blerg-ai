package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// TestSetSessionStatus_CarriesStoredUnread verifies that setSessionStatus
// broadcasts the session's stored unread value rather than the zero value false.
// Regression guard: a session with unread=true that goes through reconcile or
// disconnect handling must NOT have its unread badge cleared by the broadcast.
func TestSetSessionStatus_CarriesStoredUnread(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000051"
	sessionID := "00000000-0000-0000-0000-000000000052"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "reconcile-unread-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	// Start the session in "waiting" state so it has a valid transition to "idle".
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "waiting",
		"/repos/app", "app", "Reconcile Unread Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	// Pre-set unread=true to simulate a session the user hasn't acknowledged yet.
	if err := db.SetSessionUnread(ctx, pool, sessionID, true); err != nil {
		t.Fatalf("SetSessionUnread (setup): %v", err)
	}

	hub := NewHub()
	send := make(chan []byte, 1)
	hub.RegisterBrowser(&BrowserConn{ID: "b1", send: send})

	// setSessionStatus simulates what reconcileSessions / handleDaemonDisconnect
	// call — it must carry the stored unread=true in the broadcast.
	setSessionStatus(ctx, hub, pool, sessionID, "idle", nil, nil, false)

	select {
	case raw := <-send:
		var got protocol.SessionStateChanged
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal broadcast: %v", err)
		}
		if !got.Unread {
			t.Errorf("broadcast Unread = false, want true — setSessionStatus must carry stored unread")
		}
		if got.Status != "idle" {
			t.Errorf("broadcast Status = %q, want %q", got.Status, "idle")
		}
	default:
		t.Error("expected a broadcast but got none")
	}

	// Also verify the DB unread is still true (setSessionStatus must not clear it).
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row == nil {
		t.Fatal("GetSession returned nil")
	}
	if !row.Unread {
		t.Errorf("DB Unread = false after setSessionStatus, want true")
	}

}
