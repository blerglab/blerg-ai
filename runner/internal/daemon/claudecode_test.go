package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
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
# record how we were called so the test can assert --resume; a system prompt passed as a file is
# logged as its text, so the prompt reads the same whichever way it came
args=()
for a in "$@"; do
  case "$a" in
    --append-system-prompt-file=*) args+=("--append-system-prompt=$(cat "${a#*=}")") ;;
    *) args+=("$a") ;;
  esac
done
echo "${args[@]}" >> "$FAKE_CLAUDE_LOG"
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

	deadline := time.After(15 * time.Second)
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
	if !containsStr(string(raw), "--resume cc-sess-1") {
		t.Errorf("second turn must pass --resume; calls:\n%s", raw)
	}
}

// Barriers keep Stop inside preparation/Start until its prompt return is observed.
func TestClaudeLegacyInterruptDuringStart(t *testing.T) {
	for _, phase := range []string{"preparation", "before-start", "after-start", "running"} {
		t.Run(phase, func(t *testing.T) {
			binDir := fakeClaude(t)
			t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
			t.Setenv("BLERG_CLAUDE_STEERING", "0")
			d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", &collectEmitter{}, nil)
			d.steer.legacy = true
			ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			preparationCancelled := make(chan struct{})
			oldHelp := ccHelpText
			defer func() { ccHelpText = oldHelp }()
			ccHelpText = func(ctx context.Context, _ sandboxExec, _ string, _ []string) ([]byte, error) {
				if phase == "preparation" {
					close(ready)
					select {
					case <-ctx.Done():
						close(preparationCancelled)
					case <-release:
					}
					return nil, ctx.Err()
				}
				return nil, nil
			}
			var process *exec.Cmd
			var interrupts atomic.Int32
			d.useCommandBoundary(func(cmd *exec.Cmd) error {
				if phase == "preparation" {
					t.Error("stopped preparation launched a process")
					return context.Canceled
				}
				cmd.Path, cmd.Args = "/bin/sleep", []string{"sleep", "30"}
				process = cmd
				cmd.Cancel = func() error {
					interrupts.Add(1)
					if phase == "running" {
						<-release
					}
					return cmd.Process.Kill()
				}
				if phase == "before-start" {
					close(ready)
					<-release
					return cmd.Start()
				}
				err := cmd.Start()
				if phase == "after-start" {
					close(ready)
					<-release
				}
				return err
			})
			if phase == "running" {
				d.onEngine = func(_ int, running bool) {
					if running {
						close(ready)
					}
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			stopped := make(chan struct{})
			work := make(chan bool, 1)
			queried := false
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer func() {
				cancel()
				unblock()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("turn goroutine did not join")
				}
				select {
				case <-stopped:
				case <-time.After(5 * time.Second):
					t.Error("Stop goroutine did not join")
				}
				if queried {
					select {
					case <-work:
					case <-time.After(5 * time.Second):
						t.Error("work query did not join")
					}
				}
			}()
			go func() { d.runTurn(ctx, "hello"); close(done) }()
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				close(stopped)
				t.Fatal("barrier not reached")
			}
			queried = true
			go func() { work <- d.HasInterruptibleWork() }()
			select {
			case active := <-work:
				if !active {
					t.Error("accepted turn is not interruptible")
				}
				work <- active
			case <-time.After(500 * time.Millisecond):
				t.Error("work query blocked behind Start")
			}
			go func() { d.Interrupt(); d.Interrupt(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(500 * time.Millisecond):
				t.Error("Stop blocked behind preparation/Start")
			}
			if phase == "preparation" {
				select {
				case <-preparationCancelled:
				case <-time.After(500 * time.Millisecond):
					t.Error("Stop did not cancel preparation context")
				}
			}
			unblock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Stop did not cancel the accepted turn")
			}
			if process != nil && (process.ProcessState == nil || process.ProcessState.Success()) {
				t.Error("started process was not cancelled")
			}
			if d.HasInterruptibleWork() {
				t.Error("finished turn remains interruptible")
			}
			if phase != "preparation" && interrupts.Load() != 1 {
				t.Errorf("interrupt delivered %d times, want once", interrupts.Load())
			}

		})
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
