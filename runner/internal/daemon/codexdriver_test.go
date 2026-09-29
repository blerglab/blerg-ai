package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// fakeCodex writes a stub `codex` executable emitting the real event shapes
// captured against codex-cli 0.154.0 (`codex exec --json` /
// `codex exec resume <id> --json`) — see codexdriver.go's doc comment.
func fakeCodex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/bash
cat <<'EOF'
{"type":"thread.started","thread_id":"cx-thread-1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Reading the card."}}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"ls","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"ls","aggregated_output":"README.md\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Done."}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":10,"cache_write_input_tokens":0,"output_tokens":20}}
EOF
# record how we were called so the test can assert "resume cx-thread-1"
echo "$@" >> "$FAKE_CODEX_LOG"
`
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCodexDriverTurn(t *testing.T) {
	binDir := fakeCodex(t)
	logPath := filepath.Join(binDir, "calls.log")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG", logPath)

	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Model: "gpt-5-codex", Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	d.Enqueue("work the card", "chat")

	deadline := time.After(5 * time.Second)
	for {
		kinds := em.kinds()
		if contains(kinds, "turn_done") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no turn_done; kinds so far: %v", kinds)
		case <-time.After(50 * time.Millisecond):
		}
	}

	kinds := em.kinds()
	for _, want := range []string{"user_message", "status_changed", "assistant_text", "tool_call", "tool_result", "turn_done"} {
		if !contains(kinds, want) {
			t.Errorf("missing event kind %q in %v", want, kinds)
		}
	}

	// Second turn resumes the codex thread.
	d.Enqueue("continue", "chat")
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
	if !containsStr(string(raw), "resume cx-thread-1") {
		t.Errorf("second turn must resume the thread; calls:\n%s", raw)
	}
}

// TestCodexDriverFailedCommandMarksToolResultError verifies that a command
// exiting non-zero (status "failed", not "completed") is surfaced as an
// error tool_result — mirrors what codex-cli 0.154.0 actually emits for a
// failing shell command (see codexdriver_test.go's sibling fixture and the
// real capture in codexdriver.go's doc comment).
func TestCodexDriverFailedCommandMarksToolResultError(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/bash
cat <<'EOF'
{"type":"thread.started","thread_id":"cx-thread-2"}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"nope","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"nope","aggregated_output":"not found\n","exit_code":127,"status":"failed"}}
{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1}}
EOF
`
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
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
	defer em.mu.Unlock()
	found := false
	for _, ev := range em.events {
		if ev.Kind != "tool_result" {
			continue
		}
		payload, ok := ev.Payload.(agent.ToolResultPayload)
		if !ok {
			t.Fatalf("tool_result payload has unexpected type %T", ev.Payload)
		}
		found = true
		if !payload.IsError {
			t.Errorf("tool_result.IsError = false for a failed (exit 127) command, want true")
		}
	}
	if !found {
		t.Fatal("no tool_result event emitted")
	}
}
