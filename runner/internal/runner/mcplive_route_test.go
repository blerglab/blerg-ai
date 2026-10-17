package runner

import (
	"context"
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

// Exercise the pod's real websocket registration and AgentHost without a
// provider, a daemon service, a git repository or a container runtime.
func TestPodMCPReleaseRoutesOnlyItsOwnSession(t *testing.T) {
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,sys
if "--version" in sys.argv:
 print("codex-cli 0.162.1");sys.exit(0)
for line in sys.stdin:
 r=json.loads(line);i=r.get("id");m=r.get("method","")
 if i is None: continue
 result={}
 if m=="model/list": result={"data":[],"nextCursor":None}
 if m=="mcpServerStatus/list": result={"data":[{"name":"fixture","runtimeStatus":"starting","tools":{}}],"nextCursor":None}
 if m in ("thread/start","thread/resume"): result={"thread":{"id":"fixture-thread"},"model":"gpt-5-codex","reasoningEffort":"high"}
 if m=="turn/start": result={"turn":{"id":"fixture-turn"}}
 print(json.dumps({"id":i,"result":result}),flush=True)
 if m=="turn/start": print(json.dumps({"method":"turn/completed","params":{"threadId":"fixture-thread","turn":{"id":"fixture-turn","status":"completed"}}}),flush=True)
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir()) // Run changes HOME; t.Cleanup restores it after the runner has stopped.
	t.Setenv("CODEX_AUTH_JSON", `{}`)
	t.Setenv("BLERG_RUNNER_MCP_PROMPT_WAIT_S", "30")
	frames := make(chan json.RawMessage, 256)
	connections := make(chan *websocket.Conn, 1)
	upgrade := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrade.Upgrade(w, r, nil)
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
			case frames <- raw:
			case <-r.Context().Done():
				return
			}
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var runErr error
	cfg := Config{SessionID: "fixture", ServerWS: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws", ServerHTTP: srv.URL, Home: t.TempDir(), NoRepo: true, Engine: "codex", InitialPrompt: "held fixture prompt"}
	go func() { runErr = Run(ctx, cfg); close(finished) }()
	var conn *websocket.Conn
	defer func() {
		if conn != nil {
			_ = conn.WriteJSON(protocol.KillSession{Type: "kill_session", SessionID: "fixture"})
		}
		cancel()
		select {
		case <-finished:
			if runErr != nil {
				t.Error(runErr)
			}
		case <-time.After(5 * time.Second):
			t.Error("pod did not stop")
		}
	}()
	select {
	case conn = <-connections:
	case <-time.After(5 * time.Second):
		t.Fatal("pod did not connect")
	}
	next := func() protocol.AgentEvent {
		t.Helper()
		deadline := time.NewTimer(6 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case raw := <-frames:
				var event protocol.AgentEvent
				if json.Unmarshal(raw, &event) == nil && event.Type == "agent_event" {
					return event
				}
			case <-finished:
				t.Fatalf("pod exited before event: %v", runErr)
			case <-deadline.C:
				t.Fatal("pod did not publish expected event")
			}
		}
	}
	for {
		event := next()
		if event.Kind == "user_message" {
			t.Fatal("pod prompt bypassed hold")
		}
		if event.Kind == protocol.MCPStatusKind {
			var p protocol.MCPStatusPayload
			_ = json.Unmarshal(event.Payload, &p)
			if p.Hold != nil {
				break
			}
		}
	}
	if err := conn.WriteJSON(protocol.ReleaseHeldPrompt{Type: "release_held_prompt", SessionID: "foreign"}); err != nil {
		t.Fatal(err)
	}
	// A native settings response is a barrier proving that the websocket reader
	// processed the wrong-session release before we inspect delivery.
	if err := conn.WriteJSON(protocol.SetSessionModel{Type: "set_session_model", SessionID: "fixture", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	for {
		event := next()
		if event.Kind == "user_message" {
			t.Fatal("foreign release bypassed the pod session boundary")
		}
		if event.Kind == "model_changed" {
			break
		}
	}
	if err := conn.WriteJSON(protocol.ReleaseHeldPrompt{Type: "release_held_prompt", SessionID: "fixture"}); err != nil {
		t.Fatal(err)
	}
	sawPerson := false
	for {
		event := next()
		if event.Kind == protocol.MCPStatusKind {
			var p protocol.MCPStatusPayload
			_ = json.Unmarshal(event.Payload, &p)
			sawPerson = sawPerson || p.Released == "person"
		}
		if event.Kind == "turn_done" {
			break
		}
	}
	if !sawPerson {
		t.Fatal("pod release did not clear its first-prompt hold")
	}
}
