package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// A definitive refusal delivered on this call's connection stays definitive if the connection is
// replaced before the call reads it. Read as "connection replaced" it was transient, and the
// turn-end sync thawed a superseded session's writers. An ordinary success or unexplained failure
// on a replaced connection is still no verdict.
func TestWorkspaceAuthorityRefusalSurvivesConnectionReplacement(t *testing.T) {
	for _, c := range []struct {
		name    string
		reply   protocol.LocalWorkspaceControlResult
		refused bool
	}{
		{"definitive refusal", protocol.LocalWorkspaceControlResult{Refused: "a newer launch replaced this one"}, true},
		{"unexplained failure", protocol.LocalWorkspaceControlResult{}, false},
		{"success", protocol.LocalWorkspaceControlResult{OK: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
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
					reply := c.reply
					reply.Type, reply.RequestID, reply.SessionID = "local_workspace_control_result", req.RequestID, req.SessionID
					if conn.WriteJSON(reply) != nil {
						return
					}
				}
			}))
			defer server.Close()
			dial := func() *websocket.Conn {
				socket, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
				if err != nil {
					t.Fatal(err)
				}
				return socket
			}
			first, second := dial(), dial()
			defer first.Close()
			defer second.Close()
			client := NewWSClient(Config{})
			client.conn = first
			authority := NewWorkspaceAuthority(client)
			done := make(chan error, 1)
			go func() { done <- client.readLoop(t.Context(), first, func([]byte) {}) }()
			defer func() { first.Close(); <-done }()
			// The reconnect lands after the reply was delivered to this call, before it is read.
			workspaceReplyReceived = func() { client.mu.Lock(); client.conn = second; client.mu.Unlock() }
			defer func() { workspaceReplyReceived = func() {} }()
			err := authority.Current(t.Context(), protocol.SupervisorLaunch{SessionID: "session"}, protocol.WorkspaceRuntimeBinding{})
			var refused *WorkspaceRefusedError
			if got := errors.As(err, &refused); got != c.refused {
				t.Fatalf("Current = %v, definitive refusal %t, want %t", err, got, c.refused)
			}
			if !c.refused && (err == nil || !strings.Contains(err.Error(), "connection replaced")) {
				t.Fatalf("Current = %v, want connection replaced", err)
			}
		})
	}
}

// A definitive refusal already delivered when the call's context ends must not be lost to select's
// random choice: read as a timeout it was transient, and the sync thawed a superseded session. Each
// round makes both cases ready before the select, so the old code fails with odds 1-2^-64.
func TestWorkspaceAuthorityRefusalBeatsAnExpiredCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
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
			if conn.WriteJSON(protocol.LocalWorkspaceControlResult{Type: "local_workspace_control_result", RequestID: req.RequestID, SessionID: req.SessionID, Refused: "a newer launch replaced this one"}) != nil {
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
	client := NewWSClient(Config{})
	client.conn = socket
	authority := NewWorkspaceAuthority(client)
	done := make(chan error, 1)
	go func() { done <- client.readLoop(t.Context(), socket, func([]byte) {}) }()
	defer func() { socket.Close(); <-done }()
	defer func() { workspaceReplySelecting = func() {} }()
	for round := range 64 {
		ctx, cancel := context.WithCancel(t.Context())
		workspaceReplySelecting = func() {
			waitUntilFor(t, 5*time.Second, "reply delivery", func() bool {
				authority.mu.Lock()
				defer authority.mu.Unlock()
				return len(authority.pending) == 0
			})
			cancel()
		}
		err := authority.Current(ctx, protocol.SupervisorLaunch{SessionID: "session"}, protocol.WorkspaceRuntimeBinding{})
		cancel()
		var refused *WorkspaceRefusedError
		if !errors.As(err, &refused) {
			t.Fatalf("round %d: Current = %v, want the delivered refusal", round, err)
		}
	}
}

// Once abandon has withdrawn the wait, a reply can no longer be delivered to it, so no refusal can
// be committed after the call has returned.
func TestWorkspaceAuthorityAbandonClosesDelivery(t *testing.T) {
	client := NewWSClient(Config{})
	authority := NewWorkspaceAuthority(client)
	wait := workspaceControlWait{session: "session", reply: make(chan protocol.LocalWorkspaceControlResult, 1)}
	authority.pending["request"] = wait
	cause := errors.New("cause")
	if _, err := authority.abandon("request", wait, cause); !errors.Is(err, cause) {
		t.Fatalf("abandon with nothing queued = %v", err)
	}
	raw, _ := json.Marshal(protocol.LocalWorkspaceControlResult{RequestID: "request", SessionID: "session", Refused: "too late"})
	client.handlers["local_workspace_control_result"](raw)
	if len(wait.reply) != 0 {
		t.Fatal("a reply was delivered to an abandoned wait")
	}
}
