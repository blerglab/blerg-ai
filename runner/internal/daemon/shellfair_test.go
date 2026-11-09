package daemon

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
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
		// The file lane holds the one reply the serial viewer queue lets through.
		if control == 8 && data == 2 && ordinary == 2 && queuedFiles == 1 {
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
	// The gate can be claimed by next or by the idle select. Only next
	// advances the ordinary/shell turn, so either data lane may go first.
	// Check the policy itself: at most four controls between data turns,
	// and alternating ordinary/shell turns while both remain queued.
	remaining := map[string]int{"owner_shell_result": 8, "agent_event": 2, "owner_shell_output": 2, "workspace_file_result": 2}
	controls, lastData := 0, ""
	for _, kind := range order[1:] {
		if remaining[kind] == 0 {
			t.Fatalf("unexpected or duplicate frame: %v", order)
		}
		switch kind {
		case "owner_shell_result":
			controls++
			if controls > 4 && (remaining["agent_event"] > 0 || remaining["owner_shell_output"] > 0) {
				t.Fatalf("control burst starved data: %v", order)
			}
		case "agent_event", "owner_shell_output":
			if remaining["agent_event"] > 0 && remaining["owner_shell_output"] > 0 && kind == lastData {
				t.Fatalf("data lanes did not alternate: %v", order)
			}
			controls, lastData = 0, kind
		case "workspace_file_result":
			if remaining["owner_shell_result"] > 0 {
				t.Fatalf("file overtook queued control: %v", order)
			}
		}
		remaining[kind]--
	}
	for kind, count := range remaining {
		if count != 0 {
			t.Fatalf("%s did not make progress: %v", kind, order)
		}
	}
}

func TestStalledActualFileClosesLinkAndNewControlMakesProgress(t *testing.T) {
	conn, observed := stalledWorkspacePeer(t)
	c := NewWSClient(Config{})
	c.conn = conn
	fileDone := make(chan error, 1)
	go func() {
		fileDone <- c.SendWorkspaceFileResultBy(protocol.WorkspaceFileResult{Type: "workspace_file_result", ContentB64: "QQ=="}, time.Now().Add(5*time.Second))
	}()
	// Wait until a partial frame owns the writer, then enqueue
	// control behind it. Deadline closure, rather than a second writer, releases it.
	select {
	case <-observed.writing:
	case <-time.After(time.Second):
		t.Fatal("file write never started")
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
