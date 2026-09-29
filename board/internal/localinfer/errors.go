package localinfer

import (
	"errors"
	"fmt"
)

// Operations, carried on every Error so a log line says which endpoint failed.
const (
	OpChat  = "chat"
	OpEmbed = "embeddings"
)

// The four ways a call fails. Callers switch their own down-policy on these
// with errors.Is — the package never decides for them.
var (
	// ErrNotConfigured: nothing to call. No URL, or (for Embed) no embedding
	// model. Distinct from ErrUnavailable on purpose: nothing is broken, the
	// deployment simply has no local box wired, and a feature that needs one
	// should disable itself rather than retry.
	ErrNotConfigured = errors.New("localinfer: not configured")
	// ErrUnavailable: the endpoint did not answer — connection refused,
	// timeout, cancelled context, HTTP 429 or 5xx. This is the "the box is
	// down" signal; the same call may succeed later.
	ErrUnavailable = errors.New("localinfer: endpoint unavailable")
	// ErrBadRequest: the endpoint answered and refused the call (HTTP 4xx),
	// or the request was unusable before it was sent. Retrying unchanged
	// will fail the same way — usually a wrong model name.
	ErrBadRequest = errors.New("localinfer: request rejected")
	// ErrBadResponse: a 200 whose body we cannot use — not JSON, no choices,
	// an empty embedding. The box is up but not speaking the protocol.
	ErrBadResponse = errors.New("localinfer: unusable response")
)

// Error is what every failed call returns. It matches its Kind and any
// underlying cause under errors.Is, so both of these work:
//
//	errors.Is(err, localinfer.ErrUnavailable)
//	errors.Is(err, context.DeadlineExceeded)
type Error struct {
	Op         string // OpChat | OpEmbed
	StatusCode int    // HTTP status, 0 when no response arrived
	Kind       error  // one of the sentinels above
	Detail     string // what went wrong, in words
	Body       string // truncated server body, when there was one
	Err        error  // underlying cause, if any
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("localinfer %s: %s", e.Op, e.Detail)
	if e.StatusCode != 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	if e.Body != "" {
		msg += ": " + e.Body
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes both the kind and the cause so errors.Is matches either.
func (e *Error) Unwrap() []error {
	var errs []error
	if e.Kind != nil {
		errs = append(errs, e.Kind)
	}
	if e.Err != nil {
		errs = append(errs, e.Err)
	}
	return errs
}

// Unavailable reports whether err means the endpoint could not answer — the
// one-liner for callers whose policy is "skip when the box is down".
func Unavailable(err error) bool { return errors.Is(err, ErrUnavailable) }
