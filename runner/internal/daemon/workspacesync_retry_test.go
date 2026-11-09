package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestTurnSyncAuthorityRetriesReconnect(t *testing.T) {
	for _, inFlight := range []bool{false, true} {
		name := "disconnected"
		if inFlight {
			name = "in-flight replacement"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					var req protocol.LocalWorkspaceControl
					if conn.ReadJSON(&req) != nil {
						return
					}
					reply := protocol.LocalWorkspaceControlResult{Type: "local_workspace_control_result", RequestID: req.RequestID, SessionID: req.SessionID, OK: true}
					if conn.WriteJSON(reply) != nil {
						return
					}
				}
			}))
			defer server.Close()
			socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			c := NewWSClient(Config{})
			a := NewWorkspaceAuthority(c)
			if inFlight {
				c.conn = socket
				workspaceReplyReceived = func() { c.mu.Lock(); c.conn = nil; c.mu.Unlock(); workspaceReplyReceived = func() {} }
				defer func() { workspaceReplyReceived = func() {} }()
			}
			waits := 0
			c.sleep = func(ctx context.Context, _ time.Duration) bool {
				waits++
				c.mu.Lock()
				c.conn = socket
				c.mu.Unlock()
				return ctx.Err() == nil
			}
			done := make(chan error, 1)
			go func() { done <- c.readLoop(t.Context(), socket, func([]byte) {}) }()
			defer func() { socket.Close(); <-done }()
			ctx := context.WithValue(t.Context(), workspaceSyncKey{}, true)
			if err := a.Current(ctx, protocol.SupervisorLaunch{SessionID: "session"}, protocol.WorkspaceRuntimeBinding{}); err != nil {
				t.Fatal(err)
			}
			if waits != 1 {
				t.Fatalf("reconnect waits=%d", waits)
			}
		})
	}
}
