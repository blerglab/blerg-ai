package db

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"runtime"
	"testing"
	"time"
)

func TestBlackboardRound6QueueDepthBound(t *testing.T) {
	store := BlackboardStore{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	leave, err := store.waitAutomaticWrite(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	waitCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 32)
	for range 32 {
		go func() {
			release, e := store.automaticWriteTimer(waitCtx, t.Name(), false, func() (<-chan time.Time, func()) { return make(chan time.Time), func() {} })
			if e == nil {
				release()
			}
			done <- e
		}()
	}
	for store.AutomaticWriteWaiters(t.Name()) != 32 {
		select {
		case e := <-done:
			t.Fatalf("waiter finished early: %v", e)
		case <-ctx.Done():
			t.Fatal("waiters failed to enter")
		default:
			runtime.Gosched()
		}
	}
	release, err := store.automaticWriteTimer(ctx, t.Name(), false, func() (<-chan time.Time, func()) {
		t.Error("full queue armed a wait timer")
		return make(chan time.Time), func() {}
	})
	if err == nil {
		release()
	}
	if !errors.Is(err, blackboard.ErrConflict) {
		t.Errorf("full queue should refuse immediately: %v", err)
	}
	stop()
	for range 32 {
		<-done
	}
}

func TestBlackboardRound6QueueDeadlineAndTryOnly(t *testing.T) {
	s := BlackboardStore{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	leave, err := s.waitAutomaticWrite(ctx, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	expired := make(chan time.Time, 1)
	armed := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		release, e := s.automaticWriteTimer(ctx, t.Name(), false, func() (<-chan time.Time, func()) { close(armed); return expired, func() {} })
		if e == nil {
			release()
		}
		done <- e
	}()
	select {
	case <-armed:
	case <-ctx.Done():
		t.Fatal("deadline was not armed")
	}
	expired <- time.Time{}
	if err := <-done; !errors.Is(err, blackboard.ErrConflict) {
		t.Fatalf("deadline must refuse: %v", err)
	}
	release, err := s.automaticWriteTimer(ctx, t.Name(), true, func() (<-chan time.Time, func()) {
		t.Error("try-only write armed a wait timer")
		return expired, func() {}
	})
	if err == nil {
		release()
	}
	if !errors.Is(err, blackboard.ErrConflict) {
		t.Fatalf("try-only must refuse: %v", err)
	}
}
