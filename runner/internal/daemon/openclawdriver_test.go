package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// fakeOpenclaw writes a stub `openclaw` executable that logs its invocation
// args and prints the real "stable agent-exec JSON envelope" shape captured
// empirically against OpenClaw 2026.9.4 (`openclaw agent exec --json`) —
// see openclawdriver.go's doc comment.
func fakeOpenclaw(t *testing.T, envelopeJSON string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	// The prompt argument itself contains newlines (buildPrompt joins turns
	// with "\n\n"), so a plain `echo "$@" >> log` would make one call look
	// like several lines. Use a record-separator byte instead so tests can
	// split calls reliably regardless of embedded newlines.
	script := "#!/bin/bash\n" +
		`printf '%s\x1e' "$*" >> "$FAKE_OPENCLAW_LOG"` + "\n" +
		"cat <<'EOF'\n" + envelopeJSON + "\nEOF\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	path := filepath.Join(dir, "openclaw")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOpenclawDriverTurn(t *testing.T) {
	binDir := fakeOpenclaw(t, `{"ok":true,"status":"ok","final":"pineapple","payloads":[{"text":"pineapple","mediaUrl":null}],"assistantTurns":1,"model":"qwen3-30b","provider":"mybox","sessionId":"abc-123"}`, 0)
	logPath := filepath.Join(binDir, "calls.log")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_OPENCLAW_LOG", logPath)

	em := &collectEmitter{}
	d := newOpenclawDriver(AgentDriverOpts{Spec: engineRegistry["openclaw"], WorkDir: t.TempDir(), Model: "mybox/qwen3-30b", Emitter: em})
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

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	firstCall := strings.Split(strings.Trim(string(raw), "\x1e"), "\x1e")[0]
	for _, want := range []string{"agent exec", "say a fruit", "--json", "--model mybox/qwen3-30b"} {
		if !strings.Contains(firstCall, want) {
			t.Errorf("first call missing %q: %q", want, firstCall)
		}
	}

	// Second turn must replay the first turn into its prompt (this driver's
	// only continuity mechanism — agent exec itself is stateless).
	d.Enqueue("and another", "chat")
	deadline = time.After(15 * time.Second)
	for countKind(em, "turn_done") < 2 {
		select {
		case <-deadline:
			t.Fatal("second turn_done never arrived")
		case <-time.After(50 * time.Millisecond):
		}
	}
	raw, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.Trim(string(raw), "\x1e"), "\x1e")
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[1], "say a fruit") || !strings.Contains(calls[1], "pineapple") || !strings.Contains(calls[1], "and another") {
		t.Errorf("second call must replay turn 1's user+assistant text: %q", calls[1])
	}
}

func TestOpenclawDriverFailureEmitsError(t *testing.T) {
	binDir := fakeOpenclaw(t, `{"ok":false,"error":{"type":"cli_error","message":"Unknown model: bogus/model"}}`, 1)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_OPENCLAW_LOG", filepath.Join(binDir, "calls.log"))

	em := &collectEmitter{}
	d := newOpenclawDriver(AgentDriverOpts{Spec: engineRegistry["openclaw"], WorkDir: t.TempDir(), Model: "bogus/model", Emitter: em})
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
	defer em.mu.Unlock()
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
		if !strings.Contains(p.Message, "Unknown model: bogus/model") {
			t.Errorf("error message = %q, want it to contain the openclaw failure text", p.Message)
		}
	}
	if !found {
		t.Fatal("no error event emitted for a failed openclaw invocation")
	}
	if sawAssistantText {
		t.Error("assistant_text must not fire on a failed turn")
	}
}

func TestOpenclawContextBudgetChars(t *testing.T) {
	t.Setenv("BLERG_OPENCLAW_CONTEXT_CHARS", "")
	if got := openclawContextBudgetChars(); got != defaultOpenclawContextBudgetChars {
		t.Errorf("unset env: got %d, want default %d", got, defaultOpenclawContextBudgetChars)
	}
	t.Setenv("BLERG_OPENCLAW_CONTEXT_CHARS", "5000")
	if got := openclawContextBudgetChars(); got != 5000 {
		t.Errorf("BLERG_OPENCLAW_CONTEXT_CHARS=5000: got %d", got)
	}
	t.Setenv("BLERG_OPENCLAW_CONTEXT_CHARS", "not-a-number")
	if got := openclawContextBudgetChars(); got != defaultOpenclawContextBudgetChars {
		t.Errorf("garbage env: got %d, want default %d", got, defaultOpenclawContextBudgetChars)
	}
}

