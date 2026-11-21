package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

type wireFrame struct {
	Type    string `json:"type"`
	Seq     int    `json:"seq"`
	Data    string `json:"data"`
	Chunks  int    `json:"chunks"`
	Content string `json:"content_b64"`
	size    int
}

// framePeer is a real websocket server: it records every frame's type, size
// and content, in order, on an unbuffered-enough channel it never stalls on.
func framePeer(t *testing.T) (*websocket.Conn, chan wireFrame) {
	t.Helper()
	got := make(chan wireFrame, 4096)
	srv := httptest.NewServer(recordFrames(got))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, got
}

func recordFrames(got chan wireFrame) http.Handler {
	upgrader := websocket.Upgrader{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame wireFrame
			_ = json.Unmarshal(raw, &frame)
			frame.size = len(raw)
			got <- frame
		}
	})
}

func largeFileReply(bytes int) protocol.WorkspaceFileResult {
	return protocol.WorkspaceFileResult{
		Type: "workspace_file_result", RequestID: "req", SessionID: "session", OK: true, Path: "image.png", Size: int64(bytes),
		ContentB64: base64.StdEncoding.EncodeToString(make([]byte, bytes)), Mtime: "2026-11-01T00:00:00Z",
	}
}

func nextFrame(t *testing.T, got chan wireFrame) wireFrame {
	t.Helper()
	select {
	case frame := <-got:
		return frame
	case <-time.After(8 * time.Second):
		t.Fatal("frame never arrived")
		return wireFrame{}
	}
}

