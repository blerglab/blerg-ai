package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDelegationWaitersReleasePreservesOtherRegistration(t *testing.T) {
	w := newDelegationWaiters()
	first, releaseFirst := w.acquire("request")
	second, releaseSecond := w.acquire("request")
	if first != second {
		t.Fatal("concurrent registrations did not share signal")
	}
	releaseFirst()
	releaseFirst()
	w.mu.Lock()
	refs := w.m["request"].refs
	w.mu.Unlock()
	if refs != 1 {
		t.Fatal("release detached other waiter")
	}
	w.signal("request")
	select {
	case <-second:
	default:
		t.Fatal("remaining waiter was not signaled")
	}
	releaseSecond()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.m) != 0 {
		t.Fatal("finished registrations retained")
	}
}

func TestDelegationWaitersOldReleaseCannotRemoveNewGeneration(t *testing.T) {
	w := newDelegationWaiters()
	old, releaseOld := w.acquire("request")
	w.signal("request")
	current, releaseCurrent := w.acquire("request")
	if old == current {
		t.Fatal("signal generation reused")
	}
	releaseOld()
	w.signal("request")
	select {
	case <-current:
	default:
		t.Fatal("old release detached new generation")
	}
	releaseCurrent()
	releaseCurrent()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.m) != 0 {
		t.Fatal("new generation retained")
	}
}

func delegationRegistrationCount(a *API, id string) int {
	a.delegationWaiters.mu.Lock()
	defer a.delegationWaiters.mu.Unlock()
	if entry := a.delegationWaiters.m[id]; entry != nil {
		return entry.refs
	}
	return 0
}
func waitDelegationRegistrations(t *testing.T, a *API, id string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for delegationRegistrationCount(a, id) != n {
		if time.Now().After(deadline) {
			t.Fatalf("registration count did not reach %d", n)
		}
		time.Sleep(time.Millisecond)
	}
}
func assertNoDelegationRegistrations(t *testing.T, a *API) {
	t.Helper()
	a.delegationWaiters.mu.Lock()
	defer a.delegationWaiters.mu.Unlock()
	if len(a.delegationWaiters.m) != 0 {
		t.Fatal("completed wait retained registrations")
	}
}

func TestDelegationAwaitSettledMissingAndErrorReleaseRegistration(t *testing.T) {
	fx := newDelegFx(t)
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	if code, _ := fx.decide(id, fx.human("owner-1"), choice("deny")); code != 200 {
		t.Fatal("decision failed")
	}
	row, err := fx.api.awaitDelegation(context.Background(), id, time.Second)
	if err != nil || row == nil || row.Status != "denied" {
		t.Fatal("settled request not returned")
	}
	assertNoDelegationRegistrations(t, fx.api)
	row, err = fx.api.awaitDelegation(context.Background(), newUUID(), time.Second)
	if err != nil || row != nil {
		t.Fatal("missing request not returned")
	}
	assertNoDelegationRegistrations(t, fx.api)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fx.api.awaitDelegation(ctx, id, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled database read not propagated")
	}
	assertNoDelegationRegistrations(t, fx.api)
}

func TestDelegationAwaitCanceledCallerCannotDetachAnotherWaiter(t *testing.T) {
	fx := newDelegFx(t)
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan error, 1)
	go func() { _, err := fx.api.awaitDelegation(ctx, id, 5*time.Second); canceled <- err }()
	done := make(chan string, 1)
	go func() {
		row, err := fx.api.awaitDelegation(context.Background(), id, 5*time.Second)
		if err != nil || row == nil {
			done <- "failed"
			return
		}
		done <- row.Status
	}()
	waitDelegationRegistrations(t, fx.api, id, 2)
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation not propagated")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled wait did not return")
	}
	waitDelegationRegistrations(t, fx.api, id, 1)
	if code, _ := fx.decide(id, fx.human("owner-1"), choice("deny")); code != 200 {
		t.Fatal("decision failed")
	}
	select {
	case status := <-done:
		if status != "denied" {
			t.Fatal("other waiter lost decision")
		}
	case <-time.After(time.Second):
		t.Fatal("other waiter detached from decision")
	}
	assertNoDelegationRegistrations(t, fx.api)
}

func TestDelegationAwaitTimeoutReleasesRegistration(t *testing.T) {
	fx := newDelegFx(t)
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	row, err := fx.api.awaitDelegation(context.Background(), id, time.Millisecond)
	if err != nil || row == nil || row.Status != "pending" {
		t.Fatal("timeout lost pending request")
	}
	assertNoDelegationRegistrations(t, fx.api)
}
