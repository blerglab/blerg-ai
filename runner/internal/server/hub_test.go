package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

const testToken = "test-secret-token"

// dialTestServer opens a WebSocket connection to the given httptest.Server URL.
func dialTestServer(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/daemon"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

// sendHello writes a daemon_hello message over the given WebSocket connection.
func sendHello(t *testing.T, conn *websocket.Conn, token string) {
	t.Helper()
	hello := protocol.DaemonHello{
		Type:            "daemon_hello",
		Name:            "test-daemon",
		Mode:            "local",
		ReposRoot:       "/repos",
		Token:           token,
		ProtocolVersion: "1",
		ActiveSessions:  []string{},
	}
	data, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write hello: %v", err)
	}
}

// TestHub_DaemonRegistration verifies that a daemon sending a valid hello is
// registered in the hub.
func TestHub_DaemonRegistration(t *testing.T) {
	hub := NewHub()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", hub.ServeDaemon(testToken, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialTestServer(t, srv)
	defer conn.Close()

	sendHello(t, conn, testToken)

	// Give the server goroutine time to process the hello and register.
	deadline := time.Now().Add(2 * time.Second)
	var registered bool
	for time.Now().Before(deadline) {
		hub.mu.RLock()
		registered = len(hub.daemons) > 0
		hub.mu.RUnlock()
		if registered {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !registered {
		t.Fatal("daemon was not registered in hub after valid hello")
	}

	// Confirm GetDaemon works and the stored name is correct.
	hub.mu.RLock()
	var dc *DaemonConn
	for _, d := range hub.daemons {
		dc = d
		break
	}
	hub.mu.RUnlock()

	if dc == nil {
		t.Fatal("GetDaemon returned nil")
	}
	if dc.Name != "test-daemon" {
		t.Errorf("expected Name %q, got %q", "test-daemon", dc.Name)
	}
}

// TestSetActiveBrowserReturnsPrevious verifies that SetActiveBrowser returns
// the previous active browser ID and IsActiveBrowser reflects the new state.
func TestSetActiveBrowserReturnsPrevious(t *testing.T) {
	h := NewHub()

	// First call: no previous active browser.
	prev := h.SetActiveBrowser("s", "a")
	if prev != "" {
		t.Errorf("first SetActiveBrowser: expected empty prev, got %q", prev)
	}

	// Second call: "a" was active, now replaced by "b".
	prev = h.SetActiveBrowser("s", "b")
	if prev != "a" {
		t.Errorf("second SetActiveBrowser: expected prev %q, got %q", "a", prev)
	}
	if !h.IsActiveBrowser("s", "b") {
		t.Error("IsActiveBrowser(s, b) should be true after SetActiveBrowser")
	}
	if h.IsActiveBrowser("s", "a") {
		t.Error("IsActiveBrowser(s, a) should be false after it was replaced")
	}
}

// TestReleaseActiveBrowserPromotes verifies that ReleaseActiveBrowser promotes
// another subscribed browser and returns its ID, or returns "" when no
// promotion is possible.
func TestReleaseActiveBrowserPromotes(t *testing.T) {
	h := NewHub()

	// Seed the subscriptions map with two browsers.
	h.mu.Lock()
	h.subscriptions["s"] = map[string]chan []byte{
		"a": make(chan []byte, 1),
		"b": make(chan []byte, 1),
	}
	h.mu.Unlock()

	h.SetActiveBrowser("s", "a")

	// Releasing "a" should promote "b" (only other subscriber).
	newActive := h.ReleaseActiveBrowser("s", "a")
	if newActive != "b" {
		t.Errorf("expected promoted browser %q, got %q", "b", newActive)
	}
	if !h.IsActiveBrowser("s", "b") {
		t.Error("IsActiveBrowser(s, b) should be true after promotion")
	}

	// Releasing a browser that is NOT the current active returns "".
	newActive = h.ReleaseActiveBrowser("s", "x")
	if newActive != "" {
		t.Errorf("release of non-active should return empty, got %q", newActive)
	}

	// Remove "a" from subscriptions so only "b" remains.
	h.Unsubscribe("s", "a")

	// Now "b" is the only subscriber and it is active; releasing returns "".
	newActive = h.ReleaseActiveBrowser("s", "b")
	if newActive != "" {
		t.Errorf("release with no other subscribers should return empty, got %q", newActive)
	}
}

// TestActiveWithin verifies the engagement window used to choose push vs toast:
// false before any activity, true right after MarkActivity, and false again once
// the last interaction is older than the window.
func TestActiveWithin(t *testing.T) {
	h := NewHub()

	if h.ActiveWithin(3 * time.Minute) {
		t.Error("ActiveWithin should be false before any activity")
	}

	h.MarkActivity()
	if !h.ActiveWithin(3 * time.Minute) {
		t.Error("ActiveWithin should be true immediately after MarkActivity")
	}

	// Backdate the last interaction beyond the window.
	h.mu.Lock()
	h.lastActivity = time.Now().Add(-5 * time.Minute)
	h.mu.Unlock()
	if h.ActiveWithin(3 * time.Minute) {
		t.Error("ActiveWithin should be false once activity is older than the window")
	}
	if !h.ActiveWithin(10 * time.Minute) {
		t.Error("ActiveWithin should be true for a window that still covers the activity")
	}
}

// ── Board subscription tests ──────────────────────────────────────────────────

// newTestBrowser creates a BrowserConn with a buffered send channel and
// registers it in the hub. Call unregister() when done to clean up.
func newTestBrowser(h *Hub) (bc *BrowserConn, unregister func()) {
	bc = &BrowserConn{
		ID:   newUUID(),
		send: make(chan []byte, 32),
	}
	h.RegisterBrowser(bc)
	return bc, func() { h.UnregisterBrowser(bc.ID) }
}

// drainMsg reads a single message from bc.send with a short deadline.
// Returns (msg, true) on success or ("", false) on timeout.
func drainMsg(bc *BrowserConn) ([]byte, bool) {
	select {
	case msg := <-bc.send:
		return msg, true
	case <-time.After(100 * time.Millisecond):
		return nil, false
	}
}

// TestHubBoardSubscription verifies that a subscribed browser receives a
// BroadcastBoard message and that an unsubscribed browser does not.
func TestHubBoardSubscription(t *testing.T) {
	h := NewHub()
	boardID := "board-1"

	sub, cleanSub := newTestBrowser(h)
	defer cleanSub()

	unsub, cleanUnsub := newTestBrowser(h)
	defer cleanUnsub()

	// Only sub is subscribed to boardID.
	h.SubscribeBoard(boardID, sub.ID)

	evt := ticketCreatedEvent{
		Type:   "ticket_created",
		Ticket: ticketInfo{ID: "t1", BoardID: boardID, Title: "Test", Priority: "medium", Rank: "a"},
		OpID:   "op-xyz",
	}
	h.BroadcastBoard(boardID, evt)

	// sub should receive the event.
	msg, ok := drainMsg(sub)
	if !ok {
		t.Fatal("subscribed browser did not receive BroadcastBoard message")
	}

	// Verify the op_id is echoed.
	var got map[string]interface{}
	if err := json.Unmarshal(msg, &got); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if got["type"] != "ticket_created" {
		t.Errorf("expected type %q, got %q", "ticket_created", got["type"])
	}
	if got["op_id"] != "op-xyz" {
		t.Errorf("expected op_id %q, got %q", "op-xyz", got["op_id"])
	}

	// unsub should NOT receive anything.
	if _, ok := drainMsg(unsub); ok {
		t.Error("unsubscribed browser received a BroadcastBoard message it should not have")
	}
}

// TestHubBoardUnsubscribe verifies that after UnsubscribeBoard, the browser no
// longer receives events for that board.
func TestHubBoardUnsubscribe(t *testing.T) {
	h := NewHub()
	boardID := "board-2"

	bc, cleanup := newTestBrowser(h)
	defer cleanup()

	h.SubscribeBoard(boardID, bc.ID)
	h.UnsubscribeBoard(boardID, bc.ID)

	h.BroadcastBoard(boardID, ticketCreatedEvent{
		Type:   "ticket_created",
		Ticket: ticketInfo{ID: "t2", BoardID: boardID, Title: "After unsub", Priority: "low", Rank: "b"},
		OpID:   "op-1",
	})

	if _, ok := drainMsg(bc); ok {
		t.Error("browser received event after UnsubscribeBoard")
	}
}

// TestUnregisterBrowserRemovesBoardSub verifies that UnregisterBrowser also
// removes the browser from all board subscriptions.
func TestUnregisterBrowserRemovesBoardSub(t *testing.T) {
	h := NewHub()
	boardID := "board-3"

	bc, _ := newTestBrowser(h) // intentionally don't defer so we can unregister manually
	h.SubscribeBoard(boardID, bc.ID)

	// Unregister (simulates disconnect).
	h.UnregisterBrowser(bc.ID)

	// Verify hub internal state: no lingering board subscription.
	h.mu.RLock()
	_, hasSub := h.boardSubs[boardID]
	h.mu.RUnlock()
	if hasSub {
		t.Error("board subscription was not cleaned up after UnregisterBrowser")
	}

	// BroadcastBoard should not panic or attempt to deliver to a gone browser.
	h.BroadcastBoard(boardID, ticketCreatedEvent{
		Type:   "ticket_created",
		Ticket: ticketInfo{ID: "t3", BoardID: boardID, Title: "Gone", Priority: "low", Rank: "c"},
		OpID:   "op-2",
	})
	// No assertion needed — the test passes if no panic occurs.
}

// TestHubBoardBroadcastMultipleSubscribers verifies that BroadcastBoard reaches
// all subscribed browsers in a multi-subscriber scenario.
func TestHubBoardBroadcastMultipleSubscribers(t *testing.T) {
	h := NewHub()
	boardID := "board-4"

	a, cleanA := newTestBrowser(h)
	defer cleanA()
	b, cleanB := newTestBrowser(h)
	defer cleanB()
	c, cleanC := newTestBrowser(h)
	defer cleanC()

	h.SubscribeBoard(boardID, a.ID)
	h.SubscribeBoard(boardID, b.ID)
	// c is NOT subscribed.

	h.BroadcastBoard(boardID, columnChangedEvent{
		Type:   "column_changed",
		Column: columnInfo{ID: "col-1", BoardID: boardID, Rank: "a", Name: "Todo"},
		OpID:   "op-col",
	})

	for _, bc := range []*BrowserConn{a, b} {
		msg, ok := drainMsg(bc)
		if !ok {
			t.Errorf("browser %s did not receive BroadcastBoard message", bc.ID)
			continue
		}
		var got map[string]interface{}
		if err := json.Unmarshal(msg, &got); err != nil {
			t.Errorf("browser %s: unmarshal: %v", bc.ID, err)
			continue
		}
		if got["type"] != "column_changed" {
			t.Errorf("browser %s: expected type %q, got %q", bc.ID, "column_changed", got["type"])
		}
		if got["op_id"] != "op-col" {
			t.Errorf("browser %s: expected op_id %q, got %q", bc.ID, "op-col", got["op_id"])
		}
	}

	// c must not receive anything.
	if _, ok := drainMsg(c); ok {
		t.Error("non-subscribed browser c received event")
	}
}

// TestHubBoardSubscribeViaWebSocket exercises the end-to-end flow: a browser
// WS client sends subscribe_board, then the server broadcasts a board event
// and the client receives it.
func TestHubBoardSubscribeViaWebSocket(t *testing.T) {
	hub := NewHub()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/browser", hub.ServeBrowser(nil, allowWSForTest))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/browser"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial browser ws: %v", err)
	}
	defer conn.Close()

	// Drain the initial_state message.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read initial_state: %v", err)
	}

	boardID := "board-ws-1"

	// Send subscribe_board.
	sub := protocol.SubscribeBoard{Type: "subscribe_board", BoardID: boardID}
	data, _ := json.Marshal(sub)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write subscribe_board: %v", err)
	}

	// Give the read pump time to process the subscribe.
	time.Sleep(50 * time.Millisecond)

	// Server-side broadcast to the board.
	hub.BroadcastBoard(boardID, ticketCreatedEvent{
		Type:   "ticket_created",
		Ticket: ticketInfo{ID: "t-ws-1", BoardID: boardID, Title: "WS test", Priority: "high", Rank: "a"},
		OpID:   "op-ws-1",
	})

	// Client should receive the event.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read board event: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal board event: %v", err)
	}
	if got["type"] != "ticket_created" {
		t.Errorf("expected type %q, got %v", "ticket_created", got["type"])
	}
	if got["op_id"] != "op-ws-1" {
		t.Errorf("expected op_id %q, got %v", "op-ws-1", got["op_id"])
	}
}

