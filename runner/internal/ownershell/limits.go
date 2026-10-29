package ownershell

import "time"

// MinLimit is the shortest inactivity limit an administrator may set. It is also the time an
// already-running shell keeps after an administrator shortens its limit.
const MinLimit = 10 * time.Minute

// MaxLimit is the longest explicit inactivity limit (ten years); longer means "until the session ends".
const MaxLimit = 87600 * time.Hour

// Limits are one runtime class's inactivity limits. Zero means no limit: the shell lasts
// until the session ends (or until revocation, a fence, or a lost runtime connection end it).
type Limits struct {
	Detached time.Duration // no browser attached
	Idle     time.Duration // no human input
}

// ShellLimits are the install-wide limits per runtime class: desktop (host and local sandbox)
// and cluster.
type ShellLimits struct{ Desktop, Cluster Limits }

// DefaultLimits keeps the cluster at ReconnectGrace and IdleTimeout; desktop shells last until
// the session ends.
func DefaultLimits() ShellLimits {
	return ShellLimits{Cluster: Limits{Detached: ReconnectGrace, Idle: IdleTimeout}}
}

// ValidLimit reports whether an administrator may set d: zero (until the session ends) or
// MinLimit through MaxLimit.
func ValidLimit(d time.Duration) bool { return d == 0 || (d >= MinLimit && d <= MaxLimit) }

// For returns the limits of a binding runtime. An unknown runtime gets the cluster's.
func (l ShellLimits) For(runtime string) Limits {
	if runtime == "host" || runtime == "sandbox" {
		return l.Desktop
	}
	return l.Cluster
}

// shorter reports whether next can end a shell sooner than prev; zero is unlimited.
func shorter(next, prev time.Duration) bool { return next != 0 && (prev == 0 || next < prev) }

// Limits returns the limits in force.
func (m *Manager) Limits() ShellLimits {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.limits
}

// SetLimits applies new limits to running and future shells. When one limit gets shorter, a
// running shell keeps at least MinLimit from the change before that limit can end it, but never
// longer than the deadline it already had. A limit that is unchanged or longer keeps its deadline.
func (m *Manager) SetLimits(l ShellLimits) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l == m.limits {
		return
	}
	now := m.now()
	for _, s := range m.shells {
		prev, next := m.limits.For(s.binding.Runtime), l.For(s.binding.Runtime)
		if shorter(next.Idle, prev.Idle) {
			s.idleGrace = s.idleGrace.shortened(prev.Idle, s.lastInput, now)
		}
		if shorter(next.Detached, prev.Detached) {
			s.detachedGrace = s.detachedGrace.shortened(prev.Detached, s.detached, now)
		}
	}
	m.limits = l
}

// limitGrace holds a shortened limit off a shell whose inactivity began before the change.
type limitGrace struct{ at, until time.Time }

// deadline is when limit ends a shell inactive since since; ok is false for no limit.
func (g limitGrace) deadline(limit time.Duration, since time.Time) (time.Time, bool) {
	if limit <= 0 {
		return time.Time{}, false
	}
	d := since.Add(limit)
	if since.Before(g.at) && g.until.After(d) {
		d = g.until
	}
	return d, true
}

// shortened is the grace after the limit shortens from prev at now: MinLimit from now, but no
// later than the deadline prev already gave the shell.
func (g limitGrace) shortened(prev time.Duration, since, now time.Time) limitGrace {
	until := now.Add(MinLimit)
	if old, ok := g.deadline(prev, since); ok && old.Before(until) {
		until = old
	}
	return limitGrace{at: now, until: until}
}

// limitReached reports whether a shell inactive since since has passed limit (zero never does).
func limitReached(g limitGrace, limit time.Duration, since, now time.Time) bool {
	d, ok := g.deadline(limit, since)
	return ok && !now.Before(d)
}

// endedRetention is how long the cause of a limit-ended shell stays readable by its owner,
// so a tab that wakes up later can say why its shell ended. maxEnded bounds the memory.
const (
	endedRetention = 24 * time.Hour
	maxEnded       = 1024
)

// endedShell records why an inactivity limit ended a shell. It holds no payload.
type endedShell struct {
	actor  Actor
	reason Reason
	after  time.Duration
	at     time.Time
}

// recordEndedLocked remembers why a limit ended s. Called under the manager lock.
func (m *Manager) recordEndedLocked(s *shell, reason Reason, after time.Duration) {
	if len(m.ended) >= maxEnded {
		oldest := ""
		for h, e := range m.ended {
			if oldest == "" || e.at.Before(m.ended[oldest].at) {
				oldest = h
			}
		}
		delete(m.ended, oldest)
	}
	m.ended[s.handle] = endedShell{actor: s.actor, reason: reason, after: after, at: m.now()}
}

// pruneEndedLocked drops causes past their retention. Called under the manager lock.
func (m *Manager) pruneEndedLocked() {
	for h, e := range m.ended {
		if m.now().Sub(e.at) >= endedRetention {
			delete(m.ended, h)
		}
	}
}

// Ended reports why an inactivity limit ended the shell behind handle, only to the same
// human login that owned it. ok is false for any other end, an unknown handle or another actor.
func (m *Manager) Ended(a Actor, handle string) (reason Reason, after time.Duration, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, found := m.ended[handle]
	if !found || a.Kind != "human" || a.Owner == "" || a.Login == "" || e.actor != a || m.now().Sub(e.at) >= endedRetention {
		return "", 0, false
	}
	return e.reason, e.after, true
}
