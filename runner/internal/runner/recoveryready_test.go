package runner

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryReadyEnvironmentReachesPodSpawn(t *testing.T) {
	for _, value := range []string{"", "0", "true", "1"} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv("BLERG_RUNNER_REQUIRE_RECOVERY_READY", value)
			cfg := ConfigFromEnv()
			spawn := podSpawn(cfg, t.TempDir())
			if cfg.RequireRecoveryReady != (value == "1") || spawn.RequireRecoveryReady != (value == "1") {
				t.Fatalf("internal recovery marker did not survive environment -> config -> spawn: config=%v spawn=%v", cfg.RequireRecoveryReady, spawn.RequireRecoveryReady)
			}
		})
	}
}

// Exercise the real scoped runner startup and its AgentHost against a fake
// websocket/native binary, without a pod, Docker daemon, provider, or git repo.
func TestScopedRunnerRecoveryMarkerHoldsInitialAndManualInput(t *testing.T) {
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,sys
if "--version" in sys.argv:
 print("codex-cli 0.162.1");sys.exit(0)
for line in sys.stdin:
 r=json.loads(line);i=r.get("id");m=r.get("method","")
 if i is None: continue
 with open(os.environ["FAKE_RECOVERY_CODEX_LOG"],"a") as f: f.write(m+"\n")
 result={}
 if m=="model/list": result={"data":[],"nextCursor":None}
 if m=="mcpServerStatus/list": result={"data":[],"nextCursor":None}
 if m in ("thread/start","thread/resume"): result={"thread":{"id":"fixture-thread"},"model":"gpt-5-codex","reasoningEffort":"high"}
 if m in ("turn/start","turn/steer"): result={"turn":{"id":"fixture-turn"}}
 print(json.dumps({"id":i,"result":result}),flush=True)
 if m=="turn/start": print(json.dumps({"method":"turn/completed","params":{"threadId":"fixture-thread","turn":{"id":"fixture-turn","status":"completed"}}}),flush=True)
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_AUTH_JSON", `{"auth_mode":"apikey","OPENAI_API_KEY":"fixture-api-key"}`)
	t.Setenv("BLERG_RUNNER_MCP_PROMPT_WAIT_S", "0")
	logPath := filepath.Join(dir, "native.log")
	t.Setenv("FAKE_RECOVERY_CODEX_LOG", logPath)
	frames := make(chan []byte, 512)
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
	cfg := Config{SessionID: "fixture", ServerWS: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws", ServerHTTP: srv.URL, Home: t.TempDir(), NoRepo: true, Engine: "codex", InitialPrompt: "initial fixture", RequireRecoveryReady: true}
	go func() { runErr = Run(ctx, cfg); close(finished) }()
	defer func() {
		cancel()
		select {
		case <-finished:
			if runErr != nil {
				t.Error(runErr)
			}
		case <-time.After(15 * time.Second):
			t.Error("scoped runner failed to stop")
		}
	}()
	var conn *websocket.Conn
	select {
	case conn = <-connections:
	case <-time.After(15 * time.Second):
		t.Fatal("scoped runner never connected")
	}
	next := func(typ, kind string) {
		t.Helper()
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case raw := <-frames:
				var frame struct {
					Type, Kind string
					Payload    json.RawMessage
				}
				if json.Unmarshal(raw, &frame) == nil && frame.Type == typ && (kind == "" || frame.Kind == kind) {
					if kind == "model_changed" {
						var change struct{ Source string }
						if json.Unmarshal(frame.Payload, &change) != nil || change.Source != "ui" {
							continue
						}
					}
					if typ == "daemon_hello" {
						var hello protocol.DaemonHello
						_ = json.Unmarshal(raw, &hello)
						if !hello.RecoveryReady {
							t.Fatal("scoped hello did not advertise ready barrier")
						}
					}
					return
				}
			case <-finished:
				t.Fatalf("scoped runner ended early: %v", runErr)
			case <-deadline.C:
				t.Fatalf("missing frame %s/%s", typ, kind)
			}
		}
	}
	next("daemon_hello", "")
	if err := conn.WriteJSON(protocol.DaemonWelcome{Type: "daemon_welcome", SessionID: "fixture", RecoveryHold: true}); err != nil {
		t.Fatal(err)
	}
	next("session_started", "")
	if err := conn.WriteJSON(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: "fixture", Text: "manual fixture"}); err != nil {
		t.Fatal(err)
	}
	// A settings reply proves the read pump handled a subsequent frame while
	// both the initial and manually delivered prompts are waiting.
	if err := conn.WriteJSON(protocol.SetSessionModel{Type: "set_session_model", SessionID: "fixture", Model: "gpt-5-codex"}); err != nil {
		t.Fatal(err)
	}
	next("agent_event", "model_changed")
	assertNoTurn := func() {
		t.Helper()
		raw, _ := os.ReadFile(logPath)
		if strings.Contains(string(raw), "turn/start") {
			t.Fatal("scoped engine received a held prompt")
		}
	}
	time.Sleep(30 * time.Millisecond)
	assertNoTurn()
	if err := conn.WriteJSON(protocol.DaemonRecoveryReady{Type: "daemon_recovery_ready", SessionID: "foreign"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	assertNoTurn()
	if err := conn.WriteJSON(protocol.DaemonRecoveryReady{Type: "daemon_recovery_ready", SessionID: "fixture"}); err != nil {
		t.Fatal(err)
	}
	next("agent_event", "user_message")
	next("agent_event", "user_message")
	raw, _ := os.ReadFile(logPath)
	if strings.Count(string(raw), "turn/start")+strings.Count(string(raw), "turn/steer") != 2 {
		t.Fatalf("scoped queued prompts did not run exactly once: %s", raw)
	}
}