// TestHub_InvalidToken verifies that a daemon sending the wrong token has its
// connection closed by the server.
func TestHub_InvalidToken(t *testing.T) {
	hub := NewHub()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", hub.ServeDaemon(testToken, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialTestServer(t, srv)
	defer conn.Close()

	sendHello(t, conn, "wrong-token")

	// The server should close the connection with code 4401.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed after wrong token, but read succeeded")
	}
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		// Any read error (including io.EOF) is acceptable — the connection was
		// closed by the server.
		return
	}
	if closeErr.Code != 4401 {
		t.Errorf("expected close code 4401, got %d", closeErr.Code)
	}

	// The hub should have no daemons registered.
	hub.mu.RLock()
	count := len(hub.daemons)
	hub.mu.RUnlock()
	if count != 0 {
		t.Errorf("expected 0 daemons after rejected hello, got %d", count)
	}
}

// TestDaemonConnSandboxAvailable verifies the getter/setter pair
// daemon_conn.go uses to keep sandbox_available current from both
// daemon_hello (initial) and daemon_heartbeat (refreshed live).
func TestDaemonConnSandboxAvailable(t *testing.T) {
	dc := &DaemonConn{ID: "d1"}
	if dc.SandboxAvailable() {
		t.Fatal("zero-value DaemonConn should report sandbox unavailable")
	}
	dc.SetSandboxAvailable(true)
	if !dc.SandboxAvailable() {
		t.Error("SandboxAvailable() = false after SetSandboxAvailable(true)")
	}
	dc.SetSandboxAvailable(false)
	if dc.SandboxAvailable() {
		t.Error("SandboxAvailable() = true after SetSandboxAvailable(false)")
	}
}

