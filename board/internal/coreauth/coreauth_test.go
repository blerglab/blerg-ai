package coreauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

// fakeRev is a RevocationChecker that is never stale and never revokes
// anything, matching a freshly-refreshed coreauth.Client.
type fakeRev struct{}

func (fakeRev) Revoked(_, _, _, _ string) bool { return false }
func (fakeRev) StaleBeyondCeiling() bool       { return false }

// mint signs a core-minted-style token: header {"alg":"EdDSA","kid":kid} and
// the given claims, exactly like blerg-core would produce.
func mint(t *testing.T, priv ed25519.PrivateKey, kid string, c identity.Claims) string {
	t.Helper()
	hb, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	si := identity.EncodeSigningInput(hb, pb)
	sig := ed25519.Sign(priv, []byte(si))
	return si + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerifyAndSynthesize(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := identity.KeySet{"core-1": pub}
	now := time.Now().Unix()

	claims := identity.Claims{
		Sub:       "agent-42",
		Aud:       "blerg-board",
		Kind:      "agent",
		Caps:      []string{"card.write", "column.write"},
		Lineage:   "lineage-1",
		ExpiresAt: now + 60,
	}
	tok := mint(t, priv, "core-1", claims)

	principal, err := identity.Verify(tok, "blerg-board", keys, fakeRev{}, SensitiveCaps)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	got := Synthesize(principal)
	want := Synthesized{
		Sub:     "agent-42",
		Kind:    "agent",
		Caps:    []string{"card.write", "column.write"},
		Lineage: "lineage-1",
	}
	if got.Sub != want.Sub || got.Kind != want.Kind || got.Lineage != want.Lineage {
		t.Fatalf("Synthesize() = %+v, want %+v", got, want)
	}
	if len(got.Caps) != len(want.Caps) {
		t.Fatalf("Synthesize() caps = %v, want %v", got.Caps, want.Caps)
	}
	for i := range want.Caps {
		if got.Caps[i] != want.Caps[i] {
			t.Fatalf("Synthesize() caps = %v, want %v", got.Caps, want.Caps)
		}
	}
}

func TestVerifyWrongAudienceRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := identity.KeySet{"core-1": pub}
	now := time.Now().Unix()

	// Minted for a different audience (e.g. blerg-runner), presented to board.
	claims := identity.Claims{
		Sub:       "agent-42",
		Aud:       "blerg-runner",
		Kind:      "agent",
		Caps:      []string{"card.write"},
		ExpiresAt: now + 60,
	}
	tok := mint(t, priv, "core-1", claims)

	if _, err := identity.Verify(tok, "blerg-board", keys, fakeRev{}, SensitiveCaps); !errors.Is(err, identity.ErrWrongAudience) {
		t.Fatalf("Verify() err = %v, want ErrWrongAudience", err)
	}
}

// TestClientVerify exercises the Client.Verify wrapper end to end (KeySet +
// RevocationChecker wiring), not just the underlying identity.Verify call.
func TestClientVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient("http://blerg-core.invalid")
	c.keysMu.Lock()
	c.keys = identity.KeySet{"core-1": pub}
	c.keysMu.Unlock()
	c.revMu.Lock()
	c.revAt = time.Now()
	c.revMu.Unlock()

	now := time.Now().Unix()
	good := mint(t, priv, "core-1", identity.Claims{
		Sub: "svc-1", Aud: "blerg-board", Kind: "service",
		Caps: []string{"board.admin"}, ExpiresAt: now + 60,
	})
	synth, err := c.Verify(good)
	if err != nil {
		t.Fatalf("Client.Verify: %v", err)
	}
	if synth.Kind != "service" || synth.Sub != "svc-1" {
		t.Fatalf("Client.Verify() = %+v, want kind=service sub=svc-1", synth)
	}

	badAud := mint(t, priv, "core-1", identity.Claims{
		Sub: "svc-1", Aud: "blerg-runner", Kind: "service", ExpiresAt: now + 60,
	})
	if _, err := c.Verify(badAud); !errors.Is(err, identity.ErrWrongAudience) {
		t.Fatalf("Client.Verify() err = %v, want ErrWrongAudience", err)
	}
}

// TestClientRevocationScopedByIssuedAt: a "sub" entry with revoked_at parsed
// from core's JSON applies only to tokens issued at or before it. A token
// minted by a re-login AFTER a password change / logout-all verifies while
// the cached list still holds the entry (the 60 s poll window); one minted
// before, or in the same second, is ErrRevoked.
func TestClientRevocationScopedByIssuedAt(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	revokedAt := time.Now().Add(-30 * time.Second).Truncate(time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"core-1": base64.RawURLEncoding.EncodeToString(pub)})
	})
	mux.HandleFunc("/revocations", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"kind": "sub", "value": "u1", "revoked_at": revokedAt.Format(time.RFC3339Nano)},
			{"kind": "sub", "value": "legacy"}, // no timestamp: revokes everything
			{"kind": "kid", "value": "core-dead", "revoked_at": revokedAt.Format(time.RFC3339Nano)},
		})
	})
	core := httptest.NewServer(mux)
	defer core.Close()

	c := NewClient(core.URL)
	c.Start(context.Background())

	exp := time.Now().Add(time.Hour).Unix()
	verify := func(sub string, iat int64) error {
		_, err := c.Verify(mint(t, priv, "core-1", identity.Claims{
			Sub: sub, Aud: "blerg-board", Kind: "human", Caps: []string{"card.read"},
			IssuedAt: iat, ExpiresAt: exp,
		}))
		return err
	}
	if err := verify("u1", revokedAt.Unix()-1); !errors.Is(err, identity.ErrRevoked) {
		t.Fatalf("token issued before revocation: %v, want ErrRevoked", err)
	}
	if err := verify("u1", revokedAt.Unix()); !errors.Is(err, identity.ErrRevoked) {
		t.Fatalf("token issued in the revocation's second: %v, want ErrRevoked", err)
	}
	if err := verify("u1", revokedAt.Unix()+1); err != nil {
		t.Fatalf("token issued after revocation: %v, want ok", err)
	}
	if err := verify("legacy", time.Now().Unix()); !errors.Is(err, identity.ErrRevoked) {
		t.Fatalf("entry without revoked_at must revoke a fresh token: %v", err)
	}
	if err := verify("u2", revokedAt.Unix()-1); err != nil {
		t.Fatalf("unrelated sub: %v, want ok", err)
	}
	// A "kid" entry is unconditional (the client only has core-1's key, so use
	// the set directly).
	c.revMu.RLock()
	dead := c.revoked.RevokedFor("core-dead", "", "anyone", "", time.Now().Unix()+3600)
	c.revMu.RUnlock()
	if !dead {
		t.Fatal("kid revocation must apply regardless of iat")
	}
}
