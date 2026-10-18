package boardfiling

import (
	"strings"
	"testing"
)

func TestCapabilityOnlyAcceptsOneCanonicalIntentAndDigest(t *testing.T) {
	intent := "12345678-1234-4321-8123-123456789abc"
	digest := strings.Repeat("a", 64)
	capability, err := Capability(intent, digest)
	if err != nil {
		t.Fatal(err)
	}
	gotIntent, gotDigest, ok := ParseCapability([]string{capability})
	if !ok || gotIntent != intent || gotDigest != digest {
		t.Fatal("one-card capability failed roundtrip")
	}
	for _, caps := range [][]string{nil, {}, {capability, "card.read"}, {capability, capability}, {"card.create_once"}, {capability + ":extra"}, {"card.write"}, {strings.Replace(capability, digest, strings.Repeat("g", 64), 1)}} {
		if _, _, ok := ParseCapability(caps); ok {
			t.Fatal("broader or malformed capabilities accepted")
		}
	}
	for _, bad := range []string{"", strings.ToUpper(intent), "bad", intent + "\n", intent + ":extra"} {
		if _, err := Capability(bad, digest); err == nil {
			t.Fatal("invalid intent accepted")
		}
	}
	for _, bad := range []string{"", strings.Repeat("A", 64), strings.Repeat("a", 63), digest + ":extra"} {
		if _, err := Capability(intent, bad); err == nil {
			t.Fatal("invalid digest accepted")
		}
	}
}