// The whole wire: no frame is large, the chunks rebuild the content exactly and
// the closing result carries the count. A heartbeat and a control frame sent
// during the transfer land before it ends, over a real socket.
func TestLargeFileReplyGoesAsChunksAndControlOvertakesIt(t *testing.T) {
	conn, got := framePeer(t)
	c := NewWSClient(Config{})
	c.conn = conn
	c.fileChunks.Store(true)
	reply := largeFileReply(24 << 20)
	sent := make(chan error, 1)
	go func() {
		sent <- c.SendWorkspaceFileResultContext(t.Context(), reply, time.Now().Add(time.Minute))
	}()

	var content strings.Builder
	seen := map[string]int{}
	seq := 0
	var last wireFrame
	for frames := 0; ; frames++ {
		frame := nextFrame(t, got)
		if frame.size > protocol.WorkspaceFileChunkBytes+4096 {
			t.Fatalf("frame %d is %d bytes", frames, frame.size)
		}
		if frames == 0 {
			if err := c.Send(map[string]string{"type": "daemon_heartbeat"}); err != nil {
				t.Fatal(err)
			}
			if err := c.Send(map[string]string{"type": "owner_shell_result", "op": "renew"}); err != nil {
				t.Fatal(err)
			}
		}
		seen[frame.Type]++
		if frame.Type == "workspace_file_chunk" {
			if frame.Seq != seq {
				t.Fatalf("chunk %d arrived as %d", seq, frame.Seq)
			}
			seq++
			content.WriteString(frame.Data)
		}
		if frame.Type == "workspace_file_result" {
			last = frame
			break
		}
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if seen["daemon_heartbeat"] != 1 || seen["owner_shell_result"] != 1 {
		t.Fatalf("control waited behind the transfer: %v", seen)
	}
	if last.Chunks != seq || last.Content != "" || seq < 2 {
		t.Fatalf("closing frame: chunks=%d of %d, inline %d bytes", last.Chunks, seq, len(last.Content))
	}
	if content.String() != reply.ContentB64 {
		t.Fatal("chunks do not rebuild the content")
	}
}

// A server that did not offer chunks gets the one frame it understands, and a
// reply that fits in a chunk stays one frame for every server.
func TestFileReplyStaysOneFrameWithoutChunksOrWhenSmall(t *testing.T) {
	for _, tt := range []struct {
		name   string
		offer  bool
		size   int
		inline bool
	}{{"old server", false, 256 << 10, true}, {"small reply", true, 1024, true}} {
		t.Run(tt.name, func(t *testing.T) {
			conn, got := framePeer(t)
			c := NewWSClient(Config{})
			c.conn = conn
			c.fileChunks.Store(tt.offer)
			reply := largeFileReply(tt.size)
			if err := c.SendWorkspaceFileResultContext(t.Context(), reply, time.Now().Add(5*time.Second)); err != nil {
				t.Fatal(err)
			}
			frame := nextFrame(t, got)
			if frame.Type != "workspace_file_result" || frame.Chunks != 0 || frame.Content != reply.ContentB64 {
				t.Fatalf("frame: %s chunks=%d content=%d bytes", frame.Type, frame.Chunks, len(frame.Content))
			}
		})
	}
}

// A stalled peer during a transfer costs one frame's deadline and closes the
// link: the deadline starts with each frame, not with the reply.
func TestChunkedReplyStallClosesLinkWithinOneFrameDeadline(t *testing.T) {
	conn, observed := stalledWorkspacePeer(t)
	c := NewWSClient(Config{})
	c.conn = conn
	c.fileChunks.Store(true)
	start := time.Now()
	err := c.SendWorkspaceFileResultContext(t.Context(), largeFileReply(1<<20), time.Now().Add(8*time.Second))
	if err == nil {
		t.Fatal("stalled transfer succeeded")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stall held the writer for %v", elapsed)
	}
	select {
	case <-observed.closed:
	default:
		t.Fatal("stalled connection stayed open")
	}
}

// A reply running out of time with a chunk on the wire ends at that chunk and
// keeps the link: the server stopped waiting, nothing about the socket is wrong.
func TestChunkedReplyPastItsDeadlineLeavesTheLinkUp(t *testing.T) {
	got := make(chan wireFrame, 4096)
	srv := httptest.NewServer(recordFrames(got))
	t.Cleanup(srv.Close)
	slow := &slowConn{}
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		slow.Conn = conn
		return slow, err
	}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := NewWSClient(Config{})
	c.conn = conn
	c.fileChunks.Store(true)
	slow.delay.Store(int64(150 * time.Millisecond))
	err = c.SendWorkspaceFileResultContext(t.Context(), largeFileReply(1<<20), time.Now().Add(50*time.Millisecond))
	if err == nil {
		t.Fatal("expired transfer succeeded")
	}
	if err := c.Send(map[string]string{"type": "daemon_heartbeat"}); err != nil {
		t.Fatalf("link went down with the reply: %v", err)
	}
	for {
		frame := nextFrame(t, got)
		if frame.Type == "workspace_file_result" {
			t.Fatal("an abandoned reply was closed")
		}
		if frame.Type == "daemon_heartbeat" {
			return
		}
	}
}

// slowConn pauses once, on the next socket write, so a deadline passes mid-frame.
type slowConn struct {
	net.Conn
	delay atomic.Int64
}

func (c *slowConn) Write(p []byte) (int, error) {
	time.Sleep(time.Duration(c.delay.Swap(0)))
	return c.Conn.Write(p)
}

// gatedConn holds the next socket write, past any deadline, until it is released.
type gatedConn struct {
	net.Conn
	armed            atomic.Bool
	entered, release chan struct{}
}

func (c *gatedConn) Write(p []byte) (int, error) {
	if c.armed.Swap(false) {
		close(c.entered)
		<-c.release
	}
	return c.Conn.Write(p)
}

