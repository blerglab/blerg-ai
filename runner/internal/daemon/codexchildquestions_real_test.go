package daemon

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCodexChildNativeQuestionRealCLI(t *testing.T) {
	if os.Getenv("BLERG_REAL_CODEX") != "1" {
		t.Skip("set BLERG_REAL_CODEX=1 for native child question inference")
	}
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Effort: "low", Emitter: em})
	d.DeveloperInstructions = "This is a disposable native protocol integration test. Do not read or change files, use the shell, read credentials, or access external services. Delegate only as requested. Native request_user_input questions will be answered automatically."
	questions := make(chan string, 4)
	d.AskUser = func(_ context.Context, body string) (string, error) { questions <- body; return "Blue", nil }
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { d.Run(ctx); done <- true }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("native runtime failed to stop")
		}
	}()
	d.Enqueue(`Spawn exactly one subagent that needs the person to pick Blue or Green for a protocol test. The child must send you its question through collaboration messaging and wait for your answer, because native request_user_input is root-only. When you receive the question, YOU must use native request_user_input, asking exactly "Codex subagent asks: Pick a color for the protocol test" with Blue and Green choices. Forward the returned answer to that same child and wait for it to reply CHILD-CHOICE-BLUE, then close it and reply TEST-DONE. Do not use the shell or invent a user response.`, "chat")
	deadline := time.After(80 * time.Second)
	for countKind(em, "turn_done") == 0 {
		select {
		case <-deadline:
			t.Fatalf("native child question timed out; event kinds: %v", em.kinds())
		case <-time.After(50 * time.Millisecond):
		}
	}
	select {
	case body := <-questions:
		if !strings.Contains(body, "Codex subagent") || !strings.Contains(body, "Pick a color") {
			t.Fatal("question did not originate from an owned child")
		}
	default:
		t.Fatalf("native parent did not broker the child question; event kinds: %v", em.kinds())
	}
	if countKind(em, "subagent_started") == 0 || countKind(em, "subagent_done") == 0 {
		t.Fatal("questioning child lifecycle was not tracked")
	}
	t.Log("native parent-mediated child question, callback answer, and completion passed")
}
