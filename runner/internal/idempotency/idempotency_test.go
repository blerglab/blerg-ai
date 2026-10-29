package idempotency

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	for _, key := range []string{"k", "key-1", "AZaz09_.:-", strings.Repeat("k", 128)} {
		if !Valid(key) {
			t.Errorf("valid key %q refused", key)
		}
	}
	for _, key := range []string{" ", " key", "key ", "key with spaces", "\tkey", "key\t", "", strings.Repeat("k", 129), "key@invalid", "key/slash", "key%3A", "key+plus", "key,comma", "key\ttab", "key\n", "key\r", "key\x00", "key\x7f", "key-é"} {
		if Valid(key) {
			t.Errorf("invalid key %q accepted", key)
		}
	}
}

func TestScopeBoundaries(t *testing.T) {
	seen := map[string]string{}
	for _, parts := range [][]string{
		{"marks", "session", "agent:owner", "account"},
		{"marks", "session:agent", "owner", "account"},
		{"marks", "session", "agent", "owner:account"},
		{"marks", "session", "agent", "owner%3Aaccount"},
		{"marks", "session", "agent", "owner%253Aaccount"},
		{"marks", "session", "agent", ""},
		{"marks", "session", "agent"},
		{"marks", "session", "agent", "", ""},
		{"marks", "session", "agent", "é:😀"},
		{"marks", "session", "agent", "é", "😀"},
		{"marks", "session", "agent", "4:part"},
		{"marks", "session", "agent", "v2|4:part"},
	} {
		scope := Scope(parts[0], parts[1:]...)
		description := strings.Join(parts, "|")
		if previous, ok := seen[scope]; ok {
			t.Errorf("%q and %q share scope %q", previous, description, scope)
		}
		seen[scope] = description
		if Scope(parts[0], parts[1:]...) != scope {
			t.Fatal("scope is unstable")
		}
		if !strings.HasPrefix(scope, "v2|") {
			t.Errorf("legacy namespace: %q", scope)
		}
	}
	if got := Scope("cron", "abc"); got != "v2|4:cron3:abc" {
		t.Fatalf("encoding = %q", got)
	}
}
