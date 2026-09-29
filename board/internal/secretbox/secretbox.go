// Package secretbox encrypts small secrets at rest with AES-256-GCM.
//
// A sealed value is "v1:" + base64(nonce || ciphertext||tag). The version
// prefix lets a later key rotation add "v2" without ambiguity; the caller's
// additional data (a record id) is bound into the AEAD, so a ciphertext copied
// to another record fails to open. Nothing in this package ever puts a key,
// plaintext or ciphertext into an error.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// V1Prefix marks a value sealed by this package's first format.
const V1Prefix = "v1:"

// ErrOpen is returned for every failure to open a sealed value: wrong key,
// wrong additional data, corruption, or an unknown format. It is deliberately
// one error so nothing about the secret can leak through its wording.
var ErrOpen = errors.New("secretbox: cannot open sealed value")

// Box seals and opens values under one key.
type Box struct{ aead cipher.AEAD }

// ParseKey decodes a key given as standard padded base64 of exactly 32 bytes.
// Anything else — empty, wrong alphabet, wrong length — is refused. The error
// names the requirement, never the value.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("key is empty")
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, errors.New("key is not valid base64")
	}
	if len(b) != KeySize {
		return nil, fmt.Errorf("key must decode to exactly %d bytes (got %d)", KeySize, len(b))
	}
	return b, nil
}

// New builds a Box from a raw 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be exactly %d bytes (got %d)", KeySize, len(key))
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("secretbox: cipher init failed")
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, errors.New("secretbox: cipher init failed")
	}
	return &Box{aead: g}, nil
}

// IsSealed reports whether s carries a version prefix this package writes.
func IsSealed(s string) bool { return strings.HasPrefix(s, V1Prefix) }

// Seal encrypts plaintext, binding ad, with a fresh random nonce.
func (b *Box) Seal(plaintext, ad string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("secretbox: no randomness available")
	}
	out := b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(ad))
	return V1Prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal with the same ad.
func (b *Box) Open(sealed, ad string) (string, error) {
	rest, ok := strings.CutPrefix(sealed, V1Prefix)
	if !ok {
		return "", ErrOpen
	}
	raw, err := base64.StdEncoding.DecodeString(rest)
	if err != nil || len(raw) < b.aead.NonceSize()+b.aead.Overhead() {
		return "", ErrOpen
	}
	n := b.aead.NonceSize()
	pt, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(ad))
	if err != nil {
		return "", ErrOpen
	}
	return string(pt), nil
}
