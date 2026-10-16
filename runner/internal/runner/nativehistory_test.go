package runner

import (
	"github.com/blerglab/blerg-ai/runner/internal/nativehistory"
	"testing"
)

func TestNativeHistoryConfigurationUsesTrustedFixedIdentityPath(t *testing.T) {
	t.Setenv("BLERG_RUNNER_NATIVE_HISTORY", "1")
	t.Setenv("BLERG_RUNNER_NATIVE_HISTORY_ACCOUNT", "fixture-account")
	t.Setenv("BLERG_RUNNER_NATIVE_HISTORY_UID", "fixture-volume-uid")
	t.Setenv("BLERG_RUNNER_RESUME", "1")
	cfg := ConfigFromEnv()
	if cfg.NativeHistory == nil || cfg.NativeHistory.Directory != nativehistory.IdentityPath || cfg.NativeHistory.AccountID != "fixture-account" || cfg.NativeHistory.ClaimUID != "fixture-volume-uid" || !cfg.NativeHistory.Resume {
		t.Fatal("native history lost trusted binding")
	}
	t.Setenv("BLERG_RUNNER_NATIVE_HISTORY", "")
	if ConfigFromEnv().NativeHistory != nil {
		t.Fatal("legacy pod unexpectedly uses durable history")
	}
}
