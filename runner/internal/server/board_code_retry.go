package server

import (
	"context"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Interactive reads keep their slots. Reconciliation yields once on transient
// admission pressure, without widening a binding or retrying other refusals.
func readBoardCodeHistory(ctx context.Context, read func() (protocol.CommitResult, error), wait func(context.Context) error) (protocol.CommitResult, bool, error) {
	result, err := read()
	if !errors.Is(err, errCommitBusy) && !errors.Is(err, errWorkspaceBusy) && (err != nil || result.OK || result.ErrorCode != "busy") {
		return result, false, err
	}
	if err := wait(ctx); err != nil {
		return protocol.CommitResult{}, true, err
	}
	result, err = read()
	return result, true, err
}

func boardCodeBusyBackoff(ctx context.Context) error {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
