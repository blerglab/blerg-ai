package daemon

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSteeringRealCLI drives the streaming engine against the real `claude` binary (a few cents of Haiku).
// Opt-in: BLERG_REAL_CLAUDE=1. It is how the assumptions baked into the fake in claudestream_fake_test.go
// are re-checked against a new CLI version.
func TestSteeringRealCLI(t *testing.T) {
	if os.Getenv("BLERG_REAL_CLAUDE") != "1" {
		t.Skip("set BLERG_REAL_CLAUDE=1 to run against the real claude CLI")
	}
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-haiku-4-5", "", em, nil)
	d.steer.legacy = false
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)

	wait := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(120 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; events: %v", what, em.outline())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	idle := func(turns int) func() bool {
		return func() bool {
			o := em.outline()
			return em.count("turn_done") >= turns && o[len(o)-1] == "status:idle"
		}
	}

	// 1. A message sent while a tool runs is folded into the turn.
	d.Enqueue("Use the Bash tool to run exactly: python3 -c \"import time; time.sleep(12)\" . When it is done reply with the word SLOWDONE.", "chat")
	wait("the tool call", func() bool { return em.count("tool_call") >= 1 })
	d.Enqueue("Also add the word BANANA to your final reply.", "chat")
	wait("the first turn", idle(1))
	o := em.outline()
	if !contains(o, "user:Also add the word BANANA to your final reply.") {
		t.Fatalf("the steering message was never shown: %v", o)
	}
	final := ""
	for _, s := range o {
		if strings.HasPrefix(s, "text:") {
			final = s
		}
	}
	if !strings.Contains(final, "BANANA") {
		t.Fatalf("the final answer should include the steering message's request: %v", o)
	}
	if em.count("error") != 0 {
		t.Fatalf("unexpected error: %v", o)
	}

	// 2. Interrupt with a message queued behind: the turn is cut, the queued message runs next.
	d.Enqueue("Use the Bash tool to run exactly: python3 -c \"import time; time.sleep(60)\" . Then reply LONGDONE.", "chat")
	wait("the second tool call", func() bool { return em.count("tool_call") >= 2 })
	d.Enqueue("Reply with exactly the word TAKEOVER.", "chat")
	time.Sleep(1500 * time.Millisecond)
	d.Interrupt()
	wait("the interrupted turn and the queued one", idle(3))
	o = em.outline()
	if !contains(o, "turn_done:interrupted") || em.count("error") != 0 {
		t.Fatalf("an interrupt is a turn_done 'interrupted', not an error: %v", o)
	}
	if !contains(o, "user:Reply with exactly the word TAKEOVER.") {
		t.Fatalf("the queued message must have run: %v", o)
	}

	// 3. The process survived all of that and still answers.
	d.Enqueue("Reply with exactly the word ALIVE.", "chat")
	wait("the last turn", idle(4))
	if !strings.Contains(strings.Join(em.outline(), "\n"), "ALIVE") {
		t.Fatalf("%v", em.outline())
	}
}
