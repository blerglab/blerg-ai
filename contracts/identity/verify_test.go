package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeRev struct {
	stale   bool
	revoked bool
}

func (f fakeRev) Revoked(_, _, _, _ string) bool { return f.revoked }
func (f fakeRev) StaleBeyondCeiling() bool       { return f.stale }

// mint is a test helper that signs claims with a header we control (to forge alg/kid).
func mint(t *testing.T, priv ed25519.PrivateKey, hdr map[string]string, c Claims) string {
	t.Helper()
	hb, _ := json.Marshal(hdr)
	pb, _ := json.Marshal(c)
	si := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	sig := ed25519.Sign(priv, []byte(si))
	return si + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := KeySet{"k1": pub}
	notSensitive := func(string) bool { return false }
	sensitive := func(c string) bool { return c == "secrets.read" }
	now := time.Now().Unix()

	good := mint(t, priv, map[string]string{"alg": "EdDSA", "kid": "k1"},
		Claims{Sub: "u1", Aud: "blerg-board", Caps: []string{"card.write"}, ExpiresAt: now + 60})

	if _, err := Verify(good, "blerg-board", keys, fakeRev{}, notSensitive); err != nil {
		t.Fatalf("good token: %v", err)
	}
	// alg=none rejected
	none := mint(t, priv, map[string]string{"alg": "none", "kid": "k1"}, Claims{Aud: "blerg-board", ExpiresAt: now + 60})
	if _, err := Verify(none, "blerg-board", keys, fakeRev{}, notSensitive); !errors.Is(err, ErrBadAlg) {
		t.Fatalf("alg=none: err = %v, want ErrBadAlg", err)
	}
	// unknown kid rejected
	unk := mint(t, priv, map[string]string{"alg": "EdDSA", "kid": "nope"}, Claims{Aud: "blerg-board", ExpiresAt: now + 60})
	if _, err := Verify(unk, "blerg-board", keys, fakeRev{}, notSensitive); !errors.Is(err, ErrUnknownKid) {
		t.Fatalf("unknown kid: err = %v, want ErrUnknownKid", err)
	}
	// wrong audience rejected (cross-component replay)
	if _, err := Verify(good, "blerg-runner", keys, fakeRev{}, notSensitive); !errors.Is(err, ErrWrongAudience) {
		t.Fatalf("wrong aud: err = %v, want ErrWrongAudience", err)
	}
	// expired rejected
	old := mint(t, priv, map[string]string{"alg": "EdDSA", "kid": "k1"}, Claims{Aud: "blerg-board", ExpiresAt: now - 1})
	if _, err := Verify(old, "blerg-board", keys, fakeRev{}, notSensitive); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: err = %v, want ErrExpired", err)
	}
	// fail-closed: stale revocation list + sensitive capability
	sens := mint(t, priv, map[string]string{"alg": "EdDSA", "kid": "k1"},
		Claims{Aud: "blerg-board", Caps: []string{"secrets.read"}, ExpiresAt: now + 60})
	if _, err := Verify(sens, "blerg-board", keys, fakeRev{stale: true}, sensitive); !errors.Is(err, ErrFailClosed) {
		t.Fatalf("fail-closed: err = %v, want ErrFailClosed", err)
	}
	// stale but NON-sensitive still allowed (low-risk reads degrade gracefully)
	if _, err := Verify(good, "blerg-board", keys, fakeRev{stale: true}, sensitive); err != nil {
		t.Fatalf("stale non-sensitive: %v", err)
	}
	// explicitly revoked rejected
	if _, err := Verify(good, "blerg-board", keys, fakeRev{revoked: true}, notSensitive); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked: err = %v, want ErrRevoked", err)
	}
	// tampered signature rejected: flip a byte in the middle of the sig segment so the
	// decoded signature bytes actually change (a trailing-char swap can decode identically).
	{
		parts := strings.Split(good, ".")
		sigChars := []byte(parts[2])
		mid := len(sigChars) / 2
		if sigChars[mid] == 'A' {
			sigChars[mid] = 'B'
		} else {
			sigChars[mid] = 'A'
		}
		parts[2] = string(sigChars)
		tampered := strings.Join(parts, ".")
		if _, err := Verify(tampered, "blerg-board", keys, fakeRev{}, notSensitive); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("tampered signature: err = %v, want ErrBadSignature", err)
		}
	}
}

func TestVerifyMalformed(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	keys := KeySet{"k1": pub}
	notSensitive := func(string) bool { return false }

	// Valid base64 header whose decoded bytes are not valid JSON.
	badHeaderJSON := base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".e30.e30"

	cases := []struct {
		name  string
		token string
	}{
		{"empty string", ""},
		{"two parts", "a.b"},
		{"four parts", "a.b.c.d"},
		{"invalid base64", "!!!.!!!.!!!"},
		{"header valid base64 invalid json", badHeaderJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Verify(tc.token, "blerg-board", keys, fakeRev{}, notSensitive); !errors.Is(err, ErrMalformed) {
				t.Fatalf("%s: err = %v, want ErrMalformed", tc.name, err)
			}
		})
	}
}
