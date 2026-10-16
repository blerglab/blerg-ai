package daemon

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// TestCodexAppRealCLI exercises the installed CLI with a few short inference
// turns in a disposable workspace. Opt in with BLERG_REAL_CODEX=1.
func TestCodexAppRealCLI(t *testing.T) {
	if os.Getenv("BLERG_REAL_CODEX") != "1" {
		t.Skip("set BLERG_REAL_CODEX=1 to exercise the real Codex CLI")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal("Codex CLI is unavailable")
	}
	workspace := t.TempDir()
	wait := func(what string, em *collectEmitter, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for !ready() {
			if time.Now().After(deadline) {
				// Never include raw engine errors or RPC payloads in test output.
				t.Fatalf("timed out waiting for %s; event kinds: %v", what, em.kinds())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	start := func(thread string) (*codexDriver, *collectEmitter, func()) {
		em := &collectEmitter{}
		d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: workspace, Effort: "low", Emitter: em})
		d.threadID = thread
		d.DeveloperInstructions = "This is a disposable integration test. Follow the user's short test instructions. Do not modify files, read credentials, or access the network. Delegate only when explicitly requested."
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); d.Run(ctx) }()
		stop := func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("Codex driver did not stop after cancellation")
			}
		}
		t.Cleanup(stop)
		wait("runtime capabilities", em, func() bool { return countKind(em, "capabilities") > 0 })
		return d, em, stop
	}

	d, em, stop := start("")
	d.mu.Lock()
	thread, model, effort := d.threadID, d.model, d.effort
	d.mu.Unlock()
	if thread == "" || model == "" || effort != "low" {
		t.Fatalf("runtime configuration missing: thread=%t model=%t effort=%q", thread != "", model != "", effort)
	}
	d.EnqueueInput(agent.Input{Text: "Reply with exactly HEDGEHOG. Do not use tools.", ClientID: "real-first"})
	wait("first answer", em, func() bool { return countKind(em, "turn_done") == 1 })
	if !codexRealAnswerContains(em, "HEDGEHOG") {
		t.Fatal("first answer did not contain the requested marker")
	}
	d.EnqueueInput(agent.Input{Text: "Use the shell tool to run exactly sleep 20. After it completes, reply SLEEP-DONE. Do not use other tools.", ClientID: "real-sleep"})
	wait("harmless sleep tool", em, func() bool { return countKind(em, "tool_call") > 0 })
	d.EnqueueInput(agent.Input{Text: "Change the final answer to STEERED. Do not start another tool.", ClientID: "real-steer"})
	wait("accepted mid-turn steering", em, func() bool { return countKind(em, "user_message") == 3 })
	d.Interrupt()
	wait("interrupted turn", em, func() bool { return countKind(em, "turn_done") == 2 })
	em.mu.Lock()
	interrupted := false
	for _, ev := range em.events {
		if p, ok := ev.Payload.(agent.TurnDonePayload); ok && p.StopReason == "interrupted" {
			interrupted = true
		}
	}
	em.mu.Unlock()
	if !interrupted {
		t.Fatal("interrupt did not produce an interrupted turn result")
	}
	stop()

	resumed, next, _ := start(thread)
	resumed.EnqueueInput(agent.Input{Text: "Do not use any tools. Reply with the animal marker from the first user message, and nothing else.", ClientID: "real-resume"})
	wait("same-thread resumed answer", next, func() bool { return countKind(next, "turn_done") == 1 })
	resumed.mu.Lock()
	same := resumed.threadID == thread
	resumed.mu.Unlock()
	if !same || !codexRealAnswerContains(next, "HEDGEHOG") {
		t.Fatal("resumed driver did not preserve the conversation")
	}
	resumed.EnqueueInput(agent.Input{Text: "Spawn exactly one subagent tasked only to reply CHILD-OK without using tools. Wait for it to finish. Send a follow-up task to that SAME subagent asking it to reply FOLLOWUP-OK without tools, and wait for it to finish again. Then close it and reply CHILD-DONE. Do not spawn a second subagent or do anything else.", ClientID: "real-child"})
	wait("child agent turn", next, func() bool { return countKind(next, "turn_done") == 2 })
	if countKind(next, "subagent_started") < 2 || countKind(next, "subagent_done") < 2 {
		t.Fatal("initial and resumed child lifecycles were not reported")
	}
	childText := false
	starts := []agent.SubagentPayload{}
	next.mu.Lock()
	for _, ev := range next.events {
		if ev.Kind == "subagent_started" {
			starts = append(starts, ev.Payload.(agent.SubagentPayload))
		}
		if p, ok := ev.Payload.(agent.AssistantTextPayload); ok && p.ParentCallID != "" {
			childText = true
		}
	}
	next.mu.Unlock()
	if len(starts) != 2 || !starts[1].Resumed || starts[1].ResumedFrom != starts[0].ChildID || starts[1].ChildID == starts[0].ChildID {
		t.Fatal("same-child follow-up did not create a distinct resumed run")
	}
	t.Logf("real Codex configuration, capabilities, answer, steering, interruption, resume, and child lifecycle passed; child text routed: %t", childText)
}

func codexRealAnswerContains(em *collectEmitter, want string) bool {
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, ev := range em.events {
		if p, ok := ev.Payload.(agent.AssistantTextPayload); ok && p.Done && strings.Contains(p.Text, want) {
			return true
		}
	}
	return false
}
