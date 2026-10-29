package daemon

import (
	"context"
	"time"
)

// Private diagnostics for native tests, never serialized into explorer replies.
type workspaceStatusTiming func(stage string, elapsed, remaining time.Duration, err error)
type workspaceStatusTimingKey struct{}

func statusObserver(ctx context.Context) func(string, time.Duration, error) {
	sink, ok := ctx.Value(workspaceStatusTimingKey{}).(workspaceStatusTiming)
	if !ok || sink == nil {
		return nil
	}
	return func(stage string, elapsed time.Duration, err error) {
		var remaining time.Duration
		if until, ok := ctx.Deadline(); ok {
			remaining = time.Until(until)
		}
		sink(stage, elapsed, remaining, err)
	}
}
