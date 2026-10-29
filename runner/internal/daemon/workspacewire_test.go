package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func TestWorkspaceWireQueuedViewerProgress(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint(disconnect), func(t *testing.T) {
			got := make(chan string, 8)
			upgrader := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
					var envelope struct {
						RequestID string `json:"request_id"`
					}
					_ = json.Unmarshal(raw, &envelope)
					got <- envelope.RequestID
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
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			c.writeMu.Lock()
			locked := true
			defer func() {
				if locked {
					c.writeMu.Unlock()
				}
			}()
			done := make(chan error, 3)
			go func() {
				done <- c.SendWorkspaceExplorerResultContext(ctx, protocol.WorkspaceExplorerResult{RequestID: "first"})
			}()
			wait := func(viewer bool, queued int) {
				t.Helper()
				for {
					c.workspaceOutput.mu.Lock()
					n := len(c.workspaceOutput.explorer)
					if viewer {
						n = len(c.workspaceOutput.viewer)
					}
					running := c.workspaceOutput.running
					c.workspaceOutput.mu.Unlock()
					if running && n == queued {
						return
					}
					select {
					case <-ctx.Done():
						t.Fatal("queue admission")
					default:
						time.Sleep(time.Millisecond)
					}
				}
			}
			wait(false, 0)
			go func() {
				done <- c.SendWorkspaceExplorerResultContext(ctx, protocol.WorkspaceExplorerResult{RequestID: "second"})
			}()
			wait(false, 1)
			go func() {
				done <- c.SendWorkspaceFileResultContext(ctx, protocol.WorkspaceFileResult{RequestID: "viewer"}, time.Now().Add(3*time.Second))
			}()
			wait(true, 1)
			if disconnect {
				_ = conn.Close()
			}
			c.writeMu.Unlock()
			locked = false
			for i := 0; i < 3; i++ {
				select {
				case err := <-done:
					if !disconnect && err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("queued output not joined")
				}
			}
			if !disconnect {
				for _, want := range []string{"first", "viewer", "second"} {
					select {
					case actual := <-got:
						if actual != want {
							t.Fatalf("received %s before %s", actual, want)
						}
					case <-ctx.Done():
						t.Fatal("viewer response missing")
					}
				}
			}
		})
	}
}

func TestWorkspaceWireCancelBeforeWriterPreservesConnection(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint(queued), func(t *testing.T) {
			conn, got := recordingPeer(t)
			c := NewWSClient(Config{})
			c.conn = conn
			c.writeMu.Lock()
			locked := true
			defer func() {
				if locked {
					c.writeMu.Unlock()
				}
			}()
			lifetime, finish := context.WithTimeout(t.Context(), 5*time.Second)
			defer finish()
			var first chan error
			if queued {
				first = make(chan error, 1)
				go func() {
					first <- c.SendWorkspaceExplorerResultContext(lifetime, protocol.WorkspaceExplorerResult{RequestID: "first"})
				}()
			}
			if queued {
				until := time.Now().Add(time.Second)
				for {
					c.workspaceOutput.mu.Lock()
					active := c.workspaceOutput.running && len(c.workspaceOutput.explorer) == 0
					c.workspaceOutput.mu.Unlock()
					if active {
						break
					}
					if time.Now().After(until) {
						t.Fatal("first output not active")
					}
					time.Sleep(time.Millisecond)
				}
			}
			ctx, cancel := context.WithCancel(lifetime)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if queued {
					done <- c.SendWorkspaceFileResultContext(ctx, protocol.WorkspaceFileResult{RequestID: "canceled"}, time.Now().Add(4*time.Second))
				} else {
					done <- c.SendWorkspaceExplorerResultContext(ctx, protocol.WorkspaceExplorerResult{RequestID: "canceled"})
				}
			}()
			deadline := time.After(time.Second)
			for {
				c.workspaceOutput.mu.Lock()
				running := c.workspaceOutput.running
				waiting := len(c.workspaceOutput.viewer)
				c.workspaceOutput.mu.Unlock()
				if running && (!queued || waiting > 0) {
					break
				}
				select {
				case <-deadline:
					t.Fatal("output not admitted")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			// Let the active job enter the shared-writer wait, while the lock stays held.
			if !queued {
				time.Sleep(30 * time.Millisecond)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled output: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("unsent canceled output did not join while another writer held the lock")
			}
			c.writeMu.Unlock()
			locked = false
			if first != nil {
				if err := <-first; err != nil {
					t.Fatal(err)
				}
				select {
				case raw := <-got:
					if !strings.Contains(raw, "first") {
						t.Fatal("wrong first frame")
					}
				case <-lifetime.Done():
					t.Fatal("first output lost")
				}
			}
			if c.currentConn() != conn {
				t.Fatal("cancellation replaced the shared connection")
			}
			if err := c.Send(map[string]string{"type": "control", "request_id": "unrelated-control"}); err != nil {
				t.Fatalf("unsent cancellation disconnected unrelated traffic: %v", err)
			}
			if err := c.SendWorkspaceFileResultContext(lifetime, protocol.WorkspaceFileResult{RequestID: "admitted-viewer"}, time.Now().Add(time.Second)); err != nil {
				t.Fatalf("viewer could not use the same connection: %v", err)
			}
			for _, want := range []string{"unrelated-control", "admitted-viewer"} {
				select {
				case raw := <-got:
					if !strings.Contains(raw, want) {
						t.Fatalf("unexpected frame instead of %s: %s", want, raw)
					}
				case <-lifetime.Done():
					t.Fatalf("missing %s", want)
				}
			}
		})
	}
}

// Track the actual socket write, rather than assuming a large response has begun.
type workspaceObservedConn struct {
	net.Conn
	armed                atomic.Bool
	writing, closed      chan struct{}
	onceWrite, onceClose sync.Once
}

func (c *workspaceObservedConn) Write(p []byte) (int, error) {
	if c.armed.Load() {
		c.onceWrite.Do(func() { close(c.writing) })
	}
	return c.Conn.Write(p)
}
func (c *workspaceObservedConn) Close() error {
	c.onceClose.Do(func() { close(c.closed) })
	return c.Conn.Close()
}
func TestWorkspaceWireCancelActiveWritePoisonsConnection(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if tcp, ok := conn.NetConn().(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(1024)
		}
		<-release
	}))
	defer func() { close(release); srv.Close() }()
	var observed *workspaceObservedConn
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetWriteBuffer(1024)
		}
		observed = &workspaceObservedConn{Conn: conn, writing: make(chan struct{}), closed: make(chan struct{})}
		return observed, nil
	}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	observed.armed.Store(true)
	c := NewWSClient(Config{})
	c.conn = conn
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.SendWorkspaceFileResultContext(ctx, protocol.WorkspaceFileResult{ContentB64: strings.Repeat("A", 8<<20)}, time.Now().Add(8*time.Second))
	}()
	select {
	case <-observed.writing:
	case <-time.After(3 * time.Second):
		t.Fatal("frame did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled frame succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("active frame cancellation did not join")
	}
	select {
	case <-observed.closed:
	default:
		t.Fatal("partial frame connection not closed")
	}
}
