package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// dialBrowserTestServer opens a WebSocket connection to /ws/browser on the
// given httptest.Server.
func dialBrowserTestServer(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/browser"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial /ws/browser: %v", err)
	}
	return conn
}

// readMessageWithDeadline reads one WebSocket message within 2 seconds.
func readMessageWithDeadline(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	return raw
}

// TestBrowserInitialState verifies that a newly connected browser receives an
// initial_state message as its very first message.
func TestBrowserInitialState(t *testing.T) {
	hub := NewHub()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", hub.ServeDaemon(testToken, nil))
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, allowWSForTest))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialBrowserTestServer(t, srv)
	defer conn.Close()

	raw := readMessageWithDeadline(t, conn)

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal initial_state: %v", err)
	}
	if env.Type != "initial_state" {
		t.Errorf("expected first message type %q, got %q", "initial_state", env.Type)
	}

	var state protocol.InitialState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("unmarshal InitialState fields: %v", err)
	}
	// With nil pool and no connected daemons, both slices should be empty (not nil).
	if state.Daemons == nil {
		t.Error("Daemons field should be non-nil empty slice")
	}
	if state.Sessions == nil {
		t.Error("Sessions field should be non-nil empty slice")
	}
}

// TestBrowserPingKeepalive verifies the server sends WebSocket ping frames to an
// idle browser connection. Without this, idle connections are killed by the
// ingress proxy's ~60s read timeout; the browser then reconnects, re-subscribes,
// and the server re-replays history — which resets the terminal and yanks the
// user's scroll position back to the bottom roughly once a minute ("can't scroll
// back"). The ping (period < proxy timeout) keeps the connection alive.
func TestBrowserPingKeepalive(t *testing.T) {
	// Shorten the ping period so the test doesn't wait the production interval.
	old := browserPingPeriod
	browserPingPeriod = 50 * time.Millisecond
	defer func() { browserPingPeriod = old }()

	hub := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, allowWSForTest))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialBrowserTestServer(t, srv)
	defer conn.Close()

	pinged := make(chan struct{}, 1)
	conn.SetPingHandler(func(string) error {
		select {
		case pinged <- struct{}{}:
		default:
		}
		return nil
	})

	// Ping/control frames are only processed while the client is reading, so
	// drain messages (incl. the initial_state) in the background.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-pinged:
		// success: server keeps the idle connection alive
	case <-time.After(2 * time.Second):
		t.Fatal("server sent no ping to an idle browser within 2s — no keepalive, idle connections will be reaped")
	}
}

// TestBrowserSubscribeReceivesOutput verifies the subscribe → fan-out → receive
// flow:
//  1. Connect a browser.
//  2. Consume the initial_state.
//  3. Send a subscribe_session message.
//  4. Inject fake output via hub.FanOutSessionOutput.
//  5. Assert the browser receives the session_output message.
func TestBrowserSubscribeReceivesOutput(t *testing.T) {
	hub := NewHub()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, allowWSForTest))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialBrowserTestServer(t, srv)
	defer conn.Close()

	// Step 1: consume initial_state.
	_ = readMessageWithDeadline(t, conn)

	// Step 2: subscribe to a fake session.
	const sessionID = "test-session-abc"
	sub, _ := json.Marshal(protocol.SubscribeSession{
		Type:      "subscribe_session",
		SessionID: sessionID,
	})
	if err := conn.WriteMessage(websocket.TextMessage, sub); err != nil {
		t.Fatalf("write subscribe_session: %v", err)
	}

	// Wait until the hub has recorded the subscription.
	deadline := time.Now().Add(2 * time.Second)
	var subscribed bool
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		_, subscribed = hub.subscriptions[sessionID]
		hub.mu.RUnlock()
		if subscribed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !subscribed {
		t.Fatal("subscription was not registered in hub within 2 s")
	}

	// Step 3: inject a fake session_output event via the fan-out.
	output := protocol.SessionOutput{
		Type:      "session_output",
		SessionID: sessionID,
		Data:      "aGVsbG8=", // base64("hello")
		Seq:       1,
	}
	outputData, _ := json.Marshal(output)
	hub.FanOutSessionOutput(sessionID, outputData)

	// Step 4: the browser should receive the session_output message. A
	// history_done message is sent first after every subscribe (signalling the
	// end of history replay); skip past it to the live output.
	var raw []byte
	var env envelope
	for i := 0; i < 5; i++ {
		raw = readMessageWithDeadline(t, conn)
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("unmarshal output message: %v", err)
		}
		if env.Type != "history_done" {
			break
		}
	}
	if env.Type != "session_output" {
		t.Errorf("expected message type %q, got %q", "session_output", env.Type)
	}

	var received protocol.SessionOutput
	if err := json.Unmarshal(raw, &received); err != nil {
		t.Fatalf("unmarshal SessionOutput: %v", err)
	}
	if received.SessionID != sessionID {
		t.Errorf("expected SessionID %q, got %q", sessionID, received.SessionID)
	}
	if received.Data != output.Data {
		t.Errorf("expected Data %q, got %q", output.Data, received.Data)
	}
}

