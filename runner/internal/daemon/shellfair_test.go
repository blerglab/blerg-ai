package daemon

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestActualFileRepliesShareShellControlAndDataScheduler(t *testing.T) {
	conn, got := recordingPeer(t)
	c := NewWSClient(Config{})
	c.conn = conn
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	// Exercise actual WSClient APIs and writes; hold only the first scheduled
	// frame so every lane is ready before checking ordering on the real socket.
	c.fairConn = conn
	c.fair = shellruntime.NewFairWriter(func(ctx context.Context, raw []byte) error {
		if strings.Contains(string(raw), `"type":"gate"`) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
			return err
		}
		return conn.WriteMessage(websocket.TextMessage, raw)
	})
	defer c.fair.Close()
	var wg sync.WaitGroup
	start := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				t.Error(err)
			}
		}()
	}
	start(func() error { return c.Send(map[string]string{"type": "gate"}) })
	<-entered
	for n := 0; n < 2; n++ {
		start(func() error {
			return c.SendWorkspaceFileResultBy(protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: "file"}, time.Now().Add(5*time.Second))
		})
	}
	for n := 0; n < 2; n++ {
		start(func() error { return c.Send(map[string]string{"type": "agent_event"}) })
	}
	for n := 0; n < 8; n++ {
		start(func() error { return c.Send(shellruntime.RPC{Type: "owner_shell_result", Op: "renew"}) })
	}
	for n := 0; n < 2; n++ {
		start(func() error { return c.Send(shellruntime.RPC{Type: "owner_shell_output", Data: []byte("bounded")}) })
	}
	deadline := time.Now().Add(time.Second)
	for {
		control, data, ordinary := c.fair.Pending()
		c.workspaceOutput.mu.Lock()
		queuedFiles := len(c.workspaceOutput.viewer)
		c.workspaceOutput.mu.Unlock()
		if control == 8 && data == 2 && ordinary >= 3 && ordinary+queuedFiles == 4 {
			break
		}
		select {
		case <-got:
			once.Do(func() { close(release) })
			wg.Wait()
			t.Fatal("real file API bypassed the scheduler while its first frame was blocked")
		default:
		}
		if time.Now().After(deadline) {
			once.Do(func() { close(release) })
			wg.Wait()
			t.Fatal("actual file/control/data calls did not enter their bounded lanes")
		}
		runtime.Gosched()
	}
	// Drain continuously: recordingPeer's own small test queue must not stall writes.
	result := make(chan []string, 1)
	go func() {
		var order []string
		for range 15 {
			select {
			case raw := <-got:
				var f struct{ Type string }
				_ = json.Unmarshal([]byte(raw), &f)
				order = append(order, f.Type)
			case <-time.After(3 * time.Second):
				result <- order
				return
			}
		}
		result <- order
	}()
	once.Do(func() { close(release) })
	wg.Wait()
	order := <-result
	if len(order) != 15 || order[1] != "owner_shell_result" {
		t.Fatalf("control was not prioritized: %v", order)
	}
	firstFile, firstAgent, firstShell := 99, 99, 99
	for i, kind := range order {
		switch kind {
		case "workspace_file_result":
			firstFile = min(firstFile, i)
		case "agent_event":
			firstAgent = min(firstAgent, i)
		case "owner_shell_output":
			firstShell = min(firstShell, i)
		}
	}
	if min(firstFile, firstAgent) > 10 || max(firstFile, firstAgent) > 14 || firstShell > 10 {
		t.Fatalf("file/agent/shell starvation: %v", order)
	}
}

func TestStalledActualFileClosesLinkAndNewControlMakesProgress(t *testing.T) {
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
	fileDone := make(chan error, 1)
	go func() {
		fileDone <- c.SendWorkspaceFileResultBy(protocol.WorkspaceFileResult{Type: "workspace_file_result", ContentB64: strings.Repeat("A", 8<<20)}, time.Now().Add(5*time.Second))
	}()
	// Wait until the actual large socket write owns the writer, then enqueue
	// control behind it. Deadline closure, rather than a second writer, releases it.
	deadline := time.Now().Add(time.Second)
	for c.writeMu.TryLock() {
		c.writeMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("file write never started")
		}
		runtime.Gosched()
	}
	started := time.Now()
	_ = c.Send(shellruntime.RPC{Type: "owner_shell_result", Op: "close_launch"})
	if err := <-fileDone; err == nil {
		t.Fatal("stalled file unexpectedly succeeded")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("stalled file held revocation past write bound")
	}
	replacement, got := recordingPeer(t)
	c.mu.Lock()
	c.conn = replacement
	c.mu.Unlock()
	if err := c.Send(shellruntime.RPC{Type: "owner_shell_result", Op: "renew"}); err != nil {
		t.Fatal("fresh connection control stalled", err)
	}
	select {
	case raw := <-got:
		if !strings.Contains(raw, "renew") {
			t.Fatal("wrong control")
		}
	case <-time.After(time.Second):
		t.Fatal("new control never arrived")
	}
	c.mu.Lock()
	writer := c.fair
	c.mu.Unlock()
	if writer != nil {
		writer.Close()
	}
}
