package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestNewRepoGuidanceIsEngineContext(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	oldDelay := newRepoCloneDelay
	newRepoCloneDelay = 0
	t.Cleanup(func() { newRepoCloneDelay = oldDelay })
	for _, initial := range []string{"fix the build", ""} {
		t.Run(fmt.Sprintf("empty=%t", initial == ""), func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\ncase \" $* \" in *' clone '*) echo 'repository not found' >&2; exit 1;; *' push '*) exit 1;; esac\nexec " + git + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			codex := `#!/usr/bin/env python3
import json,os,sys,urllib.request
if '--version' in sys.argv:
 print('codex-cli 0.162.1'); sys.exit(0)
for line in sys.stdin:
 r=json.loads(line); i=r.get('id'); m=r.get('method','')
 if i is None: continue
 if m in ('thread/start','thread/resume'):
  urllib.request.urlopen(urllib.request.Request(os.environ['TEST_NEW_REPO_CONTEXT'],data=json.dumps(r.get('params',{})).encode(),method='POST')).read()
 result={}
 if m=='model/list': result={'data':[],'nextCursor':None}
 if m=='mcpServerStatus/list': result={'data':[],'nextCursor':None}
 if m in ('thread/start','thread/resume'): result={'thread':{'id':'fixture-thread'},'model':'gpt-5-codex','reasoningEffort':'high'}
 if m in ('turn/start','turn/steer'): result={'turn':{'id':'fixture-turn'}}
 print(json.dumps({'id':i,'result':result}),flush=True)
 if m=='turn/start': print(json.dumps({'method':'turn/completed','params':{'threadId':'fixture-thread','turn':{'id':'fixture-turn','status':'completed'}}}),flush=True)
`
			if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(codex), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CODEX_AUTH_JSON", `{"auth_mode":"apikey","OPENAI_API_KEY":"fixture-key"}`)
			t.Setenv("BLERG_RUNNER_MCP_PROMPT_WAIT_S", "0")
			contexts := make(chan []byte, 1)
			frames := make(chan []byte, 512)
			connections := make(chan *websocket.Conn, 1)
			upgrader := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/context" {
					data, e := io.ReadAll(r.Body)
					if e != nil {
						http.Error(w, "read", 500)
						return
					}
					contexts <- data
					return
				}
				if r.URL.Path != "/ws" {
					http.NotFound(w, r)
					return
				}
				c, e := upgrader.Upgrade(w, r, nil)
				if e != nil {
					return
				}
				defer c.Close()
				connections <- c
				for {
					_, raw, e := c.ReadMessage()
					if e != nil {
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
			t.Setenv("TEST_NEW_REPO_CONTEXT", srv.URL+"/context")
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			cfg := Config{SessionID: "fixture", ServerWS: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws", ServerHTTP: srv.URL, Home: t.TempDir(), Repo: "app", GitURL: "https://example.com/org/app.git", NewRepo: true, Engine: "codex", InitialPrompt: initial}
			go func() { done <- Run(ctx, cfg) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					t.Error("runner did not stop")
				}
			}()
			var conn *websocket.Conn
			select {
			case conn = <-connections:
			case <-time.After(15 * time.Second):
				t.Fatal("no connection")
			}
			if err := conn.WriteJSON(protocol.DaemonWelcome{Type: "daemon_welcome", SessionID: "fixture"}); err != nil {
				t.Fatal(err)
			}
			deadline := time.After(30 * time.Second)
			expected := initial
			sent := initial != ""
			for {
				select {
				case raw := <-frames:
					var f struct {
						Type, Kind, Status string
						Payload            json.RawMessage
					}
					if json.Unmarshal(raw, &f) != nil {
						continue
					}
					if f.Type == "session_state_changed" && f.Status == "idle" && !sent {
						// This idle transition proves there was no automatic turn; send the first message.
						expected = "later"
						sent = true
						if err := conn.WriteJSON(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: "fixture", Text: expected}); err != nil {
							t.Fatal(err)
						}
					}
					if f.Type == "agent_event" && f.Kind == "user_message" {
						var p struct{ Text string }
						if json.Unmarshal(f.Payload, &p) != nil {
							t.Fatal("bad message")
						}
						if p.Text != expected || !sent {
							t.Fatalf("user message = %q, want %q", p.Text, expected)
						}
						goto checkContext
					}
				case e := <-done:
					t.Fatalf("runner stopped before prompt: %v", e)
				case <-deadline:
					t.Fatal("missing initial user message or idle transition")
				}
			}
		checkContext:
			select {
			case data := <-contexts:
				var p struct {
					Instructions string `json:"developerInstructions"`
				}
				if json.Unmarshal(data, &p) != nil || !strings.Contains(p.Instructions, "This is a new repository") {
					t.Fatal("new repository guidance missing from engine context")
				}
			case <-deadline:
				t.Fatal("engine context not received")
			}

		})
	}
}
