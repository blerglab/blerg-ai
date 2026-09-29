// Package secrets validates shared-secret configuration at boot: a copied
// example file or a too-short value must never become a real credential
// (desktop-security I1).
package secrets

import (
	"fmt"
	"strings"
)

const minLen = 16

// Check returns nil when value is usable as a shared secret: at least 16 bytes and not a
// documentation placeholder (case-insensitive prefix "change-me"/"changeme", or exactly
// "secret"/"password"/"token"/"dev"). An empty value passes Check (use Require for
// must-exist secrets). Errors name the variable, never the value.
func Check(name, value string) error {
	if value == "" {
		return nil
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "change-me") || strings.HasPrefix(lower, "changeme") ||
		lower == "secret" || lower == "password" || lower == "token" || lower == "dev" {
		return fmt.Errorf("%s is a placeholder value — generate a real secret: openssl rand -hex 32", name)
	}
	if len(value) < minLen {
		// Deliberately avoids the word "short" — a too-short test value (e.g. "short") would
		// otherwise be a substring of the message itself, tripping the "never echoes the
		// value" check below for reasons that have nothing to do with leaking the value.
		return fmt.Errorf("%s has fewer than %d required bytes — generate a real secret: openssl rand -hex 32", name, minLen)
	}
	return nil
}

// Require is Check, plus an empty value is itself an error.
func Require(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is unset — generate one with: openssl rand -hex 32", name)
	}
	return Check(name, value)
}
