// Package keybackend provides envelope-encryption backends for the
// per-user credential vault (Phase E). A Backend encrypts and decrypts
// opaque plaintext (e.g. an OAuth token) and identifies which key it used
// via keyID, so callers can store (ciphertext, keyID, backendID) and later
// route Decrypt back to the correct Backend/key without ambiguity.
package keybackend

import "context"

// Backend is the §Phase E envelope-encryption seam. Implementations own key
// material and lifecycle; callers never touch keys directly.
type Backend interface {
	// ID identifies this backend implementation (e.g. "local"), stored
	// alongside ciphertext and keyID so a later Decrypt can be routed to
	// the right Backend even if multiple backend types are ever in use.
	ID() string

	// Encrypt returns the ciphertext for plaintext and the id of the key
	// used to produce it. keyID must be passed back to Decrypt.
	Encrypt(ctx context.Context, plaintext []byte) (ciphertext []byte, keyID string, err error)

	// Decrypt reverses Encrypt. keyID selects which key to decrypt with;
	// an unknown keyID (e.g. from a rotated-out key — not implemented in
	// v1) is an error rather than a silent fallback.
	Decrypt(ctx context.Context, ciphertext []byte, keyID string) ([]byte, error)
}
