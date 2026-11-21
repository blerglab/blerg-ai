package main

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"testing"
)

func TestRound10DialPreservesAuthenticatedIncompatibilityOnly(t *testing.T) {
	inspect, maintenance := keeperInspect, keeperMaintenance
	t.Cleanup(func() { keeperInspect, keeperMaintenance = inspect, maintenance })
	keeperInspect = func(context.Context, string, shellkeeper.Epoch) (shellkeeper.Snapshot, error) {
		return shellkeeper.Snapshot{}, shellkeeper.ErrRefused
	}
	for _, cause := range []error{nil, context.DeadlineExceeded, shellkeeper.ErrRestartRequired} {
		keeperMaintenance = func(context.Context, string) error { return cause }
		b, err := dialKeeper(t.Context(), "fixture", shellkeeper.Epoch{})
		if b != nil || errors.Is(err, shellruntime.ErrKeeperIncompatible) != errors.Is(cause, shellkeeper.ErrRestartRequired) {
			t.Fatal("wrong keeper diagnostic", cause, err)
		}
	}
}
