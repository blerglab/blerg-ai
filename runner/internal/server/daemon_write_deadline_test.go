package server

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"

	"github.com/gorilla/websocket"
)

// The authority deadline may expire while a frame is already in the kernel.
// Interrupting that frame poisons Gorilla's writer and drops every session.
func TestDaemonStartedFrameOutlivesAuthorityDeadline(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	authority, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		wrapped := conn.UnderlyingConn().(*deadlineGateConn)
		wrapped.entered, wrapped.release = entered, release
		dc := &DaemonConn{send: make(chan []byte, 1)}
		dc.send <- []byte(`{"type":"cancel_workspace_explorer"}`)
		// Expire only after Write starts, independently of scheduler speed.
		scoped, stop := context.WithDeadline(authority, time.Now().Add(100*time.Millisecond))
		defer stop()
		go func() { <-entered; <-scoped.Done(); close(release) }()
		daemonWritePump(scoped, dc, conn, nil)
		close(done)
	}))
	srv.Listener = &deadlineGateListener{Listener: srv.Listener}
	srv.Start()
	defer srv.Close()
	peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := peer.ReadMessage()
	if err != nil {
		t.Fatalf("authority deadline interrupted started frame: %v", err)
	}
	if string(raw) != `{"type":"cancel_workspace_explorer"}` {
		t.Fatalf("frame: %s", raw)
	}
	<-done
}

type deadlineGateListener struct{ net.Listener }

func (l *deadlineGateListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	return &deadlineGateConn{Conn: c}, nil
}

type deadlineGateConn struct {
	net.Conn
	entered, release chan struct{}
}

func (c *deadlineGateConn) Write(p []byte) (int, error) {
	if c.entered != nil {
		close(c.entered)
		c.entered = nil
		<-c.release
	}
	return c.Conn.Write(p)
}

func TestDaemonWriterShutdownLogsCause(t *testing.T) {
	var out bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	defer func() { failurelog.Default = previous }()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		daemonWritePump(ctx, &DaemonConn{send: make(chan []byte)}, conn, nil)
		close(done)
	}))
	defer srv.Close()
	peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	<-done
	if !strings.Contains(out.String(), `"stage":"close_write_pump_stopped"`) || !strings.Contains(out.String(), `"cause":"canceled"`) {
		t.Fatalf("initiator cause missing: %s", out.String())
	}
}
