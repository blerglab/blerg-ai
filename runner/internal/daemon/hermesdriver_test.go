package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// fakeHermes writes a stub `hermes` executable that logs its invocation args
// and prints stdout/exits per the real `hermes chat --oneshot -Q` contract
// verified against hermes-agent v0.21.2: plain final-response text (or an
// error message) on stdout, nothing structured — see hermesdriver.go's doc
// comment.
func fakeHermes(t *testing.T, stdout string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/bash\n" +
		`echo "$@" >> "$FAKE_HERMES_LOG"` + "\n" +
		"cat <<'EOF'\n" + stdout + "\nEOF\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	path := filepath.Join(dir, "hermes")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHermesDriverTurn(t *testing.T) {
	binDir := fakeHermes(t, "pineapple", 0)
	logPath := filepath.Join(binDir, "calls.log")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_HERMES_LOG", logPath)

	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Model: "anthropic/claude-sonnet-4.6", SessionID: "sess-123", Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	d.Enqueue("say a fruit", "chat")

	deadline := time.After(5 * time.Second)
	for !contains(em.kinds(), "turn_done") {
		select {
		case <-deadline:
			t.Fatalf("no turn_done; kinds so far: %v", em.kinds())
		case <-time.After(50 * time.Millisecond):
		}
	}

	kinds := em.kinds()
	for _, want := range []string{"user_message", "status_changed", "assistant_text", "turn_done"} {
		if !contains(kinds, want) {
			t.Errorf("missing event kind %q in %v", want, kinds)
		}
	}
	// No tool_call/tool_result — documented capability gap for hermes's
	// text-only headless mode.
	for _, unwanted := range []string{"tool_call", "tool_result"} {
		if contains(kinds, unwanted) {
			t.Errorf("unexpected event kind %q — hermes headless mode has no tool event stream", unwanted)
		}
	}

	em.mu.Lock()
	for _, ev := range em.events {
		if ev.Kind != "assistant_text" {
			continue
		}
		p := ev.Payload.(agent.AssistantTextPayload)
		if p.Text != "pineapple" {
			t.Errorf("assistant_text = %q, want %q", p.Text, "pineapple")
		}
	}
	em.mu.Unlock()

	// Second turn continues the SAME named session — no dynamic id to swap,
	// unlike codex.
	d.Enqueue("another", "chat")
	deadline = time.After(5 * time.Second)
	for countKind(em, "turn_done") < 2 {
		select {
		case <-deadline:
			t.Fatal("second turn_done never arrived")
		case <-time.After(50 * time.Millisecond):
		}
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.TrimSpace(string(raw))
	for _, line := range strings.Split(calls, "\n") {
		if !strings.Contains(line, "--continue sess-123") || !strings.Contains(line, "--create-if-missing") {
			t.Errorf("call missing --continue sess-123 --create-if-missing: %q", line)
		}
	}
}

func TestHermesDriverFailureEmitsError(t *testing.T) {
	binDir := fakeHermes(t, "hermes -z: agent failed: no provider configured", 1)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_HERMES_LOG", filepath.Join(binDir, "calls.log"))

	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), SessionID: "sess-456", Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.Enqueue("do the thing", "chat")

	deadline := time.After(5 * time.Second)
	for !contains(em.kinds(), "turn_done") {
		select {
		case <-deadline:
			t.Fatal("no turn_done")
		case <-time.After(50 * time.Millisecond):
		}
	}

	em.mu.Lock()
	found := false
	sawAssistantText := false
	for _, ev := range em.events {
		if ev.Kind == "assistant_text" {
			sawAssistantText = true
		}
		if ev.Kind != "error" {
			continue
		}
		found = true
		p, ok := ev.Payload.(agent.ErrorPayload)
		if !ok {
			t.Fatalf("error payload has unexpected type %T", ev.Payload)
		}
		if !strings.Contains(p.Message, "no provider configured") {
			t.Errorf("error message = %q, want it to contain the hermes failure text", p.Message)
		}
	}
	em.mu.Unlock()
	if !found {
		t.Fatal("no error event emitted for a failed hermes invocation")
	}
	if sawAssistantText {
		t.Error("assistant_text must not fire on a failed turn")
	}
}
