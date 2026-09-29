package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

// staleNever wraps a RevocationSet as a full IssuedAtRevocationChecker for Verify.
type staleNever struct{ RevocationSet }

func (staleNever) StaleBeyondCeiling() bool { return false }

// TestVerifyRevocationScopedByIssuedAt pins the timestamp rule end to end through Verify:
// a "sub" revocation at T revokes tokens with iat < T and iat == T (equality fails closed),
// but a token minted after T (a re-login following a password change) verifies even while
// the entry is still in the consumer's cached list.
func TestVerifyRevocationScopedByIssuedAt(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := KeySet{"k1": pub}
	notSensitive := func(string) bool { return false }
	hdr := map[string]string{"alg": "EdDSA", "kid": "k1"}
	revokedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	exp := time.Now().Add(time.Hour).Unix()

	set := RevocationSet{}
	set.Add("sub", "u1", revokedAt)
	set.Add("lineage", "L1", revokedAt)
	rev := staleNever{set}

	cases := []struct {
		name string
		c    Claims
		want error
	}{
		{"sub, issued before", Claims{Sub: "u1", Aud: "a", IssuedAt: revokedAt.Unix() - 1, ExpiresAt: exp}, ErrRevoked},
		{"sub, issued same second", Claims{Sub: "u1", Aud: "a", IssuedAt: revokedAt.Unix(), ExpiresAt: exp}, ErrRevoked},
		{"sub, issued after", Claims{Sub: "u1", Aud: "a", IssuedAt: revokedAt.Unix() + 1, ExpiresAt: exp}, nil},
		{"sub, missing iat fails closed", Claims{Sub: "u1", Aud: "a", ExpiresAt: exp}, ErrRevoked},
		{"lineage, issued before", Claims{Sub: "other", Lineage: "L1", Aud: "a", IssuedAt: revokedAt.Unix() - 1, ExpiresAt: exp}, ErrRevoked},
		{"lineage, issued after", Claims{Sub: "other", Lineage: "L1", Aud: "a", IssuedAt: revokedAt.Unix() + 1, ExpiresAt: exp}, nil},
		{"unrelated sub", Claims{Sub: "u2", Aud: "a", IssuedAt: revokedAt.Unix() - 1, ExpiresAt: exp}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := mint(t, priv, hdr, tc.c)
			_, err := Verify(tok, "a", keys, rev, notSensitive)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}

// A kid revocation is never scoped by iat: the key is compromised, every token it signed
// is dead, however recent.
func TestVerifyKidRevocationIsUnconditional(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := KeySet{"k1": pub}
	set := RevocationSet{}
	set.Add("kid", "k1", time.Now().Add(-time.Hour))
	now := time.Now().Unix()
	tok := mint(t, priv, map[string]string{"alg": "EdDSA", "kid": "k1"},
		Claims{Sub: "u1", Aud: "a", IssuedAt: now, ExpiresAt: now + 60})
	if _, err := Verify(tok, "a", keys, staleNever{set}, func(string) bool { return false }); !errors.Is(err, ErrRevoked) {
		t.Fatalf("fresh token under a revoked kid: %v, want ErrRevoked", err)
	}
}

// An entry without a timestamp (a core that predates revoked_at, or JSON that omitted it)
// revokes everything — the backward-safe reading.
func TestVerifyZeroRevokedAtRevokesEverything(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := KeySet{"k1": pub}
	set := RevocationSet{}
	set.Add("sub", "u1", time.Time{})
	now := time.Now().Unix()
	tok := mint(t, priv, map[string]string{"alg": "EdDSA", "kid": "k1"},
		Claims{Sub: "u1", Aud: "a", IssuedAt: now + 3600, ExpiresAt: now + 7200})
	if _, err := Verify(tok, "a", keys, staleNever{set}, func(string) bool { return false }); !errors.Is(err, ErrRevoked) {
		t.Fatalf("token under a zero-timestamp sub revocation: %v, want ErrRevoked", err)
	}
}

// A checker that implements only the legacy RevocationChecker keeps unconditional
// semantics — Verify must not silently treat it as "nothing revoked".
func TestVerifyLegacyCheckerStaysUnconditional(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := KeySet{"k1": pub}
	now := time.Now().Unix()
	tok := mint(t, priv, map[string]string{"alg": "EdDSA", "kid": "k1"},
		Claims{Sub: "u1", Aud: "a", IssuedAt: now, ExpiresAt: now + 60})
	if _, err := Verify(tok, "a", keys, fakeRev{revoked: true}, func(string) bool { return false }); !errors.Is(err, ErrRevoked) {
		t.Fatalf("legacy checker: %v, want ErrRevoked", err)
	}
}

// RevocationSet.Revoked (the legacy method) ignores timestamps entirely.
func TestRevocationSetLegacyRevokedIgnoresTimestamp(t *testing.T) {
	set := RevocationSet{}
	set.Add("sub", "u1", time.Now().Add(-time.Hour))
	if !set.Revoked("k1", "", "u1") {
		t.Fatal("Revoked must be unconditional")
	}
	if set.Revoked("k1", "", "u2") || set.Revoked("k1", "L", "") {
		t.Fatal("unrelated principal reported revoked")
	}
}
