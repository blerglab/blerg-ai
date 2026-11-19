package db

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestAuthorityFenceDeadlinePolicy(t *testing.T) {
	for _, mode := range []string{"default", "shorter_parent", "canceled_parent"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				parent, cancel := context.WithCancel(WithRuntimeAuthority(t.Context(), "fixture-authority"))
				defer cancel()
				wantDuration, wantErr := 5*time.Second, context.DeadlineExceeded
				wantDeadline := start.Add(5 * time.Second)
				if mode == "shorter_parent" {
					var done context.CancelFunc
					parent, done = context.WithTimeout(parent, time.Second)
					defer done()
					wantDuration, wantDeadline = time.Second, start.Add(time.Second)
				}
				if mode == "canceled_parent" {
					cancel()
					wantDuration, wantErr = 0, context.Canceled
				}
				ctx, done := authorityFenceContext(parent)
				defer done()
				if runtimeAuthorityToken(ctx) != "fixture-authority" {
					t.Fatal("fence lost runtime authority")
				}
				deadline, ok := ctx.Deadline()
				if !ok || !deadline.Equal(wantDeadline) {
					t.Fatalf("fence deadline = %v, %v; want %v", deadline, ok, wantDeadline)
				}
				<-ctx.Done()
				if !errors.Is(ctx.Err(), wantErr) || time.Since(start) != wantDuration {
					t.Fatalf("fence expired after %v with %v; want %v with %v", time.Since(start), ctx.Err(), wantDuration, wantErr)
				}
			})
		})
	}
}
