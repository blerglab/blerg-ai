package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// fakeHermes preserves the baseline host fixture API while speaking the native
// gateway protocol. Engine errors are deliberately sanitized by the driver.
func fakeHermes(t *testing.T, stdout string, exitCode int) string {
	t.Helper()
	root := fakeHermesGateway(t)
	t.Setenv("FAKE_HERMES_FINAL", stdout)
	if exitCode != 0 {
		t.Setenv("FAKE_HERMES_FAIL", "1")
	}
	return root
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

	deadline := time.After(15 * time.Second)
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
	// The native gateway exposes actual tool lifecycle events.
	for _, want := range []string{"tool_call", "tool_result"} {
		if !contains(kinds, want) {
			t.Errorf("missing native %s", want)
		}
	}

	em.mu.Lock()
	for _, ev := range em.events {
		if ev.Kind != "assistant_text" {
			continue
		}
		p := ev.Payload.(agent.AssistantTextPayload)
		if p.Done && p.Text != "pineapple" {
			t.Errorf("assistant_text = %q, want %q", p.Text, "pineapple")
		}
	}
	em.mu.Unlock()

	// Both turns stay in the same gateway/native conversation.
	d.Enqueue("another", "chat")
	deadline = time.After(15 * time.Second)
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
		if line == "session.resume" {
			t.Errorf("unexpected resume between turns")
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

	deadline := time.After(15 * time.Second)
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
		if strings.Contains(p.Message, "PRIVATE_SECRET") {
			t.Fatal("raw native error leaked")
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
