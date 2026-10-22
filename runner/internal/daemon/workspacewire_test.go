package daemon

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestWorkspaceWireGivesUpOnAWriterThatStaysBusy(t *testing.T) {
	c := NewWSClient(Config{})
	c.conn = &websocket.Conn{}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	start := time.Now()
	if err := c.sendWorkspaceFileResult(protocol.WorkspaceFileResult{}, time.Second); err == nil {
		t.Fatal("busy writer accepted")
	}
	// Bounded by the wait cap (the write's own budget only starts once it holds the writer).
	if time.Since(start) > workspaceWriterWait+500*time.Millisecond {
		t.Fatal("file response waited past its cap behind a control writer")
	}
	c.conn = nil
	if err := c.SendWorkspaceFileResult(protocol.WorkspaceFileResult{}); err == nil {
		t.Fatal("disconnected writer accepted")
	}
}

// A writer that is busy only for a moment (another file reply, an event) used to make the file reply
// fail at once: the reply was dropped and the server waited out its whole timeout, so an image in a
// README showed only its alt text. The reply now waits for the writer and is delivered.
func TestWorkspaceWireDeliversAfterABrieflyBusyWriter(t *testing.T) {
	got := make(chan string, 4)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			got <- string(msg)
		}
	}))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := NewWSClient(Config{})
	c.conn = conn
	c.writeMu.Lock() // another write is going out
	go func() {
		time.Sleep(50 * time.Millisecond)
		c.writeMu.Unlock()
	}()
	if err := c.sendWorkspaceFileResult(protocol.WorkspaceFileResult{RequestID: "r1", Path: "docs/media/more-power.jpg"}, 10*time.Second); err != nil {
		t.Fatalf("reply dropped behind a briefly busy writer: %v", err)
	}
	select {
	case msg := <-got:
		if !strings.Contains(msg, "more-power.jpg") {
			t.Fatalf("unexpected message %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reply never arrived")
	}
}
func TestWorkspaceWireTimeoutClosesStalledConnection(t *testing.T) {
	release := make(chan struct{})
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if tcp, ok := conn.NetConn().(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(1024)
		}
		<-release // deliberately never drain the socket
	}))
	defer func() { close(release); srv.Close() }()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcp, ok := conn.NetConn().(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(1024)
	}
	c := NewWSClient(Config{})
	c.conn = conn
	start := time.Now()
	err = c.sendWorkspaceFileResult(protocol.WorkspaceFileResult{ContentB64: strings.Repeat("A", 8<<20)}, 20*time.Millisecond)
	if err == nil {
		t.Fatal("stalled transfer succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("stalled transfer was not bounded")
	}
	if err = conn.WriteMessage(websocket.TextMessage, []byte("later")); err == nil {
		t.Fatal("timed-out connection remained usable")
	}
}

// A ws server that records every message; dial returns a client connection to it.
func recordingPeer(t *testing.T) (*websocket.Conn, chan string) {
	t.Helper()
	got := make(chan string, 8)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			got <- string(msg)
		}
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, got
}

func nothingArrives(t *testing.T, got chan string, why string) {
	t.Helper()
	select {
	case msg := <-got:
		t.Fatalf("%s: %q was written", why, msg)
	case <-time.After(200 * time.Millisecond):
	}
}

// The write gets its whole budget once it holds the writer: time spent waiting for a busy
// writer is not taken out of it (a late start then timed out and closed the whole link).
func TestWorkspaceWireWriteDeadlineStartsWithTheWrite(t *testing.T) {
	conn, got := recordingPeer(t)
	c := NewWSClient(Config{})
	c.conn = conn
	c.writeMu.Lock()
	go func() {
		time.Sleep(300 * time.Millisecond)
		c.writeMu.Unlock()
	}()
	// The writer is busy for longer than the write's own 200 ms budget.
	if err := c.sendWorkspaceFileResult(protocol.WorkspaceFileResult{RequestID: "late-start"}, 200*time.Millisecond); err != nil {
		t.Fatalf("a reply that waited lost its write budget: %v", err)
	}
	select {
	case msg := <-got:
		if !strings.Contains(msg, "late-start") {
			t.Fatalf("unexpected message %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reply never arrived")
	}
}

// A reply the server has stopped waiting for is not written: the server would read the whole
// frame and throw it away.
func TestWorkspaceWireSkipsAReplyTheServerGaveUpOn(t *testing.T) {
	conn, got := recordingPeer(t)
	c := NewWSClient(Config{})
	c.conn = conn
	if err := c.SendWorkspaceFileResultBy(protocol.WorkspaceFileResult{RequestID: "stale"}, time.Now().Add(-time.Second)); err == nil {
		t.Fatal("an expired reply was sent")
	}
	nothingArrives(t, got, "expired reply")
	// Expiry while waiting for a busy writer.
	c.writeMu.Lock()
	go func() {
		time.Sleep(300 * time.Millisecond)
		c.writeMu.Unlock()
	}()
	if err := c.SendWorkspaceFileResultBy(protocol.WorkspaceFileResult{RequestID: "stale-2"}, time.Now().Add(100*time.Millisecond)); err == nil {
		t.Fatal("a reply that expired while waiting was sent")
	}
	nothingArrives(t, got, "reply expired in the wait")
}

// A reconnect while the reply waits: the reply is not written on the old connection (the server
// bound the request to it, and a half-dead old socket must not hold the writer).
func TestWorkspaceWireDropsAReplyWhenTheConnectionChanged(t *testing.T) {
	oldConn, oldGot := recordingPeer(t)
	newConn, newGot := recordingPeer(t)
	c := NewWSClient(Config{})
	c.conn = oldConn
	c.writeMu.Lock()
	go func() {
		time.Sleep(50 * time.Millisecond)
		c.mu.Lock()
		c.conn = newConn
		c.mu.Unlock()
		c.writeMu.Unlock()
	}()
	if err := c.sendWorkspaceFileResult(protocol.WorkspaceFileResult{RequestID: "old"}, 5*time.Second); err == nil {
		t.Fatal("a reply for the old connection was sent")
	}
	nothingArrives(t, oldGot, "old connection")
	nothingArrives(t, newGot, "new connection")
}
