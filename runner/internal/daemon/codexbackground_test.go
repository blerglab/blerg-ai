package daemon

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

type terminalInventoryWriter struct {
	app     *codexApp
	calls   atomic.Int32
	seen    chan int
	failure string
}

func (w *terminalInventoryWriter) Close() error { return nil }
func (w *terminalInventoryWriter) Write(raw []byte) (int, error) {
	var req struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return 0, err
	}
	n := int(w.calls.Add(1))
	reply := codexRPC{Result: json.RawMessage(`{"data":[{"ItemID":"fixture-call","ProcessID":"fixture-process","Command":"fixture command"}],"nextCursor":null}`)}
	if n == 2 {
		switch w.failure {
		case "malformed":
			reply = codexRPC{Result: json.RawMessage(`invalid`)}
		case "missing":
			reply = codexRPC{Result: json.RawMessage(`{}`)}
		case "null":
			reply = codexRPC{Result: json.RawMessage(`{"data":null}`)}
		default:
			reply = codexRPC{Error: json.RawMessage(`{"code":-32603,"message":"temporary fixture failure"}`)}
		}
	}
	w.app.mu.Lock()
	ch := w.app.pending[req.ID]
	w.app.mu.Unlock()
	ch <- reply
	select {
	case w.seen <- n:
	case <-w.app.ctx.Done():
	}
	return len(raw), nil
}

func TestCodexTerminalInventoryRetriesTransientFailure(t *testing.T) {
	for _, failure := range []string{"rpc", "malformed", "missing", "null"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			testCodexTerminalInventoryRetry(t, failure)
		})
	}
}

func testCodexTerminalInventoryRetry(t *testing.T, failure string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: em})
	d.threadID = "fixture-root"
	a := &codexApp{d: d, ctx: ctx, pending: map[int]chan codexRPC{}, done: make(chan struct{}), terminalPollInterval: 10 * time.Millisecond}
	w := &terminalInventoryWriter{app: a, seen: make(chan int, 8), failure: failure}
	a.in = w
	finished := make(chan struct{})
	go func() { a.backgroundTerminals(); close(finished) }()
	defer func() { cancel(); <-finished }()
	for n := 1; n <= 3; n++ {
		select {
		case got := <-w.seen:
			if got != n {
				t.Fatalf("inventory request %d, want %d", got, n)
			}
		case <-finished:
			t.Fatal("a transient inventory failure permanently stopped tracking")
		case <-time.After(4 * time.Second):
			t.Fatal("terminal inventory was not retried")
		}
	}
	if countKind(em, "background_task_started") != 1 || countKind(em, "background_task_done") != 0 {
		t.Fatal("transient inventory failure changed a still-running process lifecycle")
	}
	cancel()
	<-finished
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, ev := range em.events {
		if p, ok := ev.Payload.(agent.BackgroundTaskDonePayload); ok && p.Summary != "Codex runtime disconnected" {
			t.Fatal("unexpected shutdown reason")
		}
	}
}