// TestDaemonConnAvailableEngines verifies the getter/setter pair
// daemon_conn.go uses to keep available_engines current from both
// daemon_hello (initial) and daemon_heartbeat (refreshed live).
func TestDaemonConnAvailableEngines(t *testing.T) {
	dc := &DaemonConn{ID: "d1"}
	if got := dc.AvailableEngines(); len(got) != 0 {
		t.Fatalf("zero-value DaemonConn should report no available engines, got %v", got)
	}
	dc.SetAvailableEngines([]string{"claude", "codex"})
	got := dc.AvailableEngines()
	if len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Errorf("AvailableEngines() = %v, want [claude codex]", got)
	}
	dc.SetAvailableEngines(nil)
	if got := dc.AvailableEngines(); len(got) != 0 {
		t.Errorf("AvailableEngines() = %v after SetAvailableEngines(nil), want empty", got)
	}
}

// TestDaemonConnCheckedOutRepos verifies the getter/setter pair
// daemon_conn.go uses to keep checked_out_repos current from both
// daemon_hello (initial) and daemon_heartbeat (refreshed live) — a repo
// checked out after the daemon started used to stay invisible until a
// reconnect, since the field was only ever set once at hello time.
func TestDaemonConnCheckedOutRepos(t *testing.T) {
	dc := &DaemonConn{ID: "d1"}
	if got := dc.CheckedOutRepos(); len(got) != 0 {
		t.Fatalf("zero-value DaemonConn.CheckedOutRepos() = %v, want empty", got)
	}
	dc.SetCheckedOutRepos([]string{"blerg"})
	if got := dc.CheckedOutRepos(); len(got) != 1 || got[0] != "blerg" {
		t.Errorf("CheckedOutRepos() = %v after SetCheckedOutRepos([blerg])", got)
	}
	dc.SetCheckedOutRepos([]string{"blerg", "clarity"})
	if got := dc.CheckedOutRepos(); len(got) != 2 {
		t.Errorf("CheckedOutRepos() = %v, want 2 entries after re-set", got)
	}
}
