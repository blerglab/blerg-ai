package server

import (
	"context"
	"errors"
	"net"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// A socket write timeout is fatal to Gorilla and must never be retried as a
// database timeout, even when an underlying transport implements net.Error.
type daemonSocketWriteError struct{ error }

func (e *daemonSocketWriteError) Unwrap() error { return e.error }

func daemonAuthorityTimeout(err error) bool {
	var socket *daemonSocketWriteError
	if errors.As(err, &socket) {
		return false
	}
	var network net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout()
}

// Retry admission once under a fresh authority fence. Never replay a handler
// that started, or a frame that finished: the lock-only transaction's failed
// completion cannot undo its application effects. The next frame revalidates.
// Definitive ownership/token refusals always propagate immediately.
func withDaemonAuthority(parent context.Context, stage, id string, run func(func()) error) error {
	for attempt := 0; ; attempt++ {
		completed := false
		err := run(func() { completed = true })
		if err == nil || parent.Err() != nil || !daemonAuthorityTimeout(err) && !errors.Is(err, db.ErrRuntimeFenceBusy) {
			return err
		}
		logDaemonSocketFailure(stage+"_retry", err, id)
		if completed {
			return nil
		}
		if attempt == 1 {
			return err
		}
	}
}
