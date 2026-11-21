//go:build linux

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/processcontainment"
)

// heldProvider answers every call with one final response, the first only when released.
type heldProvider struct {
	mu      sync.Mutex
	calls   []agent.Request
	started chan struct{}
	release chan struct{}
}

func (p *heldProvider) Stream(ctx context.Context, req agent.Request) (<-chan agent.StreamEvent, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req)
	first := len(p.calls) == 1
	p.mu.Unlock()
	if first {
		close(p.started)
	}
	out := make(chan agent.StreamEvent, 2)
	go func() {
		defer close(out)
		if first {
			select {
			case <-p.release:
			case <-ctx.Done():
				return
			}
		}
		out <- agent.StreamEvent{Kind: "block", Block: &agent.Block{Type: "text", Text: "done"}}
		out <- agent.StreamEvent{Kind: "done", StopReason: "end_turn", Usage: &agent.Usage{}}
	}()
	return out, nil
}

func (p *heldProvider) requests() []agent.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]agent.Request(nil), p.calls...)
}

// A real fork bomb under the real tracer and helper, in a turn that ends just after its notice: the
// notice starts nothing. The person sees it at once, and the agent reads it, once, in Blerg's voice,
// with the next thing it is told anyway.
func TestProcessLimitNoticeNeverStartsATurn(t *testing.T) {
	binary := os.Getenv("BLERG_HOST_HELPER_TEST_BINARY")
	if binary == "" {
		t.Skip("built daemon helper required")
	}
	fixture := filepath.Join(t.TempDir(), "limit")
	if out, err := exec.Command("cc", "-pthread", "-o", fixture, "../processcontainment/testdata/limit.c").CombinedOutput(); err != nil {
		t.Fatalf("compile: %v: %s", err, out)
	}
	const nonce = "0123456789abcdef"
	provider := &heldProvider{started: make(chan struct{}), release: make(chan struct{})}
	sender := &agentTestSender{}
	emitter := newWSEmitter(sender, "s1", nil)
	loop := agent.NewLoop(agent.Config{Provider: provider, Emitter: emitter, Registry: agent.NewRegistry(), Model: "test", MarkerNonce: nonce})
	sess := &agentSession{loop: loop, emitter: emitter}
	h := &AgentHost{sessions: map[string]*agentSession{"s1": sess}}
	go loop.Run(t.Context())

	sess.status.Store("running")
	loop.Enqueue("work", "chat")
	<-provider.started

	marker := filepath.Join(t.TempDir(), "out")
	group := processcontainment.NewGroup(binary, uint32(os.Getuid()), uint32(os.Getgid()))
	told := make(chan struct{}, 16)
	group.OnLimit(func(message string) {
		h.TaskLimit("s1", message)
		told <- struct{}{}
	})
	cmd := exec.Command(fixture, "bomb", marker)
	if err := group.Start(cmd); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = group.Stop(ctx)
	}()
	select {
	case <-told:
	case <-time.After(60 * time.Second):
		t.Fatal("no notice within a minute")
	}

	close(provider.release) // the turn's final response, with the notice already sent
	sender.waitForKind(t, "turn_done")
	sess.status.Store("idle")
	time.Sleep(500 * time.Millisecond)
	if calls := provider.requests(); len(calls) != 1 {
		t.Fatalf("the notice started new model work: %d provider calls", len(calls))
	}
	line := processcontainment.TaskLimitMessage(processcontainment.WriterTaskLimit)
	if note := sender.waitForKind(t, "user_message"); len(note.Payload) == 0 {
		t.Fatal("no transcript line")
	}
	shown := 0
	for _, ev := range sender.agentEvents() {
		if ev.Kind == "user_message" && strings.Contains(string(ev.Payload), line) {
			shown++
		}
	}
	if shown != 1 {
		t.Fatalf("transcript shows the line %d times", shown)
	}

	h.UserInput("s1", agent.Input{Text: "next", Source: "chat"})
	sender.waitForKindCount(t, "turn_done", 2)
	h.UserInput("s1", agent.Input{Text: "and next", Source: "chat"})
	sender.waitForKindCount(t, "turn_done", 3)
	calls := provider.requests()
	if len(calls) != 3 {
		t.Fatalf("%d provider calls", len(calls))
	}
	text := func(req agent.Request) string {
		last := req.Messages[len(req.Messages)-1]
		return last.Blocks[len(last.Blocks)-1].Text
	}
	second, third := text(calls[1]), text(calls[2])
	if !strings.Contains(second, line) || !strings.Contains(second, "[Blerg system message: process_limit · "+nonce+"]") || !strings.HasPrefix(second, "next") {
		t.Fatalf("the next input does not carry the notice: %q", second)
	}
	if strings.Contains(third, line) {
		t.Fatalf("the notice came twice: %q", third)
	}
}
