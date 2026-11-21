// Package livedata implements the isolated Core live-data issuer. No HTTP routes
// are registered until human approval and service authentication are reviewed.
package livedata

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sync"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

var ErrUnavailable = errors.New("live_data_unavailable")

// Keyring is private to Core. Load private keys only from a service-only mount.
// Epoch must be provisioned outside a restorable Core database snapshot.
type Keyring struct {
	mu       sync.RWMutex
	epoch    int64
	active   string
	private  ed25519.PrivateKey
	previous map[string]retiredKey
}
type retiredKey struct {
	public ed25519.PublicKey
	until  time.Time
}

func LoadKeyring(kid, path string, epoch int64) (out *Keyring, resultErr error) {
	defer func() { keyFailure(resultErr) }()
	key, err := loadPrivate(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	if !keyID.MatchString(kid) || epoch < 1 {
		return nil, ErrUnavailable
	}
	return &Keyring{epoch: epoch, active: kid, private: key, previous: map[string]retiredKey{}}, nil
}

func loadPrivate(path string) (ed25519.PrivateKey, error) {
	// Open once; verify the opened object, not a path followed by a second read.
	file, err := os.Open(path) //nolint:gosec // path is trusted Core-only key mount configuration, never request data.
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(raw) > 8192 {
		return nil, ErrUnavailable
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		return nil, ErrUnavailable
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, ErrUnavailable
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, ErrUnavailable
	}
	return append(ed25519.PrivateKey(nil), key...), nil
}

// Rotate stages a fully loaded key before changing state. Ordinary rotation keeps
// the preceding public key for at most five minutes. Compromise advances epoch
// and removes all previous trust immediately; the operator persists that epoch.
func (k *Keyring) Rotate(kid, path string, epoch int64, compromised bool, now time.Time) (resultErr error) {
	defer func() { keyFailure(resultErr) }()
	key, err := loadPrivate(path)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if !keyID.MatchString(kid) || kid == k.active || bytes.Equal(key, k.private) || epoch < k.epoch || compromised && epoch <= k.epoch {
		return ld.ErrDenied
	}
	if _, exists := k.previous[kid]; exists {
		return ld.ErrDenied
	}
	if compromised || epoch > k.epoch {
		k.previous = map[string]retiredKey{}
	} else {
		for id, old := range k.previous {
			if !now.Before(old.until) {
				delete(k.previous, id)
			}
		}
		pub, ok := k.private.Public().(ed25519.PublicKey)
		if !ok {
			return ErrUnavailable
		}
		k.previous[k.active] = retiredKey{append(ed25519.PublicKey(nil), pub...), now.Add(ld.Lifetime)}
	}
	k.active, k.private, k.epoch = kid, key, epoch
	return nil
}

func (k *Keyring) public(now time.Time) (map[string]ed25519.PublicKey, int64) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	keys := map[string]ed25519.PublicKey{}
	pub, ok := k.private.Public().(ed25519.PublicKey)
	if ok {
		keys[k.active] = append(ed25519.PublicKey(nil), pub...)
	}
	for id, old := range k.previous {
		if now.Before(old.until) {
			keys[id] = append(ed25519.PublicKey(nil), old.public...)
		}
	}
	return keys, k.epoch
}

func (k *Keyring) sign(claims ld.Claims, now time.Time) (string, AuditMetadata, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if claims.Scope.AuthorityEpoch != k.epoch {
		return "", AuditMetadata{}, ld.ErrDenied
	}
	token, err := ld.Sign(claims, k.active, k.private, now)
	return token, AuditMetadata{KeyID: k.active, Epoch: k.epoch}, err
}

var keyID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func keyFailure(err error) {
	if err != nil {
		failurelog.Default.Event("live_data_key", slog.LevelWarn, "component", "core", "cause", "key_unavailable")
	}
}

func (k *Keyring) auditIdentity() (string, int64) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.active, k.epoch
}
