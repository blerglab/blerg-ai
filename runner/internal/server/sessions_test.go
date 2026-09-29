package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Note on state-machine enforcement:
// HandleSessionStateChanged calls isValidTransition before touching Postgres or
// the broadcast hub. The enforcement path is exercised by DB integration tests
// (set TEST_DATABASE_URL to enable) and indirectly verified by the table-driven
// unit test below, which covers all valid and invalid transition combinations.

// TestSessionStateMachine verifies that isValidTransition accepts only the
// transitions defined in the session state machine and rejects invalid ones.
func TestSessionStateMachine(t *testing.T) {
	tests := []struct {
		from  string
		to    string
		valid bool
	}{
		// Valid transitions
		{"starting", "running", true},
		{"starting", "error", true},
		// An agent session with nothing queued is ready and idle at once.
		{"starting", "idle", true},
		{"running", "waiting", true},
		{"running", "stopped", true},
		{"running", "error", true},
		{"waiting", "running", true},
		{"waiting", "stopped", true},
		{"waiting", "error", true},

		// Idle transitions — the daemon's WaitingDetector emits "idle" after a
		// turn settles, and revives to running/waiting on new activity. A missing
		// entry here silently drops the state change and the UI sticks on
		// "running" (the regression this guards against).
		{"running", "idle", true},
		{"idle", "running", true},
		{"idle", "waiting", true},
		{"idle", "stopped", true},
		{"idle", "error", true},

		// Invalid transitions — terminal states have no outgoing edges
		{"stopped", "running", false},
		{"stopped", "waiting", false},
		{"stopped", "error", false},
		{"stopped", "idle", false},
		{"error", "running", false},
		{"error", "waiting", false},
		{"error", "stopped", false},
		{"error", "idle", false},

		// Invalid transitions — backwards or nonsensical
		{"starting", "waiting", false},
		{"starting", "stopped", false},
		{"waiting", "starting", false},
		{"waiting", "idle", false},
		{"running", "starting", false},
		{"idle", "starting", false},

		// Unknown states
		{"", "running", false},
		{"unknown", "running", false},
		{"running", "unknown", false},
	}

	for _, tt := range tests {
		got := isValidTransition(tt.from, tt.to)
		if got != tt.valid {
			t.Errorf("isValidTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.valid)
		}
	}
}

// TestSessionNotification verifies which state transitions produce a push
// notification and that the body uses the session's human label. Only the
// "stop, the human is needed" states notify: waiting (needs input) and idle
// (turn finished). running/starting/stopped/error stay silent.
func TestSessionNotification(t *testing.T) {
	tests := []struct {
		status   string
		label    string
		wantOK   bool
		wantBody string
	}{
		{"waiting", "auth: add login", true, "auth: add login is waiting for your input."},
		{"idle", "auth: add login", true, "auth: add login finished its turn."},
		{"running", "auth: add login", false, ""},
		{"starting", "auth: add login", false, ""},
		{"stopped", "auth: add login", false, ""},
		{"error", "auth: add login", false, ""},
		{"", "auth: add login", false, ""},
	}

	for _, tt := range tests {
		_, body, ok := sessionNotification(tt.status, tt.label)
		if ok != tt.wantOK {
			t.Errorf("sessionNotification(%q): ok = %v, want %v", tt.status, ok, tt.wantOK)
		}
		if ok && body != tt.wantBody {
			t.Errorf("sessionNotification(%q): body = %q, want %q", tt.status, body, tt.wantBody)
		}
	}
}

// TestSessionLabel checks the fallback chain title → repo → short id used to
// name a session in notifications, so a session with no title still reads
// sensibly instead of leaking a bare UUID.
func TestSessionLabel(t *testing.T) {
	tests := []struct {
		title string
		repo  string
		id    string
		want  string
	}{
		{"add login", "auth-svc", "bdb9ccfb-0564-43da-a589-14637ea4cc71", "add login"},
		{"", "auth-svc", "bdb9ccfb-0564-43da-a589-14637ea4cc71", "auth-svc"},
		{"", "", "bdb9ccfb-0564-43da-a589-14637ea4cc71", "bdb9ccfb"},
	}
	for _, tt := range tests {
		if got := sessionLabel(tt.title, tt.repo, tt.id); got != tt.want {
			t.Errorf("sessionLabel(%q,%q,%q) = %q, want %q", tt.title, tt.repo, tt.id, got, tt.want)
		}
	}
}

// TestReconcileDesiredStatus covers the heartbeat reconciliation logic that
// self-heals dropped state changes. The key case is a session the DB has as
// "running" but the daemon reports as "idle" — the dropped idle event the user
// hit, which must converge to "idle".
func TestReconcileDesiredStatus(t *testing.T) {
	tests := []struct {
		name          string
		current       string
		reported      bool
		reportedState string
		wantStatus    string
		wantChange    bool
	}{
		// The bug: idle event was dropped, DB stuck on running. Heartbeat heals it.
		{"running heals to idle", "running", true, "idle", "idle", true},
		{"idle back to running", "idle", true, "running", "running", true},
		{"running to waiting", "running", true, "waiting", "waiting", true},

		// Already in sync — no redundant write/broadcast.
		{"running stays running", "running", true, "running", "", false},
		{"idle stays idle", "idle", true, "idle", "", false},

		// Reported but daemon hasn't characterised state yet (dormant detector):
		// keep an existing active status, revive a terminal one to running.
		{"keep idle when no state reported", "idle", true, "", "", false},
		{"keep waiting when no state reported", "waiting", true, "", "", false},
		{"revive errored session", "error", true, "", "running", true},
		{"revive stopped session", "stopped", true, "", "running", true},

		// Not reported by the daemon: stop active sessions, leave terminal ones.
		{"active but unreported stops", "running", false, "", "stopped", true},
		{"idle but unreported stops", "idle", false, "", "stopped", true},
		{"terminal unreported unchanged", "stopped", false, "", "", false},
		{"errored unreported unchanged", "error", false, "", "", false},

		// A bogus reported state is ignored (treated as "no state").
		{"bogus state ignored, keep running", "running", true, "garbage", "", false},
	}

	for _, tt := range tests {
		gotStatus, gotChange := reconcileDesiredStatus(tt.current, tt.reported, tt.reportedState)
		if gotStatus != tt.wantStatus || gotChange != tt.wantChange {
			t.Errorf("%s: reconcileDesiredStatus(%q, %v, %q) = (%q, %v), want (%q, %v)",
				tt.name, tt.current, tt.reported, tt.reportedState,
				gotStatus, gotChange, tt.wantStatus, tt.wantChange)
		}
	}
}

// TestHandleSessionNotFound exercises the pre-start spawn error path: when a
// state_change arrives for a session with no DB row, handleSessionNotFound must
// broadcast the message to browsers when Status is "error", and must NOT
// broadcast (returning nil) for any other status.
//
// handleSessionNotFound is the extracted handler for the current==nil branch of
// HandleSessionStateChanged. Testing it directly avoids a real DB dependency
// while matching the pattern used for other unexported helpers in this file.
func TestHandleSessionNotFound(t *testing.T) {
	t.Run("error status broadcasts to browsers", func(t *testing.T) {
		hub := NewHub()
		// Register a fake browser with a buffered send channel so we can
		// capture what BroadcastJSON delivers.
		send := make(chan []byte, 1)
		b := &BrowserConn{ID: "b1", send: send}
		hub.RegisterBrowser(b)

		msg := protocol.SessionStateChanged{
			Type:      "session_state_changed",
			SessionID: "pre-start-session-id",
			Status:    "error",
		}
		if err := handleSessionNotFound(hub, msg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case raw := <-send:
			var got protocol.SessionStateChanged
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal broadcast: %v", err)
			}
			if got.SessionID != msg.SessionID {
				t.Errorf("broadcast session_id = %q, want %q", got.SessionID, msg.SessionID)
			}
			if got.Status != "error" {
				t.Errorf("broadcast status = %q, want %q", got.Status, "error")
			}
		default:
			t.Error("expected a broadcast for Status=error but got none")
		}
	})

	t.Run("non-error status does not broadcast", func(t *testing.T) {
		hub := NewHub()
		send := make(chan []byte, 1)
		b := &BrowserConn{ID: "b1", send: send}
		hub.RegisterBrowser(b)

		msg := protocol.SessionStateChanged{
			Type:      "session_state_changed",
			SessionID: "pre-start-session-id",
			Status:    "waiting",
		}
		if err := handleSessionNotFound(hub, msg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		select {
		case got := <-send:
			t.Errorf("did not expect a broadcast for Status=waiting, got: %s", got)
		default:
			// correct: no broadcast
		}
	})
}

// ─── sessionRowToInfo — unread mapping ───────────────────────────────────────

// TestSessionRowToInfo_UnreadMapped verifies that sessionRowToInfo copies
// row.Unread into the returned SessionInfo. This is a pure unit test with no
// DB dependency — it catches a missing mapping before any integration path.
func TestSessionRowToInfo_UnreadMapped(t *testing.T) {
	row := db.SessionRow{
		ID:        "test-session",
		DaemonID:  "test-daemon",
		Status:    "waiting",
		Repo:      "myrepo",
		StartedAt: time.Now(),
		Unread:    true,
	}
	info := sessionRowToInfo(row, "")
	if !info.Unread {
		t.Errorf("sessionRowToInfo: Unread not mapped: got false, want true")
	}
}

// TestSessionRowToInfo_StarredMapped verifies that sessionRowToInfo copies
// row.Starred into the returned SessionInfo. This is a pure unit test with no
// DB dependency — it catches a missing mapping before any integration path.
func TestSessionRowToInfo_StarredMapped(t *testing.T) {
	row := db.SessionRow{
		ID:        "test-session",
		DaemonID:  "test-daemon",
		Status:    "waiting",
		Repo:      "myrepo",
		StartedAt: time.Now(),
		Starred:   true,
	}
	info := sessionRowToInfo(row, "")
	if !info.Starred {
		t.Errorf("sessionRowToInfo: Starred not mapped: got false, want true")
	}
}

// ─── HandleSessionStateChanged — unread integration tests ────────────────────
//
// These tests require a real Postgres database. Set TEST_DATABASE_URL to run.

// connectSrvTestDB returns a pool connected to the test database, or skips the
// test if TEST_DATABASE_URL is not set.
func connectSrvTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// setupSrvTestSchema creates an isolated schema for server integration tests and
// sets the search_path so all unqualified table names resolve to it.
func setupSrvTestSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	schema := "test_blerg_runner_srv"

	for _, q := range []string{
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema),
		fmt.Sprintf("CREATE SCHEMA %s", schema),
		fmt.Sprintf("SET search_path TO %s", schema),
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("setupSrvTestSchema %q: %v", q, err)
		}
	}

	t.Cleanup(func() {
		pool.Exec(context.Background(),
			fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
	})
}

