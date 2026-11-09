package livepoll

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

// Lease is durable and fenced. Credentials and provider bodies are never stored.
type Lease struct {
	Attempt  string
	Scope    ld.Scope
	Fence    int64
	Interval int
	Failures int
}
type ScheduleStore interface {
	Claim(context.Context) ([]Lease, error)
	Create(context.Context, ld.Scope, int64) (string, error)
	Complete(context.Context, Lease, json.RawMessage, string, time.Duration) error
	Reconcile(context.Context) error
	Maintain(context.Context) error
}
type Minter interface {
	Mint(context.Context, string, int64, bool) (string, error)
}
type Executor interface {
	Execute(context.Context, string, string) (json.RawMessage, error)
}

type Scheduler struct {
	// Production must supply the reviewed gate explicitly. False does no I/O.
	Enabled  bool
	Store    ScheduleStore
	Minter   Minter
	Executor Executor
	// Ticks makes cancellation and scheduling deterministic in contract tests.
	Ticks <-chan time.Time
}

func retryDelay(interval, failures int) time.Duration {
	floor := time.Duration(max(15, interval)) * time.Second
	delay := floor * time.Duration(1<<min(max(failures, 0), 10))
	return max(floor, min(delay, 15*time.Minute))
}
func terminal(cause string) bool {
	switch cause {
	case "authority_revoked", "authority_mismatch", "authority_epoch_rollback", "credential_revoked", "credential_invalid", "grant_expired", "configuration_mismatch", "tool_changed", "grant_revoke_failed", "attempt_revoke_failed", "connection_needs_sign_in", "mapping_failed", "unsafe_host_opt_in_required", "clock_skew":
		return true
	}
	return false
}

// Tick collapses missed intervals, never spins waiting for a quota or lease.
// Four bounded workers match the durable global concurrency quota.
func (s *Scheduler) Tick(ctx context.Context) error {
	if !s.Enabled {
		return refuse("polling_disabled")
	}
	if s.Store == nil || s.Minter == nil || s.Executor == nil {
		return refuse("scheduler_unavailable")
	}
	if err := s.Store.Reconcile(ctx); err != nil {
		return err
	}
	leases, err := s.Store.Claim(ctx)
	if err != nil {
		return err
	}
	if len(leases) > 4 {
		return refuse("scheduler_claim_limit")
	}
	// Each batch is bounded; workers finish before the next tick can claim again.
	results := make(chan error, len(leases))
	for _, lease := range leases {
		go func(l Lease) { results <- s.execute(ctx, l) }(lease)
	}
	var result error
	for range leases {
		result = errors.Join(result, <-results)
	}
	return result
}
func (s *Scheduler) execute(parent context.Context, l Lease) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	var rows json.RawMessage
	cause := ""
	id, err := s.Store.Create(ctx, l.Scope, l.Fence)
	l.Attempt = id
	if err == nil {
		// Always mint fresh, including after process restart; never persist tokens.
		token, mintErr := s.Minter.Mint(ctx, l.Scope.Binding, l.Scope.Revision, false)
		if mintErr != nil {
			if errors.Is(mintErr, ld.ErrDenied) {
				cause = "authority_revoked"
			} else {
				cause = "authority_unavailable"
			}
		} else {
			rows, err = s.Executor.Execute(ctx, token, id)
			if err != nil {
				cause = FailureCause(err)
				if cause == "unknown" {
					cause = "execution_unavailable"
				}
			}
		}
	} else {
		cause = FailureCause(err)
		if cause == "unknown" {
			cause = "attempt_store_unavailable"
		}
	}
	if ctx.Err() != nil && !terminal(cause) {
		cause = "call_canceled"
		rows = nil
	}
	if cause != "" {
		_ = refuse(cause)
		rows = nil
	}
	finish, cancelFinish := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
	defer cancelFinish()
	delay := retryDelay(l.Interval, l.Failures)
	floor := time.Duration(max(15, l.Interval)) * time.Second
	if delay > floor {
		delay = max(floor, delay*3/4+time.Duration(rand.Int64N(int64(delay/4)+1))) //nolint:gosec // Scheduling jitter is not credential entropy.
	}
	var failure *Failure
	if errors.As(err, &failure) {
		delay = max(delay, failure.retryAfter)
	}
	if err = s.Store.Complete(finish, l, rows, cause, delay); err != nil {
		return err
	}
	return nil
}

// Run has no hidden goroutines and returns only after the current batch finishes.
func (s *Scheduler) Run(ctx context.Context) error {
	if !s.Enabled {
		return refuse("polling_disabled")
	}
	ticks := s.Ticks
	if ticks == nil {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, open := <-ticks:
			if !open {
				return nil
			}
			if err := s.Tick(ctx); err != nil {
				_ = refuse("scheduler_tick_failed")
			}
		}
	}
}

// PurgesSample identifies explicit authority loss, which clears retained rows.
func PurgesSample(cause string) bool {
	switch cause {
	case "no_view_auto_pause", "connection_needs_sign_in", "authority_revoked", "authority_mismatch", "authority_epoch_rollback", "credential_revoked", "grant_expired", "tool_changed", "grant_revoke_failed", "attempt_revoke_failed":
		return true
	}
	return false
}
