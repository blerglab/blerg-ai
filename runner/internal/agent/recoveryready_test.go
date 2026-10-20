package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeRecoveryInputGateAndReconnect(t *testing.T) {
	rec := &recorder{}
	fp := &fakeProvider{scripts: [][]StreamEvent{textResponse("first"), textResponse("second"), textResponse("third")}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "fixture"})
	var held atomic.Bool
	held.Store(true)
	release := make(chan struct{}, 2)
	entered := make(chan struct{}, 2)
	l.SetRecoveryInputGate(func(ctx context.Context) {
		if held.Load() {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}, func() bool { return !held.Load() })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	l.Enqueue("first", "chat")
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("native gate was bypassed")
	}
	if len(rec.byKind("user_message")) != 0 {
		t.Fatal("held native input was executed")
	}
	held.Store(false)
	release <- struct{}{}
	waitForN(t, rec, "turn_done", 1)
	held.Store(true)
	l.Enqueue("second", "chat")
	l.Enqueue("third", "chat")
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("native reconnect gate was bypassed")
	}
	if len(rec.byKind("user_message")) != 1 {
		t.Fatal("native reconnect failed closed")
	}
	held.Store(false)
	release <- struct{}{}
	waitForN(t, rec, "turn_done", 3)
	evs := rec.byKind("user_message")
	for i, want := range []string{"first", "second", "third"} {
		if got := evs[i].Payload.(UserMessagePayload).Text; got != want {
			t.Fatalf("queue order %d: %q", i, got)
		}
	}
}

func TestNativeRecoveryQueueIsBounded(t *testing.T) {
	rec := &recorder{}
	l := NewLoop(Config{Emitter: rec, Registry: NewRegistry(), Model: "fixture"})
	l.SetRecoveryInputGate(func(context.Context) {}, nil)
	for i := range 65 {
		l.EnqueueInput(Input{Text: "fixture", ClientID: fmt.Sprint(i)})
	}
	l.mu.Lock()
	n := len(l.queue)
	l.mu.Unlock()
	if n != 64 {
		t.Fatalf("held queue has %d inputs", n)
	}
	errors := rec.byKind("error")
	if len(errors) != 1 || errors[0].Payload.(ErrorPayload).ClientID != "64" {
		t.Fatalf("overflow did not report exact input: %+v", errors)
	}
}

func TestNativeRecoveryAskAnswerWaitsOffTransport(t *testing.T) {
	rec := &recorder{}
	fm := &fakeMessenger{}
	l := NewLoop(Config{Emitter: rec, Registry: NewRegistry(), Model: "fixture", Messenger: fm})
	release, entered := make(chan struct{}), make(chan struct{})
	l.SetRecoveryInputGate(func(ctx context.Context) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan string, 1)
	go func() { answer, _ := (askTool{l}).Execute(ctx, mustJSON(`{"question":"fixture?"}`)); result <- answer }()
	waitFor(t, rec, "ask")
	delivered := make(chan struct{})
	go func() { l.EnqueueInput(Input{Text: "answer", ClientID: "fixture"}); close(delivered) }()
	select {
	case <-delivered:
	case <-time.After(15 * time.Second):
		t.Fatal("ask answer blocked transport")
	}
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("ask answer bypassed recovery gate")
	}
	select {
	case <-result:
		t.Fatal("ask resumed model work before ready")
	default:
	}
	close(release)
	select {
	case answer := <-result:
		if answer != "answer" {
			t.Fatalf("answer %q", answer)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ready did not release ask")
	}
}
