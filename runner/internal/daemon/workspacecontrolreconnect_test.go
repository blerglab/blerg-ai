package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// A report owns the lifecycle lock which background reconnect needs. It must
// recover inline rather than waiting for background recovery to acquire it.
func TestWorkspaceReportRecoversOnlyConnectionLoss(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cause        protocol.WorkspaceControlCause
		refused      string
		wantRecovery bool
		newerCache   bool
	}{
		{"connection", protocol.WorkspaceControlConnectionRecovery, "", true, false},
		{"definitive", protocol.WorkspaceControlConnectionRecovery, "replaced launch", false, false},
		{"unexplained", "", "", false, false},
		{"storage", protocol.WorkspaceControlStorage, "", false, false},
		{"unauthorized runtime", protocol.WorkspaceControlRuntimeUnauthorized, "", false, false},
		{"cached newer incarnation", protocol.WorkspaceControlStorage, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _, _, _ := localWorkspaceFixture(t)
			l := protocol.SupervisorLaunch{SessionID: w.state.Session, LaunchGeneration: w.state.Launch.LaunchGeneration, RuntimeGeneration: 1, Runtime: "host", ConnectionID: "old-connection"}
			b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: l.LaunchGeneration, Generation: l.RuntimeGeneration}, ManifestDigest: w.state.Launch.Digest, Root: protocol.WorkspaceRoot{Inode: 7}}
			next := l
			next.ConnectionID = "new-connection"
			if tc.newerCache {
				next.RuntimeGeneration++
			}
			var recoveries atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(rw, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					_, raw, err := conn.ReadMessage()
					if err != nil {
						return
					}
					var req protocol.LocalWorkspaceControl
					if json.Unmarshal(raw, &req) != nil {
						return
					}
					reply := protocol.LocalWorkspaceControlResult{Type: "local_workspace_control_result", RequestID: req.RequestID, SessionID: req.SessionID}
					switch req.Operation {
					case "current":
						reply.OK = req.Launch == next
						if !reply.OK {
							reply.Cause = tc.cause
							reply.Refused = tc.refused
						}
					case "recover":
						recoveries.Add(1)
						reply.OK = req.Launch == l && req.Binding == b
						reply.Launch = next
					case "results":
						reply.OK = req.Launch == next && req.Binding == b
					}
					if conn.WriteJSON(reply) != nil {
						return
					}
				}
			}))
			defer srv.Close()
			socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			client := NewWSClient(Config{})
			client.conn = socket
			a := NewWorkspaceAuthority(client)
			a.remember(l)
			if tc.newerCache {
				a.remember(next)
			}
			done := make(chan error, 1)
			go func() { done <- client.readLoop(t.Context(), socket, func([]byte) {}) }()
			defer func() { socket.Close(); <-done }()
			w.state.Binding = &b
			w.state.SupervisorLaunch = l
			w.authorize = func(ctx context.Context) error { return a.Current(ctx, l, b) }
			service := &LocalWorkspaceService{Authority: a, Results: a.Results}
			err = service.Report(t.Context(), w)
			if tc.wantRecovery {
				if err != nil || recoveries.Load() != 1 {
					t.Fatalf("inline report recovery: %v, recoveries=%d", err, recoveries.Load())
				}
				// The workspace's durable launch can still name the old connection until
				// background recovery joins. Its next report reuses the authenticated one.
				if err = service.Report(t.Context(), w); err != nil || recoveries.Load() != 1 {
					t.Fatalf("cached connection: %v", err)
				}
			} else {
				if err == nil || recoveries.Load() != 0 {
					t.Fatalf("unexpected recovery: %v, recoveries=%d", err, recoveries.Load())
				}
				var refusal *WorkspaceRefusedError
				if errors.As(err, &refusal) != (tc.refused != "") {
					t.Fatalf("refusal classification: %v", err)
				}
			}
		})
	}
}
