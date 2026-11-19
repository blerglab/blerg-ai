package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// The shipped daemon runs with a private home, fake server and fake engine.
// Opt in with an absolute path to a freshly built cmd/daemon binary.
func TestInputJournalRealDaemon(t *testing.T) {
	binary := os.Getenv("BLERG_TEST_DAEMON_BINARY")
	if binary == "" {
		t.Skip("set BLERG_TEST_DAEMON_BINARY to a freshly built daemon")
	}
	home := t.TempDir()
	state := filepath.Join(home, "state")
	repos := filepath.Join(home, "repos")
	if err := os.MkdirAll(filepath.Join(repos, "proj"), 0700); err != nil {
		t.Fatal(err)
	}
	j, err := newInputJournal(filepath.Join(AgentRecordsDir(state), "inputs"), "journal-process")
	if err != nil {
		t.Fatal(err)
	}
	for i := range inputReceiptLimit {
		id := fmt.Sprint(i)
		in := agent.Input{ClientID: id, Text: "hello", Source: "chat", PlanNudge: "old context"}
		if _, _, err := j.accept(in); err != nil {
			t.Fatal(err)
		}
		ev := agent.Event{ClientEventID: "event-" + id, Ts: time.Now(), Kind: "user_message", Payload: in.Payload()}
		if err := j.settle(ev); err != nil {
			t.Fatal(err)
		}
		if i < inputReceiptLimit-1 {
			if err := j.ack(ev.ClientEventID); err != nil {
				t.Fatal(err)
			}
		}
	}
	connections := make(chan *websocket.Conn, 1)
	ts := newTestServer(t, func(conn *websocket.Conn) { connections <- conn })
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "HOME="+home, "BLERG_RUNNER_DAEMON_STATE_DIR="+state,
		"BLERG_RUNNER_REPOS_ROOT="+repos, "BLERG_RUNNER_DAEMON_TOKEN=test-token",
		"BLERG_RUNNER_SERVER_URL="+ts.wsURL(), "BLERG_RUNNER_DAEMON_NAME=test-daemon",
		"PATH="+fakeClaude(t)+":"+os.Getenv("PATH"), "FAKE_CLAUDE_LOG="+filepath.Join(home, "engine.log"))
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var conn *websocket.Conn
	select {
	case conn = <-connections:
	case <-time.After(20 * time.Second):
		t.Fatal("daemon did not connect")
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	write := func(v any) {
		t.Helper()
		if err := conn.WriteJSON(v); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(kind, client string) protocol.AgentEvent {
		t.Helper()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("waiting for %s: %v", kind, err)
			}
			var ev protocol.AgentEvent
			if json.Unmarshal(raw, &ev) != nil {
				continue
			}
			if ev.Type == kind && kind == "session_started" {
				return ev
			}
			if ev.Type != "agent_event" {
				continue
			}
			var p struct {
				ClientID string `json:"client_id"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			if ev.Kind == kind && (client == "" || p.ClientID == client) {
				return ev
			}
		}
	}
	write(protocol.DaemonWelcome{Type: "daemon_welcome", DaemonID: "test-daemon"})
	write(protocol.SpawnSession{Type: "spawn_session", SessionID: "journal-process", Repo: "proj", Kind: "agent", Engine: "claude"})
	wait("session_started", "")
	// A browser retransmission retains its text and ID; only server context changes.
	write(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: "journal-process", ClientID: "0", Text: "hello", PlanNudge: "new context"})
	if ev := wait("user_message", "0"); ev.ClientEventID != "event-0" {
		t.Fatal("duplicate was executed again")
	}
	path := filepath.Join(j.dir, "journal-process.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	write(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: "journal-process", ClientID: "fresh", Text: "private-process-canary"})
	ev := wait("error", "fresh")
	if !strings.Contains(string(ev.Payload), "receipt_save_failed_") {
		t.Fatalf("missing save cause: %s", ev.Payload)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	write(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: "journal-process", ClientID: "fresh", Text: "private-process-canary"})
	wait("user_message", "fresh")
	reopened, err := newInputJournal(j.dir, "journal-process")
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.state.Receipts) != inputReceiptLimit {
		t.Fatalf("receipt count: %d", len(reopened.state.Receipts))
	}
	found := false
	for _, r := range reopened.state.Receipts {
		if r.ID == fmt.Sprint(inputReceiptLimit-1) && !r.Acked {
			found = true
		}
	}
	if !found {
		t.Fatal("unacknowledged receipt evicted")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if !strings.Contains(logs.String(), "receipt_save_failed_") || strings.Contains(logs.String(), "private-process-canary") {
		t.Fatal("missing typed save log or private text leaked")
	}
}
