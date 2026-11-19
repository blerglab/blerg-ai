package server

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestWorkspaceFinalEffectsRetryOnlyBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		calls := 0
		err := retryWorkspaceFinalEffects(ctx, func() error {
			calls++
			if calls < 3 {
				return db.ErrLimitResumeBusy
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("concurrent finalizer was not joined: calls=%d err=%v", calls, err)
		}
		failure := errors.New("cleanup failed")
		calls = 0
		err = retryWorkspaceFinalEffects(ctx, func() error { calls++; return failure })
		if !errors.Is(err, failure) || calls != 1 {
			t.Fatalf("cleanup failure hidden: calls=%d err=%v", calls, err)
		}
	})
}

func TestWorkspaceFinalEffectsRetainsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := retryWorkspaceFinalEffects(ctx, func() error { return db.ErrLimitResumeBusy })
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 100*time.Millisecond {
			t.Fatalf("completion wait lost its deadline: elapsed=%s err=%v", time.Since(start), err)
		}
	})
}