// TestHandleSessionStateChanged_SetsUnreadOnTransition verifies that:
//   - running → waiting sets DB unread=true and broadcast carries Unread=true
//   - running → idle   sets DB unread=true and broadcast carries Unread=true
//   - idle    → running does NOT change unread; broadcast carries Unread=false
func TestHandleSessionStateChanged_SetsUnreadOnTransition(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000031"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "unread-test-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}

	cases := []struct {
		name       string
		from       string
		to         string
		wantUnread bool
	}{
		{"running to waiting sets unread", "running", "waiting", true},
		{"running to idle sets unread", "running", "idle", true},
		{"idle to running preserves false unread", "idle", "running", false},
	}

	for i, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sessionID := fmt.Sprintf("00000000-0000-0000-0000-1000000000%02d", i+1)
			if err := db.InsertSession(ctx, pool, sessionID, daemonID, tc.from,
				"/repos/app", "app", "Unread Test", ""); err != nil {
				t.Fatalf("InsertSession: %v", err)
			}

			hub := NewHub()
			send := make(chan []byte, 1)
			hub.RegisterBrowser(&BrowserConn{ID: "b1", send: send})

			msg := protocol.SessionStateChanged{
				Type:      "session_state_changed",
				SessionID: sessionID,
				Status:    tc.to,
			}
			if err := HandleSessionStateChanged(ctx, hub, pool, msg); err != nil {
				t.Fatalf("HandleSessionStateChanged: %v", err)
			}

			// Assert DB state.
			row, err := db.GetSession(ctx, pool, sessionID)
			if err != nil {
				t.Fatalf("GetSession: %v", err)
			}
			if row.Unread != tc.wantUnread {
				t.Errorf("DB Unread = %v, want %v", row.Unread, tc.wantUnread)
			}

			// Assert broadcast carries the correct unread value.
			select {
			case raw := <-send:
				var got protocol.SessionStateChanged
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("unmarshal broadcast: %v", err)
				}
				if got.Unread != tc.wantUnread {
					t.Errorf("broadcast Unread = %v, want %v", got.Unread, tc.wantUnread)
				}
			default:
				t.Error("expected a broadcast but got none")
			}
		})
	}
}

