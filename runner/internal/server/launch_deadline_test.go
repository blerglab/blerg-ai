package server

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestLaunchCreateDeadlinePolicy(t *testing.T) {
	for _, mode := range []string{"default", "shorter_parent", "canceled_parent"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				parent, cancel := context.WithCancel(t.Context())
				defer cancel()
				wantDuration, wantErr := 10*time.Second, context.DeadlineExceeded
				if mode == "shorter_parent" {
					var done context.CancelFunc
					parent, done = context.WithTimeout(parent, time.Second)
					defer done()
					wantDuration = time.Second
				}
				if mode == "canceled_parent" {
					cancel()
					wantDuration, wantErr = 0, context.Canceled
				}
				ctx, done := (&JobManager{}).newLaunchCreateContext(parent)
				defer done()
				deadline, ok := ctx.Deadline()
				// Even an already canceled parent retains the bounded create deadline.
				wantDeadline := start.Add(10 * time.Second)
				if mode == "shorter_parent" {
					wantDeadline = start.Add(time.Second)
				}
				if !ok || !deadline.Equal(wantDeadline) {
					t.Fatalf("create deadline = %v, %v; want %v", deadline, ok, wantDeadline)
				}
				<-ctx.Done()
				if !errors.Is(ctx.Err(), wantErr) || time.Since(start) != wantDuration {
					t.Fatalf("create expired after %v with %v; want %v with %v", time.Since(start), ctx.Err(), wantDuration, wantErr)
				}
			})
		})
	}
}
