package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrMalformed     = errors.New("identity: malformed token")
	ErrBadAlg        = errors.New("identity: algorithm not EdDSA")
	ErrUnknownKid    = errors.New("identity: unknown kid")
	ErrBadSignature  = errors.New("identity: bad signature")
	ErrWrongAudience = errors.New("identity: wrong audience")
	ErrExpired       = errors.New("identity: expired")
	ErrRevoked       = errors.New("identity: revoked")
	ErrFailClosed    = errors.New("identity: revocation list stale, sensitive capability denied")
)

// Verify validates a token locally (§4.1): pins EdDSA, rejects unknown kid, checks
// signature/audience/expiry/revocation, and fails closed on stale revocation for sensitive caps.
func Verify(token, audience string, keys KeySet, rev RevocationChecker, sensitive func(capability string) bool) (Principal, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Principal{}, ErrMalformed
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Principal{}, ErrMalformed
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		return Principal{}, ErrMalformed
	}
	if hdr.Alg != "EdDSA" { // hard alg pin; rejects "none" and everything else
		return Principal{}, ErrBadAlg
	}
	pub, ok := keys[hdr.Kid]
	if !ok {
		return Principal{}, ErrUnknownKid
	}
	if len(pub) != ed25519.PublicKeySize {
		// A malformed key in the key set must never reach ed25519.Verify (it panics on
		// non-32-byte keys); treat it the same as an unknown kid.
		return Principal{}, ErrUnknownKid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Principal{}, ErrMalformed
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return Principal{}, ErrBadSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Principal{}, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Principal{}, ErrMalformed
	}
	if c.Aud != audience {
		return Principal{}, ErrWrongAudience
	}
	// A missing/zero exp is treated as expired, so a forged token cannot omit expiry to live forever.
	if c.ExpiresAt == 0 || time.Now().Unix() >= c.ExpiresAt {
		return Principal{}, ErrExpired
	}
	// Timestamp-aware checkers scope sub/lineage revocations to tokens issued at or before
	// revoked_at (see IssuedAtRevocationChecker); a plain RevocationChecker revokes
	// unconditionally, the fail-closed fallback.
	var revoked bool
	if tr, ok := rev.(IssuedAtRevocationChecker); ok {
		revoked = tr.RevokedFor(hdr.Kid, c.Lineage, c.Sub, c.IssuedAt)
	} else {
		revoked = rev.Revoked(hdr.Kid, c.Lineage, c.Sub)
	}
	if revoked {
		return Principal{}, ErrRevoked
	}
	if rev.StaleBeyondCeiling() {
		for _, capability := range c.Caps {
			if sensitive(capability) {
				return Principal{}, ErrFailClosed
			}
		}
	}
	return Principal{Claims: c}, nil
}
