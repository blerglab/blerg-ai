package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Nothing the daemon sends may reach the server before its hello: a server reads the first frame
// as the hello and closes the connection (4400) on anything else. A sender that fires while the
// client is connecting — the usage-limits tracker's read, a session's output — must wait.
func TestWSClientHelloIsAlwaysTheFirstFrame(t *testing.T) {
	// Some folders under the repos root make the hello take a moment to build.
	root := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	firsts := make(chan string, 64)
	ts := newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var env struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &env)
		firsts <- env.Type
		// Drop it: the client connects again, racing the sender once more.
	})
	client := NewWSClient(Config{
		ServerURL: ts.wsURL(), DaemonToken: "t", DaemonName: "d", DaemonMode: "local", ReposRoot: root,
		ProtocolVersion: "1", HeartbeatInterval: time.Hour,
		ReconnectInitial: time.Millisecond, ReconnectMax: time.Millisecond,
	})
	client.sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			_ = client.Send(map[string]string{"type": "usage_limits", "provider": "codex"})
		}
	}()
	go client.Run(ctx, func() []string { return nil })
	for i := 0; i < 20; i++ {
		select {
		case typ := <-firsts:
			if typ != "daemon_hello" {
				t.Fatalf("connection %d: first frame %q, want daemon_hello", i, typ)
			}
		case <-ctx.Done():
			t.Fatal("too few connections")
		}
	}
}
