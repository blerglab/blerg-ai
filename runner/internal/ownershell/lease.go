package ownershell

import (
	"crypto/ed25519"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// RuntimeLease is the runtime-side grant gate for the P0b adapter. Construct it only with
// the key/expected binding learned over the existing authenticated runner connection.
// Call Check on every local operation/output and Tick independently of output delivery.
// Teardown must own containment; its failure remains observable to the lifecycle coordinator.
type RuntimeLease struct {
	mu          sync.Mutex
	key         ed25519.PublicKey
	expected    protocol.ShellGrant
	policy      protocol.ShellPolicy
	active      SignedGrant
	now         func() time.Time
	teardown    func() error
	closed      bool
	teardownErr error
}

func NewRuntimeLease(key ed25519.PublicKey, grant SignedGrant, expected protocol.ShellGrant, policy protocol.ShellPolicy, now func() time.Time, teardown func() error) (*RuntimeLease, error) {
	if now == nil {
		now = time.Now
	}
	if policy.Version != 1 || !policy.Enabled || teardown == nil || VerifyGrant(key, grant, expected, policy.Revision, now()) != nil {
		return nil, ErrDenied
	}
	return &RuntimeLease{key: append(ed25519.PublicKey(nil), key...), expected: expected, policy: policy, active: grant, now: now, teardown: teardown}, nil
}
func (l *RuntimeLease) closeLocked() {
	if !l.closed {
		l.closed = true
		l.teardownErr = l.teardown()
	}
}
func (l *RuntimeLease) validLocked() bool {
	if l.closed {
		return false
	}
	if !l.policy.Enabled || VerifyGrant(l.key, l.active, l.expected, l.policy.Revision, l.now()) != nil {
		l.closeLocked()
		return false
	}
	return true
}
func (l *RuntimeLease) Check() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.validLocked() {
		return ErrDenied
	}
	return nil
}
func (l *RuntimeLease) Tick() { _ = l.Check() }
func (l *RuntimeLease) Renew(grant SignedGrant) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.validLocked() || VerifyGrant(l.key, grant, l.expected, l.policy.Revision, l.now()) != nil || grant.Grant.IssuedAt < l.active.Grant.IssuedAt || grant.Grant.ExpiresAt < l.active.Grant.ExpiresAt {
		return ErrDenied
	}
	l.active = grant
	return nil
}

// Policy changes revoke rather than mutate a live shell into a new authorization generation.
func (l *RuntimeLease) Policy(policy protocol.ShellPolicy) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if policy.Version != 1 || !policy.Enabled || policy.Revision != l.policy.Revision {
		l.closeLocked()
	}
}
func (l *RuntimeLease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeLocked()
	return l.teardownErr
}