// TestBrowserDisconnectCleanup verifies that disconnecting a browser removes it
// from the hub and cleans up any subscriptions.
func TestBrowserDisconnectCleanup(t *testing.T) {
	hub := NewHub()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, allowWSForTest))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialBrowserTestServer(t, srv)

	// Consume initial_state.
	_ = readMessageWithDeadline(t, conn)

	// Subscribe to a session.
	const sessionID = "cleanup-session"
	sub, _ := json.Marshal(protocol.SubscribeSession{
		Type:      "subscribe_session",
		SessionID: sessionID,
	})
	if err := conn.WriteMessage(websocket.TextMessage, sub); err != nil {
		t.Fatalf("write subscribe_session: %v", err)
	}

	// Wait for subscription to be registered.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		_, ok := hub.subscriptions[sessionID]
		hub.mu.RUnlock()
		if ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Disconnect the browser.
	conn.Close()

	// Wait for cleanup to propagate.
	deadline = time.Now().Add(2 * time.Second)
	var cleaned bool
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		_, stillRegistered := hub.browsers[findFirstBrowserID(hub)]
		hub.mu.RUnlock()
		if !stillRegistered {
			cleaned = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cleaned {
		t.Error("browser was not unregistered from hub after disconnect")
	}
}

// findFirstBrowserID returns the first browser ID in the hub (for test use only).
// Returns "" if the hub has no browsers.
func findFirstBrowserID(h *Hub) string {
	for id := range h.browsers {
		return id
	}
	return ""
}

// skipToType reads messages from conn until one with the given type is found or
// the read deadline expires, and returns that raw message. It skips messages
// with types in skip. If an error occurs it returns nil, err.
func skipToType(conn *websocket.Conn, wantType string, skip map[string]bool) ([]byte, error) {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if env.Type == wantType {
			return raw, nil
		}
		if skip[env.Type] {
			continue
		}
		// Unexpected type — return a diagnosable error so callers know what arrived.
		return nil, fmt.Errorf("skipToType: unexpected message type %q while waiting for %q", env.Type, wantType)
	}
}

// TestSubscribeStealsFocus verifies that when a second browser subscribes to a
// session that already has an active browser, the first browser receives a
// focus_stolen message and the second does not.
func TestSubscribeStealsFocus(t *testing.T) {
	hub := NewHub()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, allowWSForTest))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Connect browser A and consume its initial_state.
	connA := dialBrowserTestServer(t, srv)
	defer connA.Close()
	_ = readMessageWithDeadline(t, connA)

	// Connect browser B and consume its initial_state.
	connB := dialBrowserTestServer(t, srv)
	defer connB.Close()
	_ = readMessageWithDeadline(t, connB)

	const sessionID = "focus-sess"
	subMsg, _ := json.Marshal(protocol.SubscribeSession{
		Type:      "subscribe_session",
		SessionID: sessionID,
	})

	// A subscribes first.
	if err := connA.WriteMessage(websocket.TextMessage, subMsg); err != nil {
		t.Fatalf("A write subscribe: %v", err)
	}

	// Wait until A is recorded as active in the hub.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		active := hub.sessionActiveBrowser[sessionID]
		hub.mu.RUnlock()
		if active != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// B subscribes the same session — this should steal focus from A.
	if err := connB.WriteMessage(websocket.TextMessage, subMsg); err != nil {
		t.Fatalf("B write subscribe: %v", err)
	}

	// A should receive a focus_stolen message; skip history_done.
	connA.SetReadDeadline(time.Now().Add(2 * time.Second))
	skip := map[string]bool{"history_done": true}
	raw, err := skipToType(connA, "focus_stolen", skip)
	if err != nil {
		t.Fatalf("A did not receive focus_stolen: %v", err)
	}
	var stolen protocol.FocusStolen
	if err := json.Unmarshal(raw, &stolen); err != nil {
		t.Fatalf("unmarshal focus_stolen: %v", err)
	}
	if stolen.SessionID != sessionID {
		t.Errorf("focus_stolen.session_id: expected %q, got %q", sessionID, stolen.SessionID)
	}

	// B should NOT receive a focus_stolen (it stole focus, it doesn't get notified).
	// Allow history_done to pass; fail if focus_stolen arrives within the deadline.
	connB.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		_, raw, err := connB.ReadMessage()
		if err != nil {
			// Deadline exceeded — no focus_stolen received; that is correct.
			break
		}
		var env envelope
		if jsonErr := json.Unmarshal(raw, &env); jsonErr != nil {
			continue
		}
		if env.Type == "focus_stolen" {
			t.Error("B should not receive focus_stolen (it is the new active browser)")
			break
		}
		// history_done or other housekeeping — keep draining.
	}
}

