package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestCodexTerminalRecoveryDoesNotReplayWhenHistoryIsMissing(t *testing.T) {
	sender := newRecordingSender(4)
	m := &Manager{client: sender}
	m.recoverSession(SessionRecord{SessionID: "lost-terminal", Engine: "codex", InitialPrompt: "never replay this task"})
	msg := <-sender.ch
	state, ok := msg.(protocol.SessionStateChanged)
	if !ok || state.Status != "error" || state.Message == nil {
		t.Fatal("missing history must explicitly refuse recovery")
	}
}

func TestCodexTerminalHomeSeparatesConversations(t *testing.T) {
	source, state := t.TempDir(), t.TempDir()
	for _, name := range []string{"auth.json", "config.toml"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := prepareCodexTerminalHome(state, "one", source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareCodexTerminalHome(state, "two", source)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("terminal conversations share a home")
	}
	if info, err := os.Lstat(filepath.Join(first, "sessions")); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("sessions must be a private real directory")
	}
	if got, err := prepareCodexTerminalHome(state, "one", source); err != nil || got != first {
		t.Fatal("restart changed the conversation home")
	}
	if _, err := prepareCodexTerminalHome(state, "../escape", source); err == nil {
		t.Fatal("unsafe session identity accepted")
	}
}

func TestCodexTerminalRecoveryChoosesOnlyItsLatestInteractiveThread(t *testing.T) {
	home := t.TempDir()
	write := func(name, payload string) {
		t.Helper()
		path := filepath.Join(home, "sessions", name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := "00000000-0000-4000-8000-000000000001"
	latest := "00000000-0000-4000-8000-000000000002"
	child := "00000000-0000-4000-8000-000000000003"
	write("rollout-old-"+old+".jsonl", `{"type":"session_meta","payload":{"id":"`+old+`","timestamp":"2026-01-01T00:00:00Z","source":"cli"}}`)
	write("rollout-new-"+latest+".jsonl", `{"type":"session_meta","payload":{"id":"`+latest+`","timestamp":"2026-01-02T00:00:00Z","source":"cli"}}`)
	write("rollout-child-"+child+".jsonl", `{"type":"session_meta","payload":{"id":"`+child+`","timestamp":"2026-01-03T00:00:00Z","source":{"subagent":{}}}}`)
	write("broken.jsonl", "not-json")
	if got := codexTerminalThread(home); got != latest {
		t.Fatalf("thread = %q, want latest interactive", got)
	}
}
