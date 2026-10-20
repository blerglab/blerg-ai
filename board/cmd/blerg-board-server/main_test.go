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

// Run as me: the mint client needs both core's URL and the mint key; the create form's box is
// checked unless the operator turns it off, and a value that is not a boolean refuses to boot.
func TestRunAsMeFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	key := "0123456789abcdef0123456789abcdef"
	m, on, err := runAsMeFromEnv(env(map[string]string{"BLERG_CORE_URL": "http://core", "BLERG_CORE_BOARD_MINT_KEY": key}))
	if err != nil || m == nil || !on {
		t.Fatalf("configured: minter %v on %v err %v", m != nil, on, err)
	}
	for name, vars := range map[string]map[string]string{
		"no key":  {"BLERG_CORE_URL": "http://core"},
		"no core": {"BLERG_CORE_BOARD_MINT_KEY": key},
	} {
		if m, _, err := runAsMeFromEnv(env(vars)); err != nil || m != nil {
			t.Errorf("%s: minter %v err %v, want none and no error", name, m != nil, err)
		}
	}
	for v, want := range map[string]bool{"": true, "true": true, "1": true, "false": false, "0": false, "FALSE": false} {
		_, on, err := runAsMeFromEnv(env(map[string]string{"BLERG_BOARD_DEFAULT_RUN_AS_CREATOR": v}))
		if err != nil || on != want {
			t.Errorf("BLERG_BOARD_DEFAULT_RUN_AS_CREATOR=%q: on %v err %v, want %v", v, on, err, want)
		}
	}
	if _, _, err := runAsMeFromEnv(env(map[string]string{"BLERG_BOARD_DEFAULT_RUN_AS_CREATOR": "sometimes"})); err == nil {
		t.Error("a non-boolean BLERG_BOARD_DEFAULT_RUN_AS_CREATOR must refuse to boot")
	}
}