// TestActiveDisconnectPromotes verifies that when the active browser disconnects,
// another subscribed browser receives a focus_granted message.
func TestActiveDisconnectPromotes(t *testing.T) {
	hub := NewHub()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, allowWSForTest))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Connect A and consume initial_state.
	connA := dialBrowserTestServer(t, srv)
	defer connA.Close()
	_ = readMessageWithDeadline(t, connA)

	// Connect B and consume initial_state.
	connB := dialBrowserTestServer(t, srv)
	// NOTE: don't defer connB.Close() here — we close it manually below.
	_ = readMessageWithDeadline(t, connB)

	const sessionID = "promo-sess"
	subMsg, _ := json.Marshal(protocol.SubscribeSession{
		Type:      "subscribe_session",
		SessionID: sessionID,
	})

	// A subscribes first and becomes active.
	if err := connA.WriteMessage(websocket.TextMessage, subMsg); err != nil {
		t.Fatalf("A write subscribe: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		active := hub.sessionActiveBrowser[sessionID]
		hub.mu.RUnlock()
		if active != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// B subscribes — B becomes active, A gets focus_stolen.
	if err := connB.WriteMessage(websocket.TextMessage, subMsg); err != nil {
		t.Fatalf("B write subscribe: %v", err)
	}

	// Wait for both A and B to be subscribed (two subscribers for the session).
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		count := len(hub.subscriptions[sessionID])
		hub.mu.RUnlock()
		if count >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Drain A's focus_stolen so we can wait cleanly for focus_granted.
	connA.SetReadDeadline(time.Now().Add(2 * time.Second))
	skip := map[string]bool{"history_done": true}
	_, _ = skipToType(connA, "focus_stolen", skip)

	// Close B — it is the active browser, so A should be promoted.
	connB.Close()

	// A should receive focus_granted; skip any late-arriving focus_stolen that
	// wasn't consumed by the drain above.
	connA.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, err := skipToType(connA, "focus_granted", map[string]bool{"focus_stolen": true})
	if err != nil {
		t.Fatalf("A did not receive focus_granted: %v", err)
	}
	var granted protocol.FocusGranted
	if err := json.Unmarshal(raw, &granted); err != nil {
		t.Fatalf("unmarshal focus_granted: %v", err)
	}
	if granted.SessionID != sessionID {
		t.Errorf("focus_granted.session_id: expected %q, got %q", sessionID, granted.SessionID)
	}
}

// TestSetSessionStar verifies the set_session_star handler end-to-end:
//  1. A session is created in the DB with starred=false (default).
//  2. A browser sends set_session_star with starred=true for that session.
//  3. The server sets DB starred=true (verified via GetSession).
//  4. The server broadcasts session_star_changed with starred=true to all browsers.
//
// Requires TEST_DATABASE_URL; skips otherwise.
func TestSetSessionStar(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000071"
	sessionID := "00000000-0000-0000-0000-000000000072"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "star-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "idle",
		"/repos/app", "app", "Star Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	// starred defaults to false; no setup needed — just verify initial state.
	row0, err := db.GetSession(ctx, pool, sessionID)
	if err != nil || row0 == nil {
		t.Fatalf("GetSession (setup): %v", err)
	}
	if row0.Starred {
		t.Fatal("Starred should be false after insert")
	}

	hub := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(pool, allowWSForTest))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialBrowserTestServer(t, srv)
	defer conn.Close()

	// Consume initial_state.
	_ = readMessageWithDeadline(t, conn)

	// Send set_session_star with starred=true.
	starMsg, _ := json.Marshal(protocol.SetSessionStar{
		Type:      "set_session_star",
		SessionID: sessionID,
		Starred:   true,
	})
	if err := conn.WriteMessage(websocket.TextMessage, starMsg); err != nil {
		t.Fatalf("write set_session_star: %v", err)
	}

	// Expect a session_star_changed broadcast.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, err := skipToType(conn, "session_star_changed", map[string]bool{})
	if err != nil {
		t.Fatalf("did not receive session_star_changed: %v", err)
	}

	var got protocol.SessionStarChanged
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal session_star_changed: %v", err)
	}
	if got.SessionID != sessionID {
		t.Errorf("session_star_changed.session_id = %q, want %q", got.SessionID, sessionID)
	}
	if !got.Starred {
		t.Errorf("session_star_changed.starred = false, want true")
	}

	// Verify DB: starred must now be true.
	row, dbErr := db.GetSession(ctx, pool, sessionID)
	if dbErr != nil {
		t.Fatalf("GetSession: %v", dbErr)
	}
	if row == nil {
		t.Fatal("GetSession: session not found")
	}
	if !row.Starred {
		t.Errorf("DB Starred = false after set_session_star(true), want true")
	}
}

