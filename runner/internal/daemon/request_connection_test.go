package daemon

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// Reproduce a scheduler-stalled list reply whose request ends mid-frame.
// The gate models CPU starvation without depending on scheduler timing.
func TestExplorerCancellationDuringFramePreservesConnection(t *testing.T) {
	got := make(chan wireFrame, 4096)
	srv := httptest.NewServer(recordFrames(got))
	t.Cleanup(srv.Close)
	gate := &gatedConn{entered: make(chan struct{}), release: make(chan struct{})}
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		gate.Conn = conn
		return gate, err
	}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := NewWSClient(Config{})
	c.conn = conn
	e, root, req := explorerFixture(t)
	for i := 0; i < 600; i++ {
		writeExplorerFile(t, root, strings.Repeat("x", 100)+time.Unix(int64(i), 0).UTC().Format("150405")+".txt")
	}
	res := e.Explore(t.Context(), req)
	if !res.OK || len(res.Entries) != 500 {
		t.Fatalf("list: ok=%v entries=%d", res.OK, len(res.Entries))
	}
	gate.armed.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	sent := make(chan error, 1)
	go func() { sent <- c.SendWorkspaceExplorerResultContext(ctx, res) }()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("list never entered socket write")
	}
	<-ctx.Done()

	close(gate.release)
	if err := <-sent; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired request: %v", err)
	}
	if err := c.Send(protocol.DaemonHeartbeat{Type: "daemon_heartbeat"}); err != nil {
		t.Fatalf("request cancellation poisoned shared connection: %v", err)
	}
	if frame := nextFrame(t, got); frame.Type != "workspace_explorer_result" {
		t.Fatalf("list frame: %s", frame.Type)
	}
	if frame := nextFrame(t, got); frame.Type != "daemon_heartbeat" {
		t.Fatalf("heartbeat frame: %s", frame.Type)
	}
}

func TestExplorerCanceledBeforeFramePreservesConnection(t *testing.T) {
	conn, got := framePeer(t)
	c := NewWSClient(Config{})
	c.conn = conn
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.SendWorkspaceExplorerResultContext(ctx, protocol.WorkspaceExplorerResult{Type: "workspace_explorer_result"}); err == nil {
		t.Fatal("canceled request succeeded")
	}
	if err := c.Send(protocol.DaemonHeartbeat{Type: "daemon_heartbeat"}); err != nil {
		t.Fatal(err)
	}
	if frame := nextFrame(t, got); frame.Type != "daemon_heartbeat" {
		t.Fatalf("canceled frame delivered: %s", frame.Type)
	}
}
