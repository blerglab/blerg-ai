package main

import (
	"strings"
	"testing"
)

// TestValidateSecrets is R4 (desktop-security I1): a copied install/desktop/.env.example must
// never boot as a live credential. BLERG_RUNNER_DAEMON_TOKEN is required; a too-short value must
// be refused naming the variable and the required byte count.
func TestValidateSecrets(t *testing.T) {
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "short")
	if err := validateSecrets(); err == nil || !strings.Contains(err.Error(), "16") {
		t.Fatalf("too-short daemon token must fail naming the required length, got %v", err)
	}
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "0123456789abcdef0123456789abcdef")
	if err := validateSecrets(); err != nil {
		t.Fatalf("valid secrets: %v", err)
	}
}