// TestMarkSessionRead verifies the mark_session_read handler end-to-end:
//  1. A session is created in the DB with unread=true.
//  2. A browser sends mark_session_read for that session.
//  3. The server clears DB unread (verified via GetSession).
//  4. The server broadcasts session_read_changed to all browsers.
//
// Requires TEST_DATABASE_URL; skips otherwise.
func TestMarkSessionRead(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000061"
	sessionID := "00000000-0000-0000-0000-000000000062"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "mark-read-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "idle",
		"/repos/app", "app", "Mark Read Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	// Pre-set unread=true so we can verify it gets cleared.
	if err := db.SetSessionUnread(ctx, pool, sessionID, true); err != nil {
		t.Fatalf("SetSessionUnread (setup): %v", err)
	}

	hub := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(pool, allowWSForTest))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialBrowserTestServer(t, srv)
	defer conn.Close()

	// Consume initial_state.
	_ = readMessageWithDeadline(t, conn)

	// Send mark_session_read.
	markMsg, _ := json.Marshal(protocol.MarkSessionRead{
		Type:      "mark_session_read",
		SessionID: sessionID,
	})
	if err := conn.WriteMessage(websocket.TextMessage, markMsg); err != nil {
		t.Fatalf("write mark_session_read: %v", err)
	}

	// Expect a session_read_changed broadcast.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, err := skipToType(conn, "session_read_changed", map[string]bool{})
	if err != nil {
		t.Fatalf("did not receive session_read_changed: %v", err)
	}

	var got protocol.SessionReadChanged
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal session_read_changed: %v", err)
	}
	if got.SessionID != sessionID {
		t.Errorf("session_read_changed.session_id = %q, want %q", got.SessionID, sessionID)
	}

	// Verify DB: unread must now be false.
	row, dbErr := db.GetSession(ctx, pool, sessionID)
	if dbErr != nil {
		t.Fatalf("GetSession: %v", dbErr)
	}
	if row == nil {
		t.Fatal("GetSession: session not found")
	}
	if row.Unread {
		t.Errorf("DB Unread = true after mark_session_read, want false")
	}
}

// TestInitialStateCarriesUnreadAndStarred verifies that the initial_state sent on
// browser connect includes each session's persisted unread and starred flags — i.e.
// they survive a page refresh / reconnect rather than resetting to false.
//
// Requires TEST_DATABASE_URL; skips otherwise.
func TestInitialStateCarriesUnreadAndStarred(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	daemonID := "00000000-0000-0000-0000-000000000081"
	sessionID := "00000000-0000-0000-0000-000000000082"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "initstate-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "idle",
		"/repos/app", "app", "Init State Test", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if err := db.SetSessionUnread(ctx, pool, sessionID, true); err != nil {
		t.Fatalf("SetSessionUnread (setup): %v", err)
	}
	if err := db.SetSessionStarred(ctx, pool, sessionID, true); err != nil {
		t.Fatalf("SetSessionStarred (setup): %v", err)
	}

	hub := NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(pool, allowWSForTest))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialBrowserTestServer(t, srv)
	defer conn.Close()

	raw := readMessageWithDeadline(t, conn)
	var initial protocol.InitialState
	if err := json.Unmarshal(raw, &initial); err != nil {
		t.Fatalf("unmarshal initial_state: %v", err)
	}

	var found *protocol.SessionInfo
	for i := range initial.Sessions {
		if initial.Sessions[i].ID == sessionID {
			found = &initial.Sessions[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("session %s not present in initial_state", sessionID)
	}
	if !found.Unread {
		t.Errorf("initial_state session Unread = false, want true (unread did not survive reconnect)")
	}
	if !found.Starred {
		t.Errorf("initial_state session Starred = false, want true (star did not survive reconnect)")
	}
}
