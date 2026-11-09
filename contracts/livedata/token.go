// Package livedata defines the isolated, session-less live polling credential.
// A valid signature is not dispatch authority: callers must check current Core
// authority and consume a committed attempt before every provider call.
package livedata

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

const Type = "blerg-live-data+jwt"
const Lifetime = 5 * time.Minute

var ErrDenied = errors.New("live_data_denied")

// Scope is comparable, so each execution boundary can check exact equality.
type Scope struct {
	Installation         string `json:"installation"`
	Runner               string `json:"runner"`
	Owner                string `json:"owner"`
	Binding              string `json:"binding"`
	Connection           string `json:"connection"`
	Tool                 string `json:"tool"`
	Hash                 string `json:"hash"`
	Digest               string `json:"digest"`
	Revision             int64  `json:"revision"`
	ConnectionGeneration int64  `json:"connection_generation"`
	OwnerEpoch           int64  `json:"owner_epoch"`
	AuthorityEpoch       int64  `json:"authority_epoch"`
}

type Claims struct {
	Scope     Scope  `json:"scope"`
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	Operation string `json:"op"`
	IssuedAt  int64  `json:"iat"`
	NotBefore int64  `json:"nbf"`
	ExpiresAt int64  `json:"exp"`
}

type header struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

func Audience(installation, runner string) string {
	return "blerg-live-data:" + installation + ":" + runner
}

func (s Scope) Valid() bool {
	for _, v := range []string{s.Installation, s.Runner, s.Owner, s.Binding, s.Connection} {
		if len(v) != 36 {
			return false
		}
		for i, c := range v {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if c != '-' {
					return false
				}
			} else if !strings.ContainsRune("0123456789abcdef", c) {
				return false
			}
		}
	}
	for _, v := range []string{s.Hash, s.Digest} {
		b, err := hex.DecodeString(v)
		if err != nil || len(b) != 32 || strings.ToLower(v) != v {
			return false
		}
	}
	if s.Tool == "" || len(s.Tool) > 128 || s.Revision < 1 || s.ConnectionGeneration < 1 || s.OwnerEpoch < 1 || s.AuthorityEpoch < 1 {
		return false
	}
	for _, c := range s.Tool {
		if !asciiIDChar(c, "_.-") {
			return false
		}
	}
	return true
}

func valid(c Claims, now time.Time) bool {
	return c.Scope.Valid() && c.Issuer == "blerg-core-live-data" && c.Operation == "poll" && c.Audience == Audience(c.Scope.Installation, c.Scope.Runner) && c.IssuedAt > 0 && c.NotBefore == c.IssuedAt && c.ExpiresAt > c.IssuedAt && c.ExpiresAt-c.IssuedAt <= int64(Lifetime/time.Second) && c.IssuedAt <= now.Unix()+30 && c.ExpiresAt > now.Unix()
}

func Sign(c Claims, kid string, key ed25519.PrivateKey, now time.Time) (string, error) {
	if !valid(c, now) || !validKeyID(kid) || len(key) != ed25519.PrivateKeySize {
		return "", ErrDenied
	}
	h, _ := json.Marshal(header{"EdDSA", Type, kid})
	p, _ := json.Marshal(c)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input))), nil
}

func Verify(token string, keys map[string]ed25519.PublicKey, installation, runner string, now time.Time) (Claims, error) {
	bad := Claims{}
	if len(token) > 8192 {
		return bad, ErrDenied
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return bad, ErrDenied
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return bad, ErrDenied
	}
	var h header
	if strict(raw, &h) != nil || h.Algorithm != "EdDSA" || h.Type != Type || !validKeyID(h.KeyID) {
		return bad, ErrDenied
	}
	key := keys[h.KeyID]
	if len(key) != ed25519.PublicKeySize {
		return bad, ErrDenied
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return bad, ErrDenied
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return bad, ErrDenied
	}
	var c Claims
	if strict(payload, &c) != nil || !valid(c, now) || c.Scope.Installation != installation || c.Scope.Runner != runner {
		return bad, ErrDenied
	}
	return c, nil
}

func validKeyID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !asciiIDChar(c, "-_") {
			return false
		}
	}
	return true
}

// Requiring the exact encoded struct also rejects duplicate fields, nulls,
// alternate field casing and unknown/ambiguous spellings in signed tokens.
func strict(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return ErrDenied
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ErrDenied
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(raw, canonical) {
		return ErrDenied
	}
	return nil
}

// Grant and Snapshot contain server-only approved configuration and verification
// keys. Never serialize either into a dashboard, agent response, or socket event.
type Grant struct {
	Configuration   json.RawMessage `json:"configuration"`
	Scope           Scope
	Arguments       json.RawMessage
	ExpiresAt       time.Time
	CoreTime        time.Time
	UnsafeHostOptIn bool
}
type Snapshot struct {
	Grant Grant
	Keys  map[string]ed25519.PublicKey
}

func asciiIDChar(c rune, extra string) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(extra, c)
}
