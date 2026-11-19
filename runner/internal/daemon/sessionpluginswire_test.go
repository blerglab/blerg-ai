package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestSessionPluginsWireDoesNotWaitBehindOtherWriters(t *testing.T) {
	c := NewWSClient(Config{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			defer conn.Close()
			_, _, _ = conn.ReadMessage()
		}
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c.conn = conn
	release := holdWriter(t, c)
	defer release()
	start := time.Now()
	if err := c.sendSessionPluginsResult(protocol.SessionPluginsResult{}, time.Second); err == nil {
		t.Fatal("busy writer accepted")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("file response blocked behind control writer")
	}
	c.conn = nil
	if err := c.SendSessionPluginsResult(protocol.SessionPluginsResult{}); err == nil {
		t.Fatal("disconnected writer accepted")
	}
}
func TestSessionPluginsWireTimeoutClosesStalledConnection(t *testing.T) {
	conn, observed := stalledWorkspacePeer(t)
	c := NewWSClient(Config{})
	c.conn = conn
	start := time.Now()
	err := c.sendSessionPluginsResult(protocol.SessionPluginsResult{Error: "stalled"}, 20*time.Millisecond)
	if err == nil {
		t.Fatal("stalled transfer succeeded")
	}
	select {
	case <-observed.writing:
	default:
		t.Fatal("transfer did not enter the stalled write")
	}
	if time.Since(start) > time.Second {
		t.Fatal("stalled transfer was not bounded")
	}
	if err = conn.WriteMessage(websocket.TextMessage, []byte("later")); err == nil {
		t.Fatal("timed-out connection remained usable")
	}
}