// TestHandleSessionStateChanged_BroadcastCarriesExistingUnread verifies that
// when a session with unread=true transitions to running (idle → running), the
// broadcast carries Unread=true — i.e. the handler reads the DB value rather
// than assuming false for non-waiting/idle transitions.
func TestHandleSessionStateChanged_BroadcastCarriesExistingUnread(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000041"
	sessionID := "00000000-0000-0000-0000-000000000042"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "unread-carry-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "idle",
		"/repos/app", "app", "Carry Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	// Pre-set unread=true to simulate a session the user hasn't read yet.
	if err := db.SetSessionUnread(ctx, pool, sessionID, true); err != nil {
		t.Fatalf("SetSessionUnread (setup): %v", err)
	}

	hub := NewHub()
	send := make(chan []byte, 1)
	hub.RegisterBrowser(&BrowserConn{ID: "b1", send: send})

	msg := protocol.SessionStateChanged{
		Type:      "session_state_changed",
		SessionID: sessionID,
		Status:    "running",
	}
	if err := HandleSessionStateChanged(ctx, hub, pool, msg); err != nil {
		t.Fatalf("HandleSessionStateChanged: %v", err)
	}

	// DB unread must remain true — idle→running must not clear it.
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !row.Unread {
		t.Errorf("DB Unread = false after idle→running, want true (must not be cleared)")
	}

	// Broadcast must carry Unread=true so the client shows the badge.
	select {
	case raw := <-send:
		var got protocol.SessionStateChanged
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal broadcast: %v", err)
		}
		if !got.Unread {
			t.Errorf("broadcast Unread = false, want true")
		}
	default:
		t.Error("expected a broadcast but got none")
	}
}

