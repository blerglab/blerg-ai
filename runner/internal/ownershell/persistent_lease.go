package ownershell

import (
	"crypto/ed25519"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// LeaseClock returns wall time and a local elapsed clock that includes suspend.
// Trusted runtime preparation must verify that platform guarantee before enabling
// persistence. Elapsed values must never decrease during a helper incarnation.
type LeaseClock func() (time.Time, time.Duration, error)

type preparedAccess struct {
	transaction string
	key         ed25519.PublicKey
	grant       SignedGrant
}

// PersistentLease separates access from lifetime for an explicitly negotiated v2
// desktop shell. It does not create custody or authorize re-adoption by itself:
// Prepare is called only by the launch coordinator over authenticated helper control,
// after authenticated inventory and authority checks. Terminal closure is sticky.
type PersistentLease struct {
	mu                                           sync.Mutex
	key                                          ed25519.PublicKey
	expected                                     protocol.ShellGrant
	active                                       SignedGrant
	policy                                       protocol.ShellPolicy
	clock                                        LeaseClock
	deadline                                     time.Time
	frozenDeadline                               time.Time
	frozenElapsed                                time.Duration
	frozen                                       bool
	elapsedDeadline, accessDeadline, lastElapsed time.Duration
	highestEpoch                                 uint64
	suspended, closed                            bool
	teardown                                     func() error
	teardownErr                                  error
	pending                                      *preparedAccess
	committed                                    string
}

func NewPersistentLease(key ed25519.PublicKey, grant SignedGrant, expected protocol.ShellGrant, policy protocol.ShellPolicy, deadline time.Time, clock LeaseClock, teardown func() error) (*PersistentLease, error) {
	if clock == nil || teardown == nil || expected.Version != 2 || expected.Runtime != "host" || policy.Version != 1 || !policy.Enabled {
		return nil, ErrDenied
	}
	wall, elapsed, err := clock()
	if err != nil || elapsed < 0 || deadline.IsZero() || !deadline.After(wall) || deadline.Sub(wall) > MaxLimit || expected.Runtime == "host" && deadline.Sub(wall) > protocol.DesktopShellLifetime+protocol.DesktopShellClockSkew || VerifyGrant(key, grant, expected, policy.Revision, wall) != nil {
		return nil, ErrDenied
	}
	remaining := deadline.Sub(wall)
	if elapsed > time.Duration(1<<63-1)-remaining {
		return nil, ErrDenied
	}
	l := &PersistentLease{key: append(ed25519.PublicKey(nil), key...), expected: expected, active: grant, policy: policy, clock: clock, deadline: deadline, elapsedDeadline: elapsed + remaining, lastElapsed: elapsed, highestEpoch: expected.AccessEpoch, teardown: teardown}
	l.setAccessDeadline(wall, elapsed)
	return l, nil
}

func (l *PersistentLease) setAccessDeadline(wall time.Time, elapsed time.Duration) {
	// VerifyGrant bounds this to at most 35 seconds, including issuance skew.
	remaining := time.Unix(l.active.Grant.ExpiresAt, 0).Sub(wall)
	if elapsed > time.Duration(1<<63-1)-remaining {
		l.suspended = true
		return
	}
	l.accessDeadline = elapsed + remaining
}

func (l *PersistentLease) closeLocked() {
	if !l.closed {
		l.closed = true
		l.suspended = true
		l.pending = nil
		l.teardownErr = l.teardown()
	}
}

func (l *PersistentLease) aliveLocked() (time.Time, time.Duration, bool) {
	if l.closed {
		return time.Time{}, 0, false
	}
	wall, elapsed, err := l.clock()
	if err != nil || elapsed < l.lastElapsed || !wall.Before(l.deadline) || elapsed >= l.elapsedDeadline || l.frozen && (!wall.Before(l.frozenDeadline) || elapsed >= l.frozenElapsed) {
		// Loss of the verified lifetime clock cannot leave an unbounded writer.
		l.closeLocked()
		return wall, elapsed, false
	}
	l.lastElapsed = elapsed
	return wall, elapsed, true
}

func (l *PersistentLease) checkLocked() error {
	wall, elapsed, alive := l.aliveLocked()
	if !alive {
		return ErrDenied
	}
	if !l.suspended && (elapsed >= l.accessDeadline || VerifyGrant(l.key, l.active, l.expected, l.policy.Revision, wall) != nil) {
		l.suspended = true
	}
	if l.suspended || l.frozen {
		return ErrDenied
	}
	return nil
}

func (l *PersistentLease) Check() error { l.mu.Lock(); defer l.mu.Unlock(); return l.checkLocked() }
func (l *PersistentLease) Tick()        { _ = l.Check() }

// Suspend latches the old access epoch closed without changing process lifetime.
func (l *PersistentLease) Suspend() { l.mu.Lock(); defer l.mu.Unlock(); l.suspended = true }

func (l *PersistentLease) Renew(grant SignedGrant) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.checkLocked() != nil {
		return ErrDenied
	}
	wall, elapsed, alive := l.aliveLocked()
	if !alive || elapsed >= l.accessDeadline || VerifyGrant(l.key, grant, l.expected, l.policy.Revision, wall) != nil || grant.Grant.IssuedAt < l.active.Grant.IssuedAt || grant.Grant.ExpiresAt < l.active.Grant.ExpiresAt {
		return ErrDenied
	}
	l.active = grant
	l.setAccessDeadline(wall, elapsed)
	return nil
}

