package keybackend_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/keybackend"
)

func testKey(fill byte) []byte { return bytes.Repeat([]byte{fill}, 32) }

func TestLocalRoundTrips(t *testing.T) {
	b, err := keybackend.NewLocal(testKey(1))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ct, keyID, err := b.Encrypt(ctx, []byte("sk-ant-super-secret-token"))
	if err != nil || bytes.Equal(ct, []byte("sk-ant-super-secret-token")) {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := b.Decrypt(ctx, ct, keyID)
	if err != nil || string(got) != "sk-ant-super-secret-token" {
		t.Fatalf("Decrypt = %q, %v", got, err)
	}
}

func TestLocalRejectsWrongKeyLength(t *testing.T) {
	if _, err := keybackend.NewLocal([]byte("short")); err == nil {
		t.Fatal("a non-32-byte key must be rejected")
	}
}

func TestLocalKeyIDIsDerivedFromTheKey(t *testing.T) {
	b, _ := keybackend.NewLocal(testKey(2))
	_, keyID, _ := b.Encrypt(context.Background(), []byte("x"))
	sum := sha256.Sum256(testKey(2))
	if want := "local:" + hex.EncodeToString(sum[:4]); keyID != want {
		t.Fatalf("keyID = %q, want %q", keyID, want)
	}
}

func TestLocalUniqueNoncePerCall(t *testing.T) {
	b, _ := keybackend.NewLocal(testKey(3))
	ct1, _, _ := b.Encrypt(context.Background(), []byte("same"))
	ct2, _, _ := b.Encrypt(context.Background(), []byte("same"))
	if bytes.Equal(ct1, ct2) {
		t.Fatal("nonce reuse: identical ciphertexts for identical plaintexts")
	}
}

func TestLocalRefusesCiphertextFromAnotherKey(t *testing.T) {
	a, _ := keybackend.NewLocal(testKey(4))
	b, _ := keybackend.NewLocal(testKey(5))
	ct, keyID, _ := a.Encrypt(context.Background(), []byte("secret"))
	if _, err := b.Decrypt(context.Background(), ct, keyID); err == nil {
		t.Fatal("a different key must not decrypt (key_id mismatch)")
	}
}