// TestHandleSessionEndedClosesOpenMessages verifies that HandleSessionEnded calls
// CloseOpenMessagesForSession and broadcasts message_answered for each closed row.
// DB-gated.
func TestHandleSessionEndedClosesOpenMessages(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000c1"
	sessionID := "00000000-0000-0000-0000-0000000000c2"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "ended-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "End Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Create two open ask messages and one already-answered update.
	ask1, _ := db.CreateMessage(ctx, pool, sessionID, "ask", "question 1")
	ask2, _ := db.CreateMessage(ctx, pool, sessionID, "ask", "question 2")
	_, _ = db.CreateMessage(ctx, pool, sessionID, "update", "already answered")

	hub := NewHub()
	send := make(chan []byte, 10)
	hub.RegisterBrowser(&BrowserConn{ID: "b1", send: send})

	HandleSessionEnded(ctx, hub, pool, protocol.SessionEnded{
		Type:      "session_ended",
		SessionID: sessionID,
		ExitCode:  0,
	})

	// Both open asks must now be answered with answer=''.
	row1, err1 := db.GetMessage(ctx, pool, ask1)
	row2, err2 := db.GetMessage(ctx, pool, ask2)
	if err1 != nil || row1 == nil || row1.Status != "answered" || row1.Answer == nil || *row1.Answer != "" {
		t.Errorf("ask1 after session_ended: err=%v row=%+v", err1, row1)
	}
	if err2 != nil || row2 == nil || row2.Status != "answered" || row2.Answer == nil || *row2.Answer != "" {
		t.Errorf("ask2 after session_ended: err=%v row=%+v", err2, row2)
	}

	// Drain all broadcasts and count message_answered events.
	var answeredCount int
	for {
		select {
		case raw := <-send:
			var m map[string]interface{}
			json.Unmarshal(raw, &m)
			if mt, _ := m["type"].(string); mt == "message_answered" {
				answeredCount++
			}
		default:
			goto done
		}
	}
done:
	if answeredCount != 2 {
		t.Errorf("got %d message_answered broadcasts, want 2 (one per open ask)", answeredCount)
	}
}

