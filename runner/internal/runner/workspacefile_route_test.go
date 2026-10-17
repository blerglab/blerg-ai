package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestPodWorkspaceFileUsesBoundSessionFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "fixture")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CODEX_AUTH_JSON", "")
	t.Setenv("HERMES_ENV_CONTENTS", "")
	// No initial input: no provider, native agent or model is invoked.
	ctx, cancel := context.WithCancel(context.Background())
	connections := make(chan *websocket.Conn, 1)
	messages := make(chan json.RawMessage, 64)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/daemon" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connections <- conn
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			select {
			case messages <- raw:
			case <-ctx.Done():
				return
			}
		}
	}))
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{SessionID: "workspace-pod", NoRepo: true, Home: home, APIKey: "fixture", ServerWS: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/daemon", ServerHTTP: srv.URL})
	}()
	var conn *websocket.Conn
	t.Cleanup(func() {
		cancel()
		if conn != nil {
			_ = conn.Close()
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("pod Run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("pod Run did not stop")
		}
		srv.Close()
	})
	select {
	case conn = <-connections:
	case <-time.After(3 * time.Second):
		t.Fatal("pod did not connect")
	}
	next := func(kind string) json.RawMessage {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case raw := <-messages:
				var envelope struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(raw, &envelope) == nil && envelope.Type == kind {
					return raw
				}
			case <-timer.C:
				t.Fatalf("missing %s", kind)
				return nil
			}
		}
	}
	next("daemon_hello")
	if err := conn.WriteJSON(map[string]string{"type": "daemon_welcome"}); err != nil {
		t.Fatal(err)
	}
	startedRaw := next("session_started")
	var started protocol.SessionStarted
	if err := json.Unmarshal(startedRaw, &started); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(started.ProjectPath, "README.md"), []byte("pod fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	// A wrong-session command precedes the valid one on the same read pump.
	if err := conn.WriteJSON(protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "foreign", SessionID: "another-session", Path: "README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "valid", SessionID: "workspace-pod", Path: "README.md"}); err != nil {
		t.Fatal(err)
	}
	raw := next("workspace_file_result")
	var got protocol.WorkspaceFileResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	content, err := base64.StdEncoding.DecodeString(got.ContentB64)
	if got.RequestID != "valid" || got.SessionID != "workspace-pod" || !got.OK || err != nil || string(content) != "pod fixture" {
		t.Fatalf("unbound result %+v decode=%v", got, err)
	}
}
