package main

import (
	"strings"
	"testing"
)

// TestValidateSecrets is R4 (desktop-security I1): a copied install/desktop/.env.example must
// never boot as a live credential. BLERG_BOARD_SERVICE_KEY is required; an unset value must be
// refused naming the variable. BLERG_RUNNER_KEY is optional but still checked for placeholders.
func TestValidateSecrets(t *testing.T) {
	t.Setenv("BLERG_BOARD_SERVICE_KEY", "")
	t.Setenv("BLERG_RUNNER_KEY", "0123456789abcdef0123456789abcdef")
	if err := validateSecrets(); err == nil || !strings.Contains(err.Error(), "unset") {
		t.Fatalf("unset service key must fail naming it as unset, got %v", err)
	}
	t.Setenv("BLERG_BOARD_SERVICE_KEY", "0123456789abcdef0123456789abcdef")
	if err := validateSecrets(); err != nil {
		t.Fatalf("valid secrets: %v", err)
	}
	t.Setenv("BLERG_RUNNER_KEY", "change-me-runner-key")
	if err := validateSecrets(); err == nil || !strings.Contains(err.Error(), "BLERG_RUNNER_KEY") {
		t.Fatalf("placeholder runner key must fail naming the var, got %v", err)
	}
}

// BLERG_BOARD_RUNNER_RUNTIME is read at boot: empty or a runner runtime is
// accepted, anything else refuses to boot naming the variable.
func TestRunnerRuntimeFromEnv(t *testing.T) {
	t.Setenv("BLERG_BOARD_RUNNER_RUNTIME", "")
	if rt, err := runnerRuntimeFromEnv(); err != nil || rt != "" {
		t.Fatalf("unset = %q, %v; want empty, nil", rt, err)
	}
	t.Setenv("BLERG_BOARD_RUNNER_RUNTIME", "daemon")
	if rt, err := runnerRuntimeFromEnv(); err != nil || rt != "daemon" {
		t.Fatalf("daemon = %q, %v", rt, err)
	}
	t.Setenv("BLERG_BOARD_RUNNER_RUNTIME", "sandbox")
	if _, err := runnerRuntimeFromEnv(); err == nil || !strings.Contains(err.Error(), "BLERG_BOARD_RUNNER_RUNTIME") {
		t.Fatalf("unknown value must fail naming the variable, got %v", err)
	}
}
