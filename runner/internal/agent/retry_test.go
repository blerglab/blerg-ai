package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// flakyProvider fails n times with retryable errors, then delegates to fake.
type flakyProvider struct {
	mu       sync.Mutex
	failures int
	inner    *fakeProvider
	calls    int
}

func (p *flakyProvider) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	fail := p.calls <= p.failures
	p.mu.Unlock()
	if fail {
		return nil, &RetryableError{Err: errors.New("overloaded"), RetryAfter: time.Millisecond}
	}
	return p.inner.Stream(ctx, req)
}

func TestRetryableProviderErrorRetries(t *testing.T) {
	rec := &recorder{}
	p := &flakyProvider{failures: 2, inner: &fakeProvider{scripts: [][]StreamEvent{textResponse("ok")}}}
	l := NewLoop(Config{Provider: p, Emitter: rec, Registry: NewRegistry(), Model: "m",
		RetryBase: time.Millisecond})
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	td := waitFor(t, rec, "turn_done").Payload.(TurnDonePayload)
	if td.StopReason != "end_turn" {
		t.Fatalf("turn_done = %+v", td)
	}
	if p.calls != 3 {
		t.Fatalf("want 3 attempts, got %d", p.calls)
	}
	if len(rec.byKind("error")) != 0 {
		t.Fatalf("retried errors must not emit error events: %v", rec.kinds())
	}
}

type errProvider struct{}

func (errProvider) Stream(context.Context, Request) (<-chan StreamEvent, error) {
	return nil, errors.New("401 unauthorized")
}

func TestNonRetryableErrorEmitsErrorAndIdles(t *testing.T) {
	rec := &recorder{}
	l := NewLoop(Config{Provider: errProvider{}, Emitter: rec, Registry: NewRegistry(), Model: "m"})
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitFor(t, rec, "error")
	waitForStatus(t, rec, "idle")
}

type hangingProvider struct{}

func (hangingProvider) Stream(ctx context.Context, _ Request) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		select {
		case ch <- StreamEvent{Kind: "text_delta", TextDelta: "thinking..."}:
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
	}()
	return ch, nil
}

func TestInterruptEndsTurnAsInterrupted(t *testing.T) {
	rec := &recorder{}
	l := NewLoop(Config{Provider: hangingProvider{}, Emitter: rec, Registry: NewRegistry(), Model: "m"})
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitForStatus(t, rec, "running")
	l.Interrupt()
	td := waitFor(t, rec, "turn_done").Payload.(TurnDonePayload)
	if td.StopReason != "interrupted" {
		t.Fatalf("turn_done = %+v", td)
	}
	waitForStatus(t, rec, "idle")
}

func TestBudgetCapEndsSession(t *testing.T) {
	rec := &recorder{}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		textResponse("t1"), textResponse("t2"),
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m",
		BudgetUSD: 0.0000001,
		Pricing:   map[string]Price{"m": {InputPerM: 3, OutputPerM: 15}},
	})
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("turn 1", "chat") // spends ~10 in + 5 out tokens
	waitFor(t, rec, "turn_done")
	l.Enqueue("turn 2", "chat") // over budget before the call
	waitFor(t, rec, "error")
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.Calls) != 1 {
		t.Fatalf("second call should be blocked by budget; calls = %d", len(fp.Calls))
	}
}

// A turn that dies on a provider error is still a turn that ENDED: it emits
// turn_done (stop_reason "error") before parking at idle, like the external
// drivers do. Without it a one-shot session (the runner's auto_stop) would
// hang forever on a transient API failure, because nothing told the server the
// turn was over.
func TestProviderErrorEndsTheTurn(t *testing.T) {
	rec := &recorder{}
	l := NewLoop(Config{Provider: errProvider{}, Emitter: rec, Registry: NewRegistry(), Model: "m"})
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitFor(t, rec, "error")
	td := waitFor(t, rec, "turn_done").Payload.(TurnDonePayload)
	if td.StopReason != "error" {
		t.Fatalf("turn_done = %+v, want stop_reason \"error\"", td)
	}
	waitForStatus(t, rec, "idle")
}
