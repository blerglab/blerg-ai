package ownershell

import "errors"

// RefuseBeforeAllocation is used only for a transport-correlated runtime reply
// that positively confirms no reservation, helper or process was created.
// Timeouts, failed bootstraps and cleanup failures must never use this marker.
type preallocationRefusal struct{ error }

func (r preallocationRefusal) Unwrap() error { return r.error }

func RefuseBeforeAllocation(err error) error { return preallocationRefusal{err} }
func BeforeAllocation(err error) bool {
	var refused preallocationRefusal
	return errors.As(err, &refused)
}