// A reply whose deadline passes with a chunk on the wire, on a link that then
// goes away, must not hold the output queue: the next connection's replies go
// out, and shutdown finishes.
func TestExpiredChunkDoesNotStrandTheOutputQueueWhenTheLinkEnds(t *testing.T) {
	for _, reconnect := range []bool{true, false} {
		name := "shutdown"
		if reconnect {
			name = "reconnect"
		}
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(recordFrames(make(chan wireFrame, 4096)))
			t.Cleanup(srv.Close)
			gate := &gatedConn{entered: make(chan struct{}), release: make(chan struct{})}
			dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
				gate.Conn = conn
				return gate, err
			}}
			first, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = first.Close() })
			next, got := framePeer(t)
			c := NewWSClient(Config{})
			c.conn = first
			c.fileChunks.Store(true)

			gate.armed.Store(true)
			reply := largeFileReply(1 << 20)
			until := time.Now().Add(300 * time.Millisecond)
			sent := make(chan error, 1)
			go func() { sent <- c.SendWorkspaceFileResultContext(context.Background(), reply, until) }()
			select {
			case <-gate.entered:
			case err := <-sent:
				t.Fatalf("the reply ended before its first chunk: %v", err)
			}
			time.Sleep(time.Until(until) + 50*time.Millisecond)
			if reconnect {
				c.mu.Lock()
				c.conn = next
				c.mu.Unlock()
				if _, err := c.writerFor(next); err != nil {
					t.Fatal(err)
				}
			} else {
				c.mu.Lock()
				fair := c.fair
				c.mu.Unlock()
				fair.Close()
			}
			close(gate.release)
			select {
			case err := <-sent:
				if err == nil {
					t.Fatal("an unfinished reply reported success")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the expired reply held the output queue")
			}
			if !reconnect {
				return
			}
			if err := c.SendWorkspaceExplorerResultContext(t.Context(), protocol.WorkspaceExplorerResult{Type: "workspace_explorer_result"}); err != nil {
				t.Fatal(err)
			}
			if frame := nextFrame(t, got); frame.Type != "workspace_explorer_result" {
				t.Fatalf("frame: %s", frame.Type)
			}
		})
	}
}

// Discovery replies have no writer of their own any more: one queues behind the
// busy writer and is delivered, and canceling it leaves the link alone.
func TestDiscoveryReplyUsesTheConnectionWriter(t *testing.T) {
	conn, got := framePeer(t)
	c := NewWSClient(Config{})
	c.conn = conn
	release := holdWriter(t, c)
	defer release()
	sent := make(chan error, 1)
	go func() { sent <- c.sendDiscoveryReply(t.Context(), conn, map[string]string{"type": "discovery"}) }()
	nothing := time.After(100 * time.Millisecond)
	select {
	case frame := <-got:
		t.Fatalf("a second writer wrote %s", frame.Type)
	case err := <-sent:
		t.Fatalf("reply did not wait for the writer: %v", err)
	case <-nothing:
	}
	release()
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if frame := nextFrame(t, got); frame.Type != "discovery" {
		t.Fatalf("frame: %s", frame.Type)
	}
}

// A server that accepts the connection and never reads cannot hold the daemon
// in its hello: it has a deadline, and the next dial follows.
func TestHelloWriteHasADeadline(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		<-stop
	}))
	t.Cleanup(func() { close(stop); srv.Close() })
	previous, timeout := serverDialer, helloWriteTimeout
	t.Cleanup(func() { serverDialer, helloWriteTimeout = previous, timeout })
	blocked, reader := net.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = blocked.Close() })
	var observed *workspaceObservedConn
	serverDialer = &websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		observed = &workspaceObservedConn{Conn: conn, blocked: blocked, writing: make(chan struct{}), closed: make(chan struct{})}
		return &armOnFirstRead{workspaceObservedConn: observed}, nil
	}}
	helloWriteTimeout = 50 * time.Millisecond
	c := NewWSClient(Config{ServerURL: "ws" + strings.TrimPrefix(srv.URL, "http")})
	done := make(chan error, 1)
	go func() {
		_, err := c.connect(t.Context(), func() []string { return nil })
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled hello succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hello write had no deadline")
	}
}

// armOnFirstRead stalls writes once the upgrade response has been read, so the
// handshake passes and the hello is the first stalled write.
type armOnFirstRead struct{ *workspaceObservedConn }

func (c *armOnFirstRead) Read(p []byte) (int, error) {
	n, err := c.workspaceObservedConn.Read(p)
	c.armed.Store(true)
	return n, err
}
