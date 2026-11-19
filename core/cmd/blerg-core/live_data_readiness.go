package main

import (
	"context"
	"sync/atomic"
	"time"
)

func retryIssuerReadiness(ctx context.Context, syncCheckpoint func(context.Context) error, ready *atomic.Bool, ticks <-chan time.Time) {
	for !ready.Load() {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			work, cancel := context.WithTimeout(ctx, 2*time.Second)
			initializeIssuerReadiness(work, syncCheckpoint, ready)
			cancel()
		}
	}
}

func issuerApprovalEpoch(current int64, ready, fenced *atomic.Bool) int64 {
	if fenced.Load() || !ready.Load() {
		return 0
	}
	return current
}

// Failure leaves an issuer installed but unavailable until a successful retry.
func initializeIssuerReadiness(ctx context.Context, syncCheckpoint func(context.Context) error, ready *atomic.Bool) {
	if err := syncCheckpoint(ctx); err != nil {
		issuerUnavailable()
		return
	}
	ready.Store(true)
}
