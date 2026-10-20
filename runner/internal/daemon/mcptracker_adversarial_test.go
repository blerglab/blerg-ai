package daemon

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Done may close asynchronously after cancellation. Expose that permitted
// window explicitly so the actor must handle Err before selecting Done.
type mcpDelayedDoneContext struct {
	context.Context
	canceled atomic.Bool
	done     chan struct{}
}

func (c *mcpDelayedDoneContext) Done() <-chan struct{} { return c.done }
func (c *mcpDelayedDoneContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

type mcpHoldBarrierEmitter struct {
	collectEmitter
	entered, release chan struct{}
	blocked          atomic.Bool
}

func (e *mcpHoldBarrierEmitter) Emit(ev agent.Event) {
	e.collectEmitter.Emit(ev)
	p, ok := ev.Payload.(protocol.MCPStatusPayload)
	if ok && p.Hold != nil && e.blocked.CompareAndSwap(false, true) {
		close(e.entered)
		<-e.release
	}
}

func TestMCPCanceledGateAndPersonReleaseDoNotConsumeFirstInput(t *testing.T) {
	lifetime, cancel := context.WithCancel(context.Background())
	em := &mcpHoldBarrierEmitter{entered: make(chan struct{}), release: make(chan struct{})}
	nativeCtx := &mcpDelayedDoneContext{Context: context.Background(), done: make(chan struct{})}
	source := &mcpFakeSource{probe: mcpProbe{Ready: true, Generation: 1, Servers: []protocol.MCPServerStatus{{Name: "fixture", Status: "pending"}}}}
	tracker := newMCPTracker(lifetime, "claude", source, em, mcpTrackerOptions{Wait: time.Second, Poll: time.Hour, Track: time.Hour})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(em.release) }) }
	t.Cleanup(func() {
		close(nativeCtx.done)
		cancel()
		unblock()
		select {
		case <-tracker.done:
		case <-time.After(15 * time.Second):
			t.Error("tracker did not stop")
		}
	})
	firstDone := make(chan struct{})
	go func() { tracker.beforeFirstPrompt(nativeCtx); close(firstDone) }()
	select {
	case <-em.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("no first hold")
	}
	nativeCtx.canceled.Store(true)
	tracker.release() // Actor cannot choose Done yet, so it must inspect Err itself.
	unblock()
	select {
	case <-firstDone:
	case <-time.After(15 * time.Second):
		t.Fatal("canceled gate did not finish")
	}
	secondDone := make(chan struct{})
	go func() { tracker.beforeFirstPrompt(lifetime); close(secondDone) }()
	waitMCP(t, func() bool {
		select {
		case <-secondDone:
			return true
		default:
			return mcpLatest(&em.collectEmitter).Hold != nil
		}
	})
	select {
	case <-secondDone:
		t.Fatal("cancel/release race consumed first input before it was delivered")
	default:
	}
	tracker.release()
	select {
	case <-secondDone:
	case <-time.After(15 * time.Second):
		t.Fatal("replacement gate did not release")
	}
}