// TestMessageRowToInfo_MapsNullableAnswer verifies the answer pointer survives the
// mapping: nil for an update, set for a real reply.
func TestMessageRowToInfo_MapsNullableAnswer(t *testing.T) {
	upd := messageRowToInfo(db.MessageRow{ID: "m1", SessionID: "s1", Kind: "update", Body: "done", Status: "answered"})
	if upd.Answer != nil {
		t.Errorf("update Answer = %v, want nil", *upd.Answer)
	}
	ans := "amber"
	ask := messageRowToInfo(db.MessageRow{ID: "m2", SessionID: "s1", Kind: "ask", Body: "?", Status: "answered", Answer: &ans})
	if ask.Answer == nil || *ask.Answer != "amber" {
		t.Errorf("ask Answer = %v, want amber", ask.Answer)
	}
}

// TestErrorReasonIsPersistedAndOrphanDeletable verifies that an "error"
// state-change persists its reason (R7), and that once the owning daemon is
// gone, a terminal-state row can still be deleted (204) instead of 404ing
// forever (trial blocker 2).
func TestErrorReasonIsPersistedAndOrphanDeletable(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	daemonID := "00000000-0000-4000-8000-00000000d006"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "err-daemon", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	sessionID := "00000000-0000-4000-8000-00000000e006"
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "starting", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	dc := &DaemonConn{ID: daemonID, Name: "err-daemon", send: make(chan []byte, 4)}
	hub.Register(dc)
	hub.SetSessionOwner(sessionID, daemonID)

	reason := "claude not found on PATH of the daemon service (PATH=/usr/bin)"
	if err := HandleSessionStateChanged(ctx, hub, pool, protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: sessionID, Status: "error", Message: &reason,
	}); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetSession(ctx, pool, sessionID)
	if row.ErrorReason == nil || *row.ErrorReason != reason {
		t.Fatalf("error_reason = %v, want %q", row.ErrorReason, reason)
	}
	if sessionRowToInfo(*row, "").ErrorReason != reason {
		t.Fatal("SessionInfo.ErrorReason not mapped")
	}

	// Daemon goes away: the error row must still be deletable (204), not 404.
	hub.UnregisterIfCurrent(dc)
	api := NewAPI(hub, pool, "daemon-tok-1234567890", nil, "")
	tok := enableBrowserAuth(t, api)("session.start")
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sessionID, nil)
	req.SetPathValue("id", sessionID)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandleDeleteSession(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete orphaned error session = %d %s, want 204", rec.Code, rec.Body.String())
	}
	row, _ = db.GetSession(ctx, pool, sessionID)
	if row.Status != "stopped" || row.EndedAt == nil {
		t.Fatalf("after delete: status=%s ended_at=%v, want stopped + set", row.Status, row.EndedAt)
	}
}

