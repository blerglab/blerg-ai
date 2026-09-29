package keybackend

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// Local is a Backend that encrypts with a single AES-256-GCM key handed in by
// the operator (BLERG_CORE_LOCAL_KEY). The key is never stored in the
// database: a key sitting next to its ciphertext is obfuscation, not
// encryption. Rotation is out of scope for v1: Decrypt only ever accepts the
// one keyID this instance was built with.
type Local struct {
	keyID string
	key   []byte
}

// NewLocal returns a Local backend using key as its AES-256 key.
func NewLocal(key []byte) (*Local, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("keybackend: local key must be 32 bytes (got %d); generate one with: openssl rand -base64 32", len(key))
	}
	sum := sha256.Sum256(key)
	return &Local{keyID: "local:" + hex.EncodeToString(sum[:4]), key: append([]byte(nil), key...)}, nil
}

func (l *Local) ID() string { return "local" }

// Encrypt seals plaintext with AES-256-GCM under a fresh random nonce (a
// GCM nonce must never repeat for a given key), prepending the nonce to the
// returned ciphertext so Decrypt can recover it without separate storage.
func (l *Local) Encrypt(_ context.Context, plaintext []byte) ([]byte, string, error) {
	gcm, err := newGCM(l.key)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, "", err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), l.keyID, nil
}

// Decrypt reverses Encrypt. keyID must match the key this instance loaded
// or generated — v1 supports exactly one active key, no rotation.
func (l *Local) Decrypt(_ context.Context, ciphertext []byte, keyID string) ([]byte, error) {
	if keyID != l.keyID {
		return nil, errors.New("keybackend: unknown key_id (key rotation not implemented in v1)")
	}
	gcm, err := newGCM(l.key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("keybackend: ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
