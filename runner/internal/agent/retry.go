package agent

import (
	"context"
	"errors"
	"time"
)

// RetryableError marks a provider error as retryable (429/5xx/timeouts).
// The Anthropic provider wraps errors; the loop retries with exponential
// backoff, honoring RetryAfter when set.
type RetryableError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

const maxAttempts = 5

// callWithRetry invokes Provider.Stream with exponential backoff on
// *RetryableError. Returns the stream channel or the final error.
func (l *Loop) callWithRetry(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	base := l.cfg.RetryBase
	if base <= 0 {
		base = time.Second
	}
	backoff := base
	for attempt := 1; ; attempt++ {
		ch, err := l.cfg.Provider.Stream(ctx, req)
		if err == nil {
			return ch, nil
		}
		var re *RetryableError
		if !errors.As(err, &re) || attempt >= maxAttempts {
			return nil, err
		}
		wait := backoff
		if re.RetryAfter > 0 {
			wait = re.RetryAfter
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		backoff *= 2
	}
}
