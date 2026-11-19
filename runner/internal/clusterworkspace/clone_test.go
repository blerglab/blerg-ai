package clusterworkspace

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCloneRetryPolicy(t *testing.T) {
	for _, cause := range []string{"deadline", "network", "rate_limit", "server", "authentication", "not_found", "unknown", "canceled"} {
		t.Run(cause, func(t *testing.T) {
			calls, cleaned := 0, 0
			var delays []time.Duration
			err := retryClone(t.Context(), func() error {
				calls++
				if calls == 3 {
					return nil
				}
				return &CloneFailure{cause}
			}, func() error { cleaned++; return nil }, func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil })
			transient := cause == "deadline" || cause == "network" || cause == "rate_limit" || cause == "server"
			if transient {
				if err != nil || calls != 3 || cleaned != 2 || !reflect.DeepEqual(delays, []time.Duration{2 * time.Second, 4 * time.Second}) {
					t.Fatalf("calls=%d cleanup=%d delays=%v err=%v", calls, cleaned, delays, err)
				}
			} else if calls != 1 || cleaned != 0 || !errors.Is(err, ErrClone) {
				t.Fatalf("permanent failure retried: %d %d %v", calls, cleaned, err)
			}
		})
	}
}

func TestCloneRetryExhaustionAndCancellation(t *testing.T) {
	calls := 0
	err := retryClone(t.Context(), func() error { calls++; return &CloneFailure{"server"} }, func() error { return nil }, func(context.Context, time.Duration) error { return nil })
	if calls != 3 || CloneCause(err) != "server" {
		t.Fatalf("exhaustion %d %v", calls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls = 0
	err = retryClone(ctx, func() error { calls++; return &CloneFailure{"deadline"} }, func() error { t.Fatal("cleanup after cancel"); return nil }, cloneBackoff)
	if calls != 1 || !errors.Is(err, ErrClone) {
		t.Fatalf("cancel %d %v", calls, err)
	}
}

func TestCloneCausePrivacy(t *testing.T) {
	if got := cloneCause(context.DeadlineExceeded, "SECRET"); got != "deadline" {
		t.Fatalf("deadline cause=%s", got)
	}
	if got := cloneCause(context.Canceled, "SECRET"); got != "canceled" {
		t.Fatalf("cancel cause=%s", got)
	}
	for _, tt := range []struct{ out, cause string }{
		{"fatal: unable to access 'https://SECRET@example.com/repo': The requested URL returned error: 429", "rate_limit"},
		{"fatal: unable to access 'SECRET': The requested URL returned error: 503", "server"},
		{"fatal: Authentication failed for 'SECRET'", "authentication"},
		{"fatal: Could not resolve host: SECRET", "network"},
		{"fatal: repository 'SECRET' not found", "not_found"},
	} {
		cause := cloneCause(errors.New("SECRET"), tt.out)
		if cause != tt.cause {
			t.Fatalf("cause %q want %q", cause, tt.cause)
		}
	}
}

func TestCloneSecondaryRateLimit(t *testing.T) {
	if got := cloneCause(errors.New("exit"), "remote: You have exceeded a secondary rate-limit.\nfatal: returned error: 403"); got != "rate_limit" {
		t.Fatalf("secondary limit classified %s", got)
	}
	if got := cloneCause(errors.New("exit"), "fatal: returned error: 403"); got != "authentication" {
		t.Fatalf("auth 403 classified %s", got)
	}
}

func TestClonePrepareBudgetCoversRetries(t *testing.T) {
	if PrepareTimeout < 3*CloneAttemptTimeout+6*time.Second+5*time.Minute {
		t.Fatal("prepare deadline consumes retry budget")
	}
	parent, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	calls := 0
	err := retryClone(parent, func() error {
		calls++
		if calls > 1 {
			return nil
		}
		attempt, done := context.WithTimeout(parent, time.Millisecond)
		defer done()
		<-attempt.Done()
		return &CloneFailure{cloneCause(attempt.Err(), "")}
	}, func() error { return nil }, func(context.Context, time.Duration) error { return nil })
	if err != nil || calls != 2 || parent.Err() != nil {
		t.Fatalf("deadline retry: calls=%d err=%v parent=%v", calls, err, parent.Err())
	}
}

// Observe the actual Runtime parent, including its default when no server deadline exists.
type deadlineGit struct {
	*fakeGit
	t *testing.T
}

func (g *deadlineGit) Prepare(ctx context.Context, m Member, branch string, resume bool) (string, string, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 20*time.Minute {
		g.t.Fatalf("runtime prepare parent deadline = %v", deadline)
	}
	return g.fakeGit.Prepare(ctx, m, branch, resume)
}
func TestRuntimePrepareBudget(t *testing.T) {
	runtime, err := New(testPlan(), &deadlineGit{newFake(), t})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Prepare(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}
