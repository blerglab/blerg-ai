package api

import (
	"crypto/sha256"
	"encoding/hex"
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
	lastSweep time.Time
	// maxKeys bounds each map; 0 means unbounded. At capacity a new key evicts the key whose
	// window ends first, so a full map never refuses a newcomer.
	maxKeys int
}

// NewLoginLimiter builds the shared login limiter. It has no key bound: a bound that refused
// newcomers would let a spray of distinct identities lock every new sign-in out, and one that
// evicted would let a spray reset a targeted account's count. Inactive keys are swept instead.
func NewLoginLimiter(limit int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{max: limit, window: window, now: time.Now,
		byIP: map[string][]time.Time{}, bySubject: map[string][]time.Time{}}
}

// NewBoardMintLimiter builds the "Run as me" mint limiter, keyed by account id. Its memory is
// bounded by maxLimiterKeys; at capacity it evicts the soonest-expiring account.
func NewBoardMintLimiter(limit int, window time.Duration) *LoginLimiter {
	l := NewLoginLimiter(limit, window)
	l.maxKeys = maxLimiterKeys
	return l
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
	ip, subject = limiterKey(ip), limiterKey(subject)
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.allowLocked(ip, subject, l.now())
}

const maxLimiterKeys = 4096

// maxLimiterKeyLen bounds a stored key. The login subject is whatever the caller sent, up to the
// 16 KiB form cap, so a key longer than this is stored as its SHA-256 instead. A hashed key is
// longer than any verbatim one, so the two kinds never meet, and distinct subjects keep distinct
// buckets: the bound saves memory under a spray without merging usernames.
const maxLimiterKeyLen = len(hashedKeyPrefix) + sha256.Size*2

const hashedKeyPrefix = "sha256:"

func limiterKey(s string) string {
	if len(s) <= maxLimiterKeyLen-len(hashedKeyPrefix)-1 {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return hashedKeyPrefix + hex.EncodeToString(sum[:])
}

// sweepLocked forgets inactive identities.
func (l *LoginLimiter) sweepLocked(now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < time.Minute {
		return
	}
	for _, buckets := range []map[string][]time.Time{l.byIP, l.bySubject} {
		for key, times := range buckets {
			if times = l.prune(times, now); len(times) == 0 {
				delete(buckets, key)
			} else {
				buckets[key] = times
			}
		}
	}
	l.lastSweep = now
}

func (l *LoginLimiter) allowLocked(ip, subject string, now time.Time) (bool, time.Duration) {
	l.sweepLocked(now)
	for _, entry := range []struct {
		b   map[string][]time.Time
		key string
	}{{l.byIP, ip}, {l.bySubject, subject}} {
		times := l.prune(entry.b[entry.key], now)
		if len(times) == 0 {
			delete(entry.b, entry.key)
		} else {
			entry.b[entry.key] = times
		}
		if len(times) >= l.max {
			return false, times[0].Add(l.window).Sub(now)
		}
	}
	return true, 0
}

func (l *LoginLimiter) recordLocked(ip, subject string, now time.Time) {
	l.sweepLocked(now)
	for _, entry := range []struct {
		b   map[string][]time.Time
		key string
	}{{l.byIP, ip}, {l.bySubject, subject}} {
		times := l.prune(entry.b[entry.key], now)
		if _, tracked := entry.b[entry.key]; !tracked && l.maxKeys > 0 && len(entry.b) >= l.maxKeys {
			evictSoonestLocked(entry.b)
		}
		if len(times) < l.max {
			entry.b[entry.key] = append(times, now)
		}
	}
}

// evictSoonestLocked drops the key whose newest attempt is oldest: its window ends first.
func evictSoonestLocked(b map[string][]time.Time) {
	var victim string
	var oldest time.Time
	found := false
	for key, times := range b {
		last := time.Time{}
		if len(times) > 0 {
			last = times[len(times)-1]
		}
		if !found || last.Before(oldest) {
			victim, oldest, found = key, last, true
		}
	}
	if found {
		delete(b, victim)
	}
}

// Take atomically checks and charges an attempt. Minting charges every attempt,
// so it must use this instead of the login failure-only Allow/Fail pair.
func (l *LoginLimiter) Take(ip, subject string) (bool, time.Duration) {
	if !l.enabled() {
		return true, 0
	}
	ip, subject = limiterKey(ip), limiterKey(subject)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	allowed, retry := l.allowLocked(ip, subject, now)
	if allowed {
		l.recordLocked(ip, subject, now)
	}
	return allowed, retry
}

func (l *LoginLimiter) Fail(ip, subject string) {
	if !l.enabled() {
		return
	}
	ip, subject = limiterKey(ip), limiterKey(subject)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recordLocked(ip, subject, l.now())
}

func (l *LoginLimiter) Reset(ip, subject string) {
	if !l.enabled() {
		return
	}
	ip, subject = limiterKey(ip), limiterKey(subject)
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
