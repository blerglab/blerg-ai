package coreauth

import (
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

// mint signs claims into a compact "header.payload.sig" token, mirroring
// contracts/identity's own test helper.
func mint(t *testing.T, priv ed25519.PrivateKey, kid string, c identity.Claims) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid})
	pb, _ := json.Marshal(c)
	si := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	sig := ed25519.Sign(priv, []byte(si))
	return si + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newTestCore(t *testing.T, pub ed25519.PublicKey, revocations []revocationEntry) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwks := map[string]string{"k1": base64.RawURLEncoding.EncodeToString(pub)}
		json.NewEncoder(w).Encode(jwks)
	})
	mux.HandleFunc("/revocations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(revocations)
	})
	return httptest.NewServer(mux)
}

func TestClientVerifiesCoreIssuedToken(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := newTestCore(t, pub, nil)
	defer srv.Close()

	c := New(srv.URL)
	if c == nil {
		t.Fatal("New returned nil for non-empty baseURL")
	}

	notSensitive := func(string) bool { return false }
	now := time.Now().Unix()

	good := mint(t, priv, "k1", identity.Claims{
		Sub: "svc:blerg-board", Aud: "blerg-runner", Kind: "service",
		Caps: []string{"runner.start"}, ExpiresAt: now + 60,
	})
	if _, err := identity.Verify(good, "blerg-runner", c.KeySet(), c, notSensitive); err != nil {
		t.Fatalf("expected valid core-issued token to verify, got: %v", err)
	}

	wrongAud := mint(t, priv, "k1", identity.Claims{
		Sub: "svc:blerg-board", Aud: "blerg-board", Kind: "service",
		Caps: []string{"runner.start"}, ExpiresAt: now + 60,
	})
	if _, err := identity.Verify(wrongAud, "blerg-runner", c.KeySet(), c, notSensitive); !errors.Is(err, identity.ErrWrongAudience) {
		t.Fatalf("expected ErrWrongAudience, got: %v", err)
	}
}

func TestClientHonorsRevocations(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := newTestCore(t, pub, []revocationEntry{{Kind: "sub", Value: "svc:blerg-board"}})
	defer srv.Close()

	c := New(srv.URL)
	notSensitive := func(string) bool { return false }
	now := time.Now().Unix()

	tok := mint(t, priv, "k1", identity.Claims{
		Sub: "svc:blerg-board", Aud: "blerg-runner", Kind: "service", ExpiresAt: now + 60,
	})
	if _, err := identity.Verify(tok, "blerg-runner", c.KeySet(), c, notSensitive); !errors.Is(err, identity.ErrRevoked) {
		t.Fatalf("expected ErrRevoked, got: %v", err)
	}
}

func TestNewWithEmptyBaseURLIsInert(t *testing.T) {
	if c := New(""); c != nil {
		t.Fatal("New(\"\") should return nil so the caller's auth branch is inert")
	}
}

// TestClientRevocationScopedByIssuedAt: a "sub" entry with revoked_at parsed
// from core's JSON applies only to tokens issued at or before it. A token
// minted by a re-login AFTER a password change / logout-all verifies while
// the cached list still holds the entry (the 60 s poll window); one minted
// before, or in the same second, is ErrRevoked. An entry without a timestamp
// keeps revoking everything.
func TestClientRevocationScopedByIssuedAt(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	revokedAt := time.Now().Add(-30 * time.Second).Truncate(time.Second)
	srv := newTestCore(t, pub, []revocationEntry{
		{Kind: "sub", Value: "u1", RevokedAt: revokedAt},
		{Kind: "sub", Value: "legacy"},
		{Kind: "kid", Value: "k-dead", RevokedAt: revokedAt},
	})
	defer srv.Close()

	c := New(srv.URL)
	notSensitive := func(string) bool { return false }
	exp := time.Now().Add(time.Hour).Unix()
	verify := func(sub string, iat int64) error {
		_, err := identity.Verify(mint(t, priv, "k1", identity.Claims{
			Sub: sub, Aud: "blerg-runner", Kind: "human", Caps: []string{"session.start"},
			IssuedAt: iat, ExpiresAt: exp,
		}), "blerg-runner", c.KeySet(), c, notSensitive)
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
	if !c.RevokedFor("k-dead", "", "anyone", time.Now().Unix()+3600) {
		t.Fatal("kid revocation must apply regardless of iat")
	}
}
