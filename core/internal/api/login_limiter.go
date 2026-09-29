package api

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LoginLimiter is an in-memory sliding-window brute-force guard for POST
// /auth/login: max failed attempts per window, tracked per client IP AND per
// subject, so neither one attacker cycling usernames nor many attackers hitting
// one account gets unlimited guesses (desktop-security C1/I5). Process-local by
// design — a desktop runs one core.
type LoginLimiter struct {
	mu        sync.Mutex
	max       int
	window    time.Duration
	now       func() time.Time
	byIP      map[string][]time.Time
	bySubject map[string][]time.Time
}

func NewLoginLimiter(limit int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{max: limit, window: window, now: time.Now,
		byIP: map[string][]time.Time{}, bySubject: map[string][]time.Time{}}
}

func (l *LoginLimiter) enabled() bool { return l != nil && l.max > 0 && l.window > 0 }

// SetClock overrides the limiter's notion of "now" — test-only, so a sliding-window test can
// advance time deterministically instead of sleeping for real.
func (l *LoginLimiter) SetClock(now func() time.Time) { l.now = now }

// prune drops entries that have aged out of the window, bounding each bucket's memory to at
// most max entries per key — this is also the only place entries are ever removed short of an
// explicit Reset, so a key that stops being attempted is naturally forgotten the next time it
// (or, for the shared byIP/bySubject maps generally, ANY key) is touched again, without a
// separate background sweep.
func (l *LoginLimiter) prune(b []time.Time, now time.Time) []time.Time {
	cut := now.Add(-l.window)
	out := b[:0]
	for _, t := range b {
		if t.After(cut) {
			out = append(out, t)
		}
	}
	return out
}

// Allow reports whether a login attempt may proceed; when denied, retryAfter is
// how long until the oldest in-window failure ages out.
func (l *LoginLimiter) Allow(ip, subject string) (bool, time.Duration) {
	if !l.enabled() {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.byIP[ip] = l.prune(l.byIP[ip], now)
	l.bySubject[subject] = l.prune(l.bySubject[subject], now)
	for _, b := range [][]time.Time{l.byIP[ip], l.bySubject[subject]} {
		if len(b) >= l.max {
			return false, b[0].Add(l.window).Sub(now)
		}
	}
	return true, 0
}

func (l *LoginLimiter) Fail(ip, subject string) {
	if !l.enabled() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.byIP[ip] = append(l.prune(l.byIP[ip], now), now)
	l.bySubject[subject] = append(l.prune(l.bySubject[subject], now), now)
}

func (l *LoginLimiter) Reset(ip, subject string) {
	if !l.enabled() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
	delete(l.bySubject, subject)
}

// ParseLoginRateLimit parses BLERG_CORE_LOGIN_RATE_LIMIT ("<max>/<window>",
// e.g. "10/5m"). "0/0" disables the limiter.
func ParseLoginRateLimit(s string) (int, time.Duration, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, fmt.Errorf("want <max>/<window>, got %q", s)
	}
	limit, err := strconv.Atoi(parts[0])
	if err != nil || limit < 0 {
		return 0, 0, fmt.Errorf("max must be a non-negative integer, got %q", parts[0])
	}
	if parts[1] == "0" {
		return limit, 0, nil
	}
	window, err := time.ParseDuration(parts[1])
	if err != nil || window < 0 {
		return 0, 0, fmt.Errorf("window must be a non-negative duration, got %q", parts[1])
	}
	return limit, window, nil
}
