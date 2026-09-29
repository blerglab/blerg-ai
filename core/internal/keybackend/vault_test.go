package keybackend_test

import (
	"context"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/keybackend"
)

// TestVaultTransitEncryptDecryptRoundTrips exercises
// NewVaultTransitWithStaticToken against a REAL `vault server -dev`
// instance (see the task-13 brief for how to stand one up: enable the
// transit engine, `vault write -f transit/keys/blerg-core-credentials-test`,
// export VAULT_ADDR/VAULT_TOKEN). Skipped, same as this repo's
// DATABASE_URL-gated tests, when VAULT_ADDR isn't set.
func TestVaultTransitEncryptDecryptRoundTrips(t *testing.T) {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		t.Skip("VAULT_ADDR not set; skipping (needs `vault server -dev` + a transit mount + a static token for this test)")
	}
	token := os.Getenv("VAULT_TOKEN")

	backend, err := keybackend.NewVaultTransitWithStaticToken(context.Background(), addr, token, "blerg-core-credentials-test")
	if err != nil {
		t.Fatal(err)
	}
	if backend.ID() != "vault-transit" {
		t.Fatalf("ID() = %q, want %q", backend.ID(), "vault-transit")
	}

	ciphertext, keyID, err := backend.Encrypt(context.Background(), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if keyID != "blerg-core-credentials-test" {
		t.Fatalf("keyID = %q, want %q", keyID, "blerg-core-credentials-test")
	}
	if string(ciphertext) == "secret" {
		t.Fatal("ciphertext equals plaintext -- Encrypt did not actually encrypt anything")
	}

	got, err := backend.Decrypt(context.Background(), ciphertext, keyID)
	if err != nil || string(got) != "secret" {
		t.Fatalf("round-trip failed: got %q, err %v", got, err)
	}
}

// TestVaultTransitEncryptDecryptRoundTripsMultipleValues checks the
// round-trip isn't accidentally order- or content-dependent (e.g. an
// off-by-one in base64 padding) by exercising a few different payloads,
// including empty and binary-ish ones.
func TestVaultTransitEncryptDecryptRoundTripsMultipleValues(t *testing.T) {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		t.Skip("VAULT_ADDR not set; skipping")
	}
	token := os.Getenv("VAULT_TOKEN")

	backend, err := keybackend.NewVaultTransitWithStaticToken(context.Background(), addr, token, "blerg-core-credentials-test")
	if err != nil {
		t.Fatal(err)
	}

	cases := [][]byte{
		[]byte(""),
		[]byte("a"),
		[]byte("a longer oauth-token-shaped secret value with punctuation!@#$%^&*()"),
		{0x00, 0x01, 0x02, 0xff, 0xfe},
	}
	for _, plaintext := range cases {
		ciphertext, keyID, err := backend.Encrypt(context.Background(), plaintext)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", plaintext, err)
		}
		got, err := backend.Decrypt(context.Background(), ciphertext, keyID)
		if err != nil {
			t.Fatalf("Decrypt round-trip for %q: %v", plaintext, err)
		}
		if string(got) != string(plaintext) {
			t.Fatalf("round-trip mismatch: got %q, want %q", got, plaintext)
		}
	}
}

// TestVaultTransitFailsClosedOnInvalidToken confirms that a Vault-rejected
// token produces a hard error from Encrypt, never a plaintext fallback.
func TestVaultTransitFailsClosedOnInvalidToken(t *testing.T) {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		t.Skip("VAULT_ADDR not set; skipping")
	}

	backend, err := keybackend.NewVaultTransitWithStaticToken(context.Background(), addr, "not-a-real-vault-token", "blerg-core-credentials-test")
	if err != nil {
		t.Fatal(err)
	}

	ciphertext, _, err := backend.Encrypt(context.Background(), []byte("secret"))
	if err == nil {
		t.Fatalf("Encrypt with invalid token: expected error, got ciphertext %q", ciphertext)
	}
}

// TestVaultTransitFailsClosedOnNonexistentTransitKey confirms that
// decrypting against a transit key name that does not exist in Vault
// produces a hard error, never a plaintext fallback. (Vault's transit
// engine auto-vivifies a brand-new key on first Encrypt against an unknown
// name, so this exercises the failure via Decrypt, which does not
// auto-create -- Vault genuinely returns "encryption key not found".)
func TestVaultTransitFailsClosedOnNonexistentTransitKey(t *testing.T) {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		t.Skip("VAULT_ADDR not set; skipping")
	}
	token := os.Getenv("VAULT_TOKEN")

	backend, err := keybackend.NewVaultTransitWithStaticToken(context.Background(), addr, token, "task-13-nonexistent-transit-key")
	if err != nil {
		t.Fatal(err)
	}

	_, err = backend.Decrypt(context.Background(), []byte("vault:v1:doesnotmatter"), "task-13-nonexistent-transit-key")
	if err == nil {
		t.Fatal("Decrypt against a nonexistent transit key: expected error, got nil")
	}
}

// TestVaultTransitDecryptRejectsMismatchedKeyID confirms Decrypt refuses to
// even attempt decryption when keyID doesn't match this backend's own
// transit key, rather than silently trying anyway.
func TestVaultTransitDecryptRejectsMismatchedKeyID(t *testing.T) {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		t.Skip("VAULT_ADDR not set; skipping")
	}
	token := os.Getenv("VAULT_TOKEN")

	backend, err := keybackend.NewVaultTransitWithStaticToken(context.Background(), addr, token, "blerg-core-credentials-test")
	if err != nil {
		t.Fatal(err)
	}

	_, err = backend.Decrypt(context.Background(), []byte("vault:v1:doesnotmatter"), "some-other-key-id")
	if err == nil {
		t.Fatal("Decrypt with mismatched keyID: expected error, got nil")
	}
}

func TestNewVaultTransitWithStaticTokenValidatesArgs(t *testing.T) {
	ctx := context.Background()
	if _, err := keybackend.NewVaultTransitWithStaticToken(ctx, "", "token", "key"); err == nil {
		t.Fatal("expected error for empty addr")
	}
	if _, err := keybackend.NewVaultTransitWithStaticToken(ctx, "http://localhost:8200", "", "key"); err == nil {
		t.Fatal("expected error for empty token")
	}
	if _, err := keybackend.NewVaultTransitWithStaticToken(ctx, "http://localhost:8200", "token", ""); err == nil {
		t.Fatal("expected error for empty transit key name")
	}
}

func TestNewVaultTransitK8sAuthValidatesArgs(t *testing.T) {
	ctx := context.Background()
	if _, err := keybackend.NewVaultTransitK8sAuth(ctx, "", "role", "key"); err == nil {
		t.Fatal("expected error for empty addr")
	}
	if _, err := keybackend.NewVaultTransitK8sAuth(ctx, "http://localhost:8200", "", "key"); err == nil {
		t.Fatal("expected error for empty role")
	}
	if _, err := keybackend.NewVaultTransitK8sAuth(ctx, "http://localhost:8200", "role", ""); err == nil {
		t.Fatal("expected error for empty transit key name")
	}
}