func openclawStopFixture(t *testing.T, childFile string) (*openclawDriver, *collectEmitter, func()) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/usr/bin/env python3
import json,sys,time,os,subprocess
if 'first request' in sys.argv[3]:
 if os.getenv('FAKE_OPENCLAW_CHILD'):
  child=subprocess.Popen(['sleep','60'])
  with open(os.environ['FAKE_OPENCLAW_CHILD'],'w') as f:f.write(str(child.pid))
 time.sleep(60)
print(json.dumps({'ok':True,'final':'done'}),flush=True)
`
	if err := os.WriteFile(filepath.Join(bin, "openclaw"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if childFile != "" {
		t.Setenv("FAKE_OPENCLAW_CHILD", childFile)
	}
	em := &collectEmitter{}
	d := newOpenclawDriver(AgentDriverOpts{Spec: engineRegistry["openclaw"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("OpenClaw worker did not stop")
		}
	}
	return d, em, func() { go func() { defer close(done); d.Run(ctx) }(); t.Cleanup(stop) }
}

func TestOpenclawStopDuringStartupDeliversQueuedInput(t *testing.T) {
	d, em, start := openclawStopFixture(t, "")
	entered, release := make(chan struct{}), make(chan struct{})
	var starts atomic.Int32
	d.startCommand = func(cmd *exec.Cmd) error {
		if starts.Add(1) == 1 {
			close(entered)
			<-release
		}
		return cmd.Start()
	}
	start()
	d.Enqueue("first request", "chat")
	<-entered
	d.Interrupt()
	d.EnqueueInput(agent.Input{Text: "next request", ClientID: "next"})
	close(release)
	waitIdle(t, em, 2)
	if em.count("turn_done:interrupted") != 1 || em.count("user:next request") != 1 || countKind(em, "error") != 0 {
		t.Fatal(em.outline())
	}
}

func TestOpenclawStopRefusalKeepsQueuedInputForRetry(t *testing.T) {
	d, em, start := openclawStopFixture(t, "")
	var refuse atomic.Bool
	refuse.Store(true)
	ready := make(chan struct{})
	d.startCommand = func(cmd *exec.Cmd) error {
		cmd.Cancel = func() error {
			if refuse.Load() {
				return errors.New("fixture stop refusal")
			}
			return cmd.Process.Kill()
		}
		err := cmd.Start()
		if err == nil {
			select {
			case <-ready:
			default:
				close(ready)
			}
		}
		return err
	}
	start()
	t.Cleanup(func() { refuse.Store(false) })
	d.Enqueue("first request", "chat")
	<-ready
	d.Interrupt()
	d.EnqueueInput(agent.Input{Text: "next request", ClientID: "next"})
	waitUntil(t, "visible OpenClaw refusal", func() bool { return em.count("error:") > 0 })
	if countKind(em, "turn_done") != 0 || em.count("user:next request") != 0 {
		t.Fatal("refused Stop pretended to finish the turn", em.outline())
	}
	refuse.Store(false)
	d.Interrupt()
	waitIdle(t, em, 2)
	if em.count("turn_done:interrupted") != 1 || em.count("user:next request") != 1 {
		t.Fatal(em.outline())
	}
}

func TestOpenclawStopEndsInheritedShellWait(t *testing.T) {
	childFile := filepath.Join(t.TempDir(), "child")
	d, em, start := openclawStopFixture(t, childFile)
	start()
	d.Enqueue("first request", "chat")
	waitUntil(t, "inherited shell", func() bool { _, err := os.Stat(childFile); return err == nil })
	raw, err := os.ReadFile(childFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	d.Interrupt()
	d.Enqueue("next request", "chat")
	waitIdle(t, em, 2)
	if em.count("turn_done:interrupted") != 1 || em.count("user:next request") != 1 || countKind(em, "error") != 0 {
		t.Fatal(em.outline())
	}
}
