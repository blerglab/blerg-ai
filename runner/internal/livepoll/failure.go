package livepoll

import (
	"errors"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"time"
)

// Failure retains only a developer-owned cause, never upstream error content.
// The scheduler uses it to purge on explicit authority loss and back off on a
// transient outage, while the public error remains uniformly denied.
type Failure struct {
	cause      string
	retryAfter time.Duration
}

func (e *Failure) Error() string { return ld.ErrDenied.Error() }
func (e *Failure) Unwrap() error { return ld.ErrDenied }
func FailureCause(err error) string {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.cause
	}
	return "unknown"
}

// SampleCause treats stored diagnostic text as data, never as a log label.
func SampleCause(cause string) string {
	switch cause {
	case "sample_expired", "no_view_auto_pause", "provider_rate_limited", "connection_needs_sign_in", "", "polling_disabled", "authority_revoked", "authority_unavailable", "authority_mismatch", "authority_epoch_rollback", "credential_revoked", "credential_invalid", "grant_expired", "configuration_mismatch", "tool_changed", "mapping_failed", "unsafe_host_opt_in_required", "clock_skew", "keys_unavailable", "tool_listing_unavailable", "grant_revoke_failed", "call_canceled", "provider_unavailable", "invalid_result", "attempt_store_unavailable", "attempt_revoke_failed", "attempt_not_eligible", "attempt_refused", "attempt_commit_failed", "epoch_unavailable", "epoch_store_unavailable", "invalid_attempt", "invalid_scope", "execution_unavailable":
		return cause
	}
	return "source_unavailable"
}

// providerFailure accepts no raw upstream text or unbounded diagnostic label.
func providerFailure(err error, fallback string) error {
	var detail interface {
		LiveFailure() (string, time.Duration)
	}
	cause, delay := fallback, time.Duration(0)
	if errors.As(err, &detail) {
		c, d := detail.LiveFailure()
		switch c {
		case "provider_rate_limited", "connection_needs_sign_in", "invalid_result":
			cause = c
		}
		delay = min(30*24*time.Hour, max(0, d))
	}
	_ = refuse(cause)
	return &Failure{cause: cause, retryAfter: delay}
}
