package livedata

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func scope() Scope {
	return Scope{Installation: "00000000-0000-4000-8000-000000000001", Runner: "00000000-0000-4000-8000-000000000002", Owner: "00000000-0000-4000-8000-000000000003", Binding: "00000000-0000-4000-8000-000000000004", Connection: "00000000-0000-4000-8000-000000000005", Tool: "quotes", Hash: strings.Repeat("a", 64), Digest: strings.Repeat("b", 64), Revision: 1, ConnectionGeneration: 1, OwnerEpoch: 1, AuthorityEpoch: 1}
}
func TestCredentialIsolation(t *testing.T) {
	now := time.Unix(1800000000, 0)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := scope()
	c := Claims{s, "blerg-core-live-data", Audience(s.Installation, s.Runner), "poll", now.Unix(), now.Unix(), now.Add(Lifetime).Unix()}
	token, err := Sign(c, "key-1", key, now)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{"key-1": pub}
	if got, err := Verify(token, keys, s.Installation, s.Runner, now); err != nil || got != c {
		t.Fatal("valid token refused")
	}
	for name, change := range map[string]func(*Claims){"owner": func(c *Claims) { c.Scope.Owner = "agent" }, "interval": func(c *Claims) { c.ExpiresAt++ }, "operation": func(c *Claims) { c.Operation = "tools/call" }, "audience": func(c *Claims) { c.Audience = "blerg-runner" }, "future": func(c *Claims) { c.IssuedAt += 31; c.NotBefore = c.IssuedAt }, "expired": func(c *Claims) { c.ExpiresAt = now.Unix() }} {
		t.Run(name, func(t *testing.T) {
			bad := c
			change(&bad)
			if _, err := Sign(bad, "key-1", key, now); err == nil {
				t.Fatal("bad claims signed")
			}
		})
	}
	for name, check := range map[string]func() error{
		"wrong runner":  func() error { _, e := Verify(token, keys, s.Installation, s.Owner, now); return e },
		"wrong install": func() error { _, e := Verify(token, keys, s.Owner, s.Runner, now); return e },
		"unknown key":   func() error { _, e := Verify(token, nil, s.Installation, s.Runner, now); return e },
		"expired":       func() error { _, e := Verify(token, keys, s.Installation, s.Runner, now.Add(Lifetime)); return e },
		"forged": func() error {
			parts := strings.Split(token, ".")
			forged := c
			forged.Scope.Owner = s.Connection
			payload, _ := json.Marshal(forged)
			parts[1] = base64.RawURLEncoding.EncodeToString(payload)
			_, e := Verify(strings.Join(parts, "."), keys, s.Installation, s.Runner, now)
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			if check() == nil {
				t.Fatal("isolation guard missing")
			}
		})
	}
	// Even correctly signed credentials of another type/algorithm are refused.
	for _, h := range []string{`{"alg":"none","typ":"blerg-live-data+jwt","kid":"key-1"}`, `{"alg":"EdDSA","typ":"JWT","kid":"key-1"}`, `{"alg":"EdDSA","typ":"blerg-live-data+jwt","kid":"key-1","jku":"https://example.com"}`, `{"alg":"EdDSA","typ":"JWT","typ":"blerg-live-data+jwt","kid":"key-1"}`} {
		p, _ := json.Marshal(c)
		input := base64.RawURLEncoding.EncodeToString([]byte(h)) + "." + base64.RawURLEncoding.EncodeToString(p)
		forged := input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
		if _, err := Verify(forged, keys, s.Installation, s.Runner, now); err == nil {
			t.Fatal("wrong header accepted")
		}
	}
}
