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

func TestWorkspaceWireDoesNotWaitBehindOtherWriters(t *testing.T) {
	c := NewWSClient(Config{})
	c.conn = &websocket.Conn{}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	start := time.Now()
	if err := c.sendWorkspaceFileResult(protocol.WorkspaceFileResult{}, time.Second); err == nil {
		t.Fatal("busy writer accepted")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("file response blocked behind control writer")
	}
	c.conn = nil
	if err := c.SendWorkspaceFileResult(protocol.WorkspaceFileResult{}); err == nil {
		t.Fatal("disconnected writer accepted")
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