// TestErrorReasonClearedOnRevival verifies that reviving a session out of
// "error" (the ordinary laptop-sleep/network-blip/daemon-restart case:
// handleDaemonDisconnect marks it "error", then reconcileSessions revives it
// to "running" once the daemon reconnects) also clears error_reason — a
// revived, healthy session must not carry a stale failure reason forward
// (fix round 1).
func TestErrorReasonClearedOnRevival(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	daemonID := "00000000-0000-4000-8000-00000000d008"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "revive-daemon", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	sessionID := "00000000-0000-4000-8000-00000000e008"
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "starting", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	dc := &DaemonConn{ID: daemonID, Name: "revive-daemon", send: make(chan []byte, 4)}
	hub.Register(dc)
	hub.SetSessionOwner(sessionID, daemonID)

	reason := "claude not found on PATH of the daemon service (PATH=/usr/bin)"
	if err := HandleSessionStateChanged(ctx, hub, pool, protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: sessionID, Status: "error", Message: &reason,
	}); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetSession(ctx, pool, sessionID)
	if row.ErrorReason == nil || *row.ErrorReason != reason {
		t.Fatalf("precondition: error_reason = %v, want %q", row.ErrorReason, reason)
	}

	// The daemon reconnects and reports this session as active but with no
	// classified state yet — reconcileDesiredStatus revives an "error" row to
	// "running" in that case, and reconcileSessions drives that through
	// setSessionStatus, which must clear error_reason.
	reconcileSessions(ctx, hub, pool, daemonID, []string{sessionID}, map[string]string{})

	row, _ = db.GetSession(ctx, pool, sessionID)
	if row.Status != "running" {
		t.Fatalf("status after reconcile = %q, want running", row.Status)
	}
	if row.ErrorReason != nil {
		t.Fatalf("error_reason after revival = %v, want nil", *row.ErrorReason)
	}
	if sessionRowToInfo(*row, "").ErrorReason != "" {
		t.Fatalf("SessionInfo.ErrorReason after revival = %q, want empty", sessionRowToInfo(*row, "").ErrorReason)
	}
}

// TestDeleteSessionWithConnectedDaemonForwardsKill verifies that the new
// orphan-delete branch in HandleDeleteSession is only taken when the owning
// daemon is gone — a session whose daemon is still connected must still get
// a kill_session forwarded to it, never a direct DB-only "stopped" write
// (which would leave a live tmux/docker session running unmanaged).
func TestDeleteSessionWithConnectedDaemonForwardsKill(t *testing.T) {
	hub := NewHub()
	daemonID := "00000000-0000-4000-8000-00000000d007"
	sessionID := "00000000-0000-4000-8000-00000000e007"
	dc := &DaemonConn{ID: daemonID, Name: "live-daemon", send: make(chan []byte, 4)}
	hub.Register(dc)
	hub.SetSessionOwner(sessionID, daemonID)

	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	tok := enableBrowserAuth(t, api)("session.start")
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sessionID, nil)
	req.SetPathValue("id", sessionID)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandleDeleteSession(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete live session = %d %s, want 204", rec.Code, rec.Body.String())
	}

	select {
	case raw := <-dc.send:
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal forwarded message: %v", err)
		}
		if m["type"] != "kill_session" {
			t.Fatalf("forwarded message type = %v, want kill_session", m["type"])
		}
		if m["session_id"] != sessionID {
			t.Fatalf("forwarded kill_session session_id = %v, want %s", m["session_id"], sessionID)
		}
	default:
		t.Fatal("expected kill_session forwarded to the connected daemon, got nothing")
	}
}
