package ownershell

import (
	"testing"
)

func TestRound10KeeperIncompatibilityIsDistinctSafeCause(t *testing.T) {
	for _, cause := range []string{"keeper_incompatible", "keeper_unavailable"} {
		if got := StartCause(RefuseStart(ReasonRuntimeStart, cause)); got != cause {
			t.Fatal("keeper cause lost", cause, got)
		}
	}
	if SafeStartCause("keeper_incompatible private-path") != "" {
		t.Fatal("raw diagnostic accepted")
	}
}