func sameCustody(a, b protocol.ShellGrant) bool {
	return a.Version == 2 && b.Version == 2 && a.CustodyID == b.CustodyID && a.OwnerID == b.OwnerID && a.LoginID == b.LoginID && a.SessionID == b.SessionID && a.LaunchGeneration == b.LaunchGeneration && a.ShellGeneration == b.ShellGeneration && a.Runtime == b.Runtime && a.ConsentID == b.ConsentID && a.PolicyRevision == b.PolicyRevision
}

// Prepare reserves a strictly higher epoch and keeps all access closed. The
// caller must already have serialized launch-wide rekey and verified custody.
func (l *PersistentLease) Prepare(transaction string, key ed25519.PublicKey, grant SignedGrant) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	wall, _, alive := l.aliveLocked()
	if !alive || transaction == "" || !sameCustody(l.expected, grant.Grant) || VerifyGrant(key, grant, grant.Grant, l.policy.Revision, wall) != nil {
		return ErrDenied
	}
	if p := l.pending; p != nil && p.transaction == transaction {
		if p.key.Equal(key) && p.grant == grant {
			return nil
		}
		return ErrDenied
	}
	if transaction == l.committed || grant.Grant.AccessEpoch <= l.highestEpoch || grant.Grant.AccessEpoch-l.highestEpoch > 1024 {
		return ErrDenied
	}
	l.highestEpoch = grant.Grant.AccessEpoch
	l.suspended = true
	l.pending = &preparedAccess{transaction: transaction, key: append(ed25519.PublicKey(nil), key...), grant: grant}
	return nil
}

func (l *PersistentLease) Commit(transaction string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	wall, elapsed, alive := l.aliveLocked()
	if !alive || l.frozen {
		return ErrDenied
	}
	if transaction != "" && transaction == l.committed && l.pending == nil {
		return l.checkLocked()
	}
	p := l.pending
	if p == nil || p.transaction != transaction || VerifyGrant(p.key, p.grant, p.grant.Grant, l.policy.Revision, wall) != nil {
		return ErrDenied
	}
	l.key = p.key
	l.active = p.grant
	l.expected = p.grant.Grant
	l.committed = transaction
	l.pending = nil
	l.suspended = false
	l.setAccessDeadline(wall, elapsed)
	return l.checkLocked()
}

func (l *PersistentLease) Policy(policy protocol.ShellPolicy) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if policy.Version != 1 || !policy.Enabled || policy.Revision != l.policy.Revision {
		l.closeLocked()
	}
}

func (l *PersistentLease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeLocked()
	return l.teardownErr
}

func (l *PersistentLease) Closed() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.closed }

func (l *PersistentLease) CheckEpoch(epoch uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.checkLocked() != nil || epoch != l.expected.AccessEpoch {
		return ErrDenied
	}
	return nil
}

// Access serializes the final nonblocking PTY syscall with suspension/rekey.
// The callback must not wait for IO: retry EAGAIN outside this critical section.
func (l *PersistentLease) Access(grant SignedGrant, write func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.checkLocked() != nil {
		return ErrDenied
	}
	wall, _, alive := l.aliveLocked()
	if !alive || VerifyGrant(l.key, grant, l.expected, l.policy.Revision, wall) != nil {
		return ErrDenied
	}
	return write()
}

// FreezeUntil installs the independent workspace-sync deadline before the
// runtime physically freezes writers. It requires live custody, not live access.
// Repeated phase messages can shorten this deadline but cannot extend it.
func (l *PersistentLease) FreezeUntil(until time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	wall, elapsed, alive := l.aliveLocked()
	if !alive || until.IsZero() || !until.After(wall) {
		return ErrDenied
	}
	if until.After(l.deadline) {
		until = l.deadline
	}
	remaining := until.Sub(wall)
	if elapsed > time.Duration(1<<63-1)-remaining {
		return ErrDenied
	}
	if !l.frozen || until.Before(l.frozenDeadline) {
		l.frozenDeadline = until
	}
	if !l.frozen || elapsed+remaining < l.frozenElapsed {
		l.frozenElapsed = elapsed + remaining
	}
	l.frozen = true
	l.suspended = true
	return nil
}

// Thawed only clears the independent sync watchdog after physical thaw has
// succeeded. Access stays suspended until a fresh prepared epoch commits.
func (l *PersistentLease) Thawed() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, _, alive := l.aliveLocked(); !alive {
		return ErrDenied
	}
	l.frozen = false
	return nil
}
