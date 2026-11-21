package clusterworkspace

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	CloneAttemptTimeout = 5 * time.Minute
	PrepareTimeout      = 25 * time.Minute
)

// CloneFailure carries only a fixed cause. Git diagnostics may contain credentials.
type CloneFailure struct{ Cause string }

func (e *CloneFailure) Error() string { return "repository clone failed: " + e.Cause }
func (e *CloneFailure) Unwrap() error { return ErrClone }

func CloneCause(err error) string {
	var failure *CloneFailure
	if errors.As(err, &failure) {
		return failure.Cause
	}
	return "unknown"
}

func cloneCause(err error, output string) string {
	s := strings.ToLower(output)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case strings.Contains(s, "returned error: 429"), strings.Contains(s, "rate limit"), strings.Contains(s, "secondary rate-limit"), strings.Contains(s, "abuse detection"):
		return "rate_limit"
	case strings.Contains(s, "returned error: 5"):
		return "server"
	case strings.Contains(s, "returned error: 404"), strings.Contains(s, "repository not found"), (strings.Contains(s, "repository ") && strings.Contains(s, "not found")):
		return "not_found"
	case strings.Contains(s, "authentication failed"), strings.Contains(s, "returned error: 403"), strings.Contains(s, "returned error: 401"), strings.Contains(s, "could not read username"), strings.Contains(s, "permission denied"):
		return "authentication"
	case strings.Contains(s, "could not resolve host"), strings.Contains(s, "failed to connect"), strings.Contains(s, "connection refused"), strings.Contains(s, "connection reset"), strings.Contains(s, "connection was closed"), strings.Contains(s, "timed out"), strings.Contains(s, "early eof"), strings.Contains(s, "remote end hung up"):
		return "network"
	default:
		return "unknown"
	}
}

// retryClone retries only transient failures, never authorization or missing refs.
// cleanup owns only a fresh allocation, before any agent has access to it.
func retryClone(ctx context.Context, run func() error, cleanup func() error, wait func(context.Context, time.Duration) error) error {
	for attempt := 0; ; attempt++ {
		err := run()
		if err == nil {
			return nil
		}
		cause := CloneCause(err)
		if attempt == 2 || ctx.Err() != nil || (cause != "deadline" && cause != "network" && cause != "rate_limit" && cause != "server") {
			return err
		}
		if err := cleanup(); err != nil {
			return err
		}
		if err := wait(ctx, time.Duration(1<<attempt)*2*time.Second); err != nil {
			return &CloneFailure{cloneCause(err, "")}
		}
	}
}

func cloneBackoff(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
