package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

type collectEmitter struct {
	mu     sync.Mutex
	events []agent.Event
}

func (c *collectEmitter) Emit(ev agent.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *collectEmitter) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.events))
	for i, e := range c.events {
		out[i] = e.Kind
	}
	return out
}

// fakeClaude writes a stub `claude` executable that emits stream-json.
func fakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/bash
cat <<'EOF'
{"type":"system","subtype":"init","session_id":"cc-sess-1"}
{"type":"assistant","message":{"model":"claude-opus-5","content":[{"type":"text","text":"Reading the card."},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_1","content":"README.md","is_error":false}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}
{"type":"result","subtype":"success","is_error":false,"session_id":"cc-sess-1","result":"Done."}
EOF
# record how we were called so the test can assert --resume
echo "$@" >> "$FAKE_CLAUDE_LOG"
`
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestClaudeCodeDriverTurn(t *testing.T) {
	binDir := fakeClaude(t)
	logPath := filepath.Join(binDir, "calls.log")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CLAUDE_LOG", logPath)

	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
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

	// Second turn resumes the Claude Code session.
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
	if !containsStr(string(raw), "--resume cc-sess-1") {
		t.Errorf("second turn must pass --resume; calls:\n%s", raw)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func countKind(c *collectEmitter, kind string) int {
	n := 0
	for _, k := range c.kinds() {
		if k == kind {
			n++
		}
	}
	return n
}
