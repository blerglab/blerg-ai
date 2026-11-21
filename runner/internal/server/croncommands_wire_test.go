//go:build linux

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/commandsteps"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

type fixedWireSender struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (s *fixedWireSender) Send(value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteJSON(value)
}

// Use the scheduler's real start and serialized spawn, a websocket transport, and
// the daemon's isolated executor. The Docker executable is a local boundary fixture.
func TestFixedCommandServerDaemonWire(t *testing.T) {
	cf := newCronFx(t)
	cf.dc.FixedCommandSteps = true
	steps := []commandsteps.Step{{Argv: []string{"/bin/echo", "wire-health"}, TimeoutSeconds: 2, MaxOutputBytes: 32}}
	definition, _ := commandsteps.ApproveForRuntime(steps, "docker", "", mcpAcct, time.Now())
	c := cf.newCron(func(c *db.Cron) { c.Runtime = "docker"; c.CommandSteps = definition })
	sid, err := cf.svc.Start(context.Background(), c, cf.run(c))
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	select {
	case raw = <-cf.dc.send:
	case <-time.After(time.Second):
		t.Fatal("no spawn")
	}
	bin := t.TempDir()
	dockerLog := filepath.Join(t.TempDir(), "docker-calls")
	docker := `#!/bin/bash
printf '%s\n' "$*" >> ` + "'" + dockerLog + "'" + `
if [ "$1" != exec ]; then exit 0; fi
shift
while [ $# -gt 0 ]; do
 case "$1" in -i|-t|-it) shift;; -w|-e|-u) shift 2;; *) break;; esac
done
shift
case "$1" in kill) exit 0;; sh) case "$3" in *kill*) exit 0;; esac;; esac
exec "$@"
`
	engine := `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"wire-engine"}' '{"type":"result","subtype":"success","is_error":false,"result":"Done"}'
`
	for name, body := range map[string]string{"docker": docker, "claude": engine} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_API_KEY", "fixture-wire-key")
	t.Setenv("BLERG_RUNNER_DAEMON_STATE_DIR", t.TempDir())
	result := make(chan protocol.AgentEvent, 1)
	upgrader := websocket.Upgrader{}
	wire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if conn.WriteMessage(websocket.TextMessage, raw) != nil {
			return
		}
		for {
			var event protocol.AgentEvent
			if conn.ReadJSON(&event) != nil {
				return
			}
			if event.Kind == "cron_command_result" {
				result <- event
				return
			}
		}
	}))
	defer wire.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(wire.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var spawn protocol.SpawnSession
	if err := conn.ReadJSON(&spawn); err != nil {
		t.Fatal(err)
	}
	if spawn.SessionID != sid || !spawn.Sandbox || spawn.CommandSteps.Hash != definition.Hash || spawn.CommandSteps.Runtime != "docker" {
		t.Fatal("spawn changed on wire")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, spawn.Repo), 0o755); err != nil {
		t.Fatal(err)
	}
	host := daemon.NewAgentHost(&fixedWireSender{conn: conn}, daemon.AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir(), ClaudeCode: true, APIKey: "fixture-wire-key"})
	host.Spawn(spawn)
	defer host.Kill(sid)
	select {
	case event := <-result:
		var got commandsteps.Result
		if json.Unmarshal(event.Payload, &got) != nil || got.ExitCode != 0 || got.Output != "wire-health\n" || got.DefinitionHash != definition.Hash {
			t.Fatalf("result: %s", event.Payload)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no fixed command result on wire")
	}
	calls, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "/usr/bin/timeout") {
		t.Fatal("command bypassed docker exec")
	}
}
