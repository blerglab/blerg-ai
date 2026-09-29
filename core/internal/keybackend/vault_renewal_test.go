package keybackend

// White-box tests for the k8s-auth renewal loop's timing/retry logic,
// exercised via injected loginFn/renewFn fakes -- this is the seam the
// task-13 brief calls for, since the real Vault Kubernetes auth method
// can't be exercised end-to-end without a real Kubernetes cluster and a
// projected service-account JWT, neither of which is available here.
//
// These tests use real timers with short (tens-of-ms) intervals and
// generous sleeps rather than a fake clock, matching the plain
// time.NewTicker-based loop() this file tests -- the same trade-off
// runner/internal/coreauth.Client's precedent loop makes.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRenewalLoopRenewsOnScheduleAndNotOnEveryTick(t *testing.T) {
	var mu sync.Mutex
	renewCalls := 0
	loginCalls := 0

	v := &VaultTransit{
		transitKey: "test-key",
		token:      "initial-token",
		renewFn: func(_ context.Context, token string) (time.Duration, error) {
			if token != "initial-token" {
				t.Errorf("renewFn called with unexpected token %q", token)
			}
			mu.Lock()
			renewCalls++
			mu.Unlock()
			return time.Hour, nil
		},
		loginFn: func(_ context.Context) (string, time.Duration, error) {
			mu.Lock()
			loginCalls++
			mu.Unlock()
			return "should-not-be-used", time.Hour, nil
		},
	}
	v.startRenewalLoop(20 * time.Millisecond)
	defer v.Stop()

	time.Sleep(95 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if renewCalls < 3 {
		t.Fatalf("expected at least 3 renew calls in ~95ms at a 20ms tick interval, got %d", renewCalls)
	}
	if loginCalls != 0 {
		t.Fatalf("expected zero re-login calls while renew keeps succeeding, got %d", loginCalls)
	}
	if got := v.currentToken(); got != "initial-token" {
		t.Fatalf("token should be unchanged by a successful renew, got %q", got)
	}
}

func TestRenewalLoopReLoginsFromScratchOnRenewFailure(t *testing.T) {
	var mu sync.Mutex
	renewCalls := 0
	loginCalls := 0

	v := &VaultTransit{
		transitKey: "test-key",
		token:      "initial-token",
		renewFn: func(_ context.Context, _ string) (time.Duration, error) {
			mu.Lock()
			renewCalls++
			mu.Unlock()
			return 0, errors.New("simulated lease expired")
		},
		loginFn: func(_ context.Context) (string, time.Duration, error) {
			mu.Lock()
			loginCalls++
			mu.Unlock()
			return "relogged-in-token", time.Hour, nil
		},
	}
	v.startRenewalLoop(15 * time.Millisecond)
	defer v.Stop()

	time.Sleep(60 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if renewCalls == 0 {
		t.Fatal("expected renew to have been attempted at least once")
	}
	if loginCalls == 0 {
		t.Fatal("expected a fresh login after renew failure -- a failed renew must not be silently ignored")
	}
	if got := v.currentToken(); got != "relogged-in-token" {
		t.Fatalf("expected the stale token to be replaced by a fresh login's token, got %q", got)
	}
}

func TestRenewalLoopRetriesLoginOnNextTickAfterReloginFailure(t *testing.T) {
	var mu sync.Mutex
	loginAttempts := 0

	v := &VaultTransit{
		transitKey: "test-key",
		token:      "initial-token",
		renewFn: func(_ context.Context, _ string) (time.Duration, error) {
			return 0, errors.New("renew always fails in this test")
		},
		loginFn: func(_ context.Context) (string, time.Duration, error) {
			mu.Lock()
			loginAttempts++
			n := loginAttempts
			mu.Unlock()
			if n < 3 {
				return "", 0, errors.New("login still failing")
			}
			return "eventually-succeeded-token", time.Hour, nil
		},
	}
	v.startRenewalLoop(15 * time.Millisecond)
	defer v.Stop()

	time.Sleep(150 * time.Millisecond)

	if got := v.currentToken(); got != "eventually-succeeded-token" {
		t.Fatalf("expected the loop to keep retrying login on later ticks until one succeeds, got token %q (attempts=%d)", got, loginAttempts)
	}

	mu.Lock()
	defer mu.Unlock()
	if v.token == "initial-token" {
		t.Fatal("stale initial token should have been replaced once login eventually succeeded")
	}
}

func TestRenewalLoopStopsCleanly(t *testing.T) {
	var mu sync.Mutex
	renewCalls := 0

	v := &VaultTransit{
		transitKey: "test-key",
		token:      "initial-token",
		renewFn: func(_ context.Context, _ string) (time.Duration, error) {
			mu.Lock()
			renewCalls++
			mu.Unlock()
			return time.Hour, nil
		},
		loginFn: func(_ context.Context) (string, time.Duration, error) {
			return "unused", time.Hour, nil
		},
	}
	v.startRenewalLoop(10 * time.Millisecond)
	time.Sleep(35 * time.Millisecond)
	v.Stop()

	mu.Lock()
	callsAtStop := renewCalls
	mu.Unlock()

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if renewCalls != callsAtStop {
		t.Fatalf("expected no further renew calls after Stop(), went from %d to %d", callsAtStop, renewCalls)
	}
}

func TestRenewalIntervalIsWellInsideLeaseTTLAndFloored(t *testing.T) {
	cases := []struct {
		lease time.Duration
		want  time.Duration
	}{
		{lease: time.Hour, want: 30 * time.Minute},
		{lease: 1 * time.Second, want: minRenewalInterval}, // floored: half of 1s is far too aggressive
		{lease: 0, want: minRenewalInterval},               // a zero/garbage lease must not yield a zero interval
	}
	for _, c := range cases {
		if got := renewalInterval(c.lease); got != c.want {
			t.Errorf("renewalInterval(%v) = %v, want %v", c.lease, got, c.want)
		}
	}
}
