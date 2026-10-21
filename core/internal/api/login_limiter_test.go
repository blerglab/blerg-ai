package api

import (
	"strconv"
	"testing"
	"time"
)

func TestLoginLimiterSlidingWindow(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	l := NewLoginLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("1.1.1.1", "alice"); !ok {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		l.Fail("1.1.1.1", "alice")
	}
	if ok, ra := l.Allow("1.1.1.1", "alice"); ok || ra <= 0 || ra > time.Minute {
		t.Fatalf("4th attempt: ok=%v retryAfter=%s, want denied with 0<retryAfter<=1m", ok, ra)
	}
	if ok, _ := l.Allow("2.2.2.2", "alice"); ok {
		t.Fatal("subject bucket must deny from another IP")
	}
	if ok, _ := l.Allow("1.1.1.1", "bob"); ok {
		t.Fatal("IP bucket must deny another subject")
	}
	if ok, _ := l.Allow("2.2.2.2", "bob"); !ok {
		t.Fatal("unrelated ip+subject must be allowed")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := l.Allow("1.1.1.1", "alice"); !ok {
		t.Fatal("window elapsed: must be allowed again")
	}
	l.Fail("1.1.1.1", "alice")
	l.Reset("1.1.1.1", "alice")
	if len(l.byIP["1.1.1.1"]) != 0 || len(l.bySubject["alice"]) != 0 {
		t.Fatal("Reset must clear both buckets")
	}
	if ok, _ := NewLoginLimiter(0, 0).Allow("x", "y"); !ok {
		t.Fatal("disabled limiter must allow")
	}
}

func TestParseLoginRateLimit(t *testing.T) {
	if n, w, err := ParseLoginRateLimit("10/5m"); err != nil || n != 10 || w != 5*time.Minute {
		t.Fatalf("10/5m → %d %s %v", n, w, err)
	}
	if n, w, err := ParseLoginRateLimit("0/0"); err != nil || n != 0 || w != 0 {
		t.Fatalf("0/0 → %d %s %v", n, w, err)
	}
	for _, bad := range []string{"x", "10", "10/", "/5m", "-1/5m", "10/-5m"} {
		if _, _, err := ParseLoginRateLimit(bad); err == nil {
			t.Errorf("%q must not parse", bad)
		}
	}
}

func TestBoardMintLimiterForgetsInactiveAccounts(t *testing.T) {
	now := time.Now()
	l := NewBoardMintLimiter(3, time.Hour)
	l.SetClock(func() time.Time { return now })
	for i := range 5000 {
		key := strconv.Itoa(i)
		l.Allow(key, key)
		l.Fail(key, key)
	}
	if len(l.bySubject) > maxLimiterKeys || len(l.byIP) > maxLimiterKeys {
		t.Fatal("live identities exceeded the memory bound")
	}
	now = now.Add(2 * time.Hour)
	l.Allow("fresh", "fresh")
	l.Fail("fresh", "fresh")
	if len(l.bySubject) > 4096 || len(l.byIP) > 4096 {
		t.Fatalf("inactive accounts retained: %d %d", len(l.bySubject), len(l.byIP))
	}
}

func TestMintLimiterAtomicTake(t *testing.T) {
	l := NewLoginLimiter(3, time.Hour)
	results := make(chan bool, 128)
	start := make(chan struct{})
	for range 128 {
		go func() { <-start; ok, _ := l.Take("account", "account"); results <- ok }()
	}
	close(start)
	allowed := 0
	for range 128 {
		if <-results {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("allowed=%d", allowed)
	}
}

func TestMintLimiterSlidingWindowTracksPartialExpiry(t *testing.T) {
	start := time.Now()
	now := start
	limiter := NewLoginLimiter(3, time.Minute)
	limiter.SetClock(func() time.Time { return now })
	for _, seconds := range []int{0, 10, 20, 61, 71, 81, 121} {
		now = start.Add(time.Duration(seconds) * time.Second)
		if ok, _ := limiter.Take("account", "account"); !ok {
			t.Fatalf("valid attempt at %ds refused", seconds)
		}
	}
	if ok, _ := limiter.Take("account", "account"); ok {
		t.Fatal("partially expired window lost recent attempts")
	}
}

// A flood of distinct failed identities must never lock a fresh, legitimate sign-in out: the
// shared login limiter has no key bound that refuses newcomers (review C1).
func TestLoginLimiterFullMapDoesNotRefuseNewIdentity(t *testing.T) {
	now := time.Now()
	l := NewLoginLimiter(10, 5*time.Minute)
	l.SetClock(func() time.Time { return now })
	for i := range 5000 {
		ip, subject := "198.51.100."+strconv.Itoa(i), "spray-"+strconv.Itoa(i)
		if ok, _ := l.Allow(ip, subject); ok {
			l.Fail(ip, subject)
		}
	}
	if ok, _ := l.Allow("192.0.2.7", "alice"); !ok {
		t.Fatal("a fresh sign-in was refused because the limiter map was full")
	}
	// The sprayed identities keep their counts: nothing was evicted from the login limiter.
	if len(l.bySubject) != 5000 {
		t.Fatalf("login limiter tracked %d subjects, want 5000", len(l.bySubject))
	}
}

// The mint limiter is bounded, but at capacity it evicts the identity whose window ends first
// instead of refusing a new account.
func TestBoardMintLimiterAtCapacityEvictsInsteadOfRefusing(t *testing.T) {
	start := time.Now()
	now := start
	l := NewBoardMintLimiter(3, time.Hour)
	l.SetClock(func() time.Time { return now })
	for i := range maxLimiterKeys {
		now = start.Add(time.Duration(i) * time.Millisecond)
		key := "account-" + strconv.Itoa(i)
		if ok, _ := l.Take(key, key); !ok {
			t.Fatalf("account %d refused while filling", i)
		}
	}
	now = now.Add(time.Second)
	if ok, _ := l.Take("newcomer", "newcomer"); !ok {
		t.Fatal("a new account was refused because the mint limiter was full")
	}
	if len(l.bySubject) > maxLimiterKeys || len(l.byIP) > maxLimiterKeys {
		t.Fatalf("mint limiter exceeded its bound: %d %d", len(l.bySubject), len(l.byIP))
	}
	if _, ok := l.bySubject["account-0"]; ok {
		t.Fatal("the soonest-expiring account was not the one evicted")
	}
	if _, ok := l.bySubject["newcomer"]; !ok {
		t.Fatal("the newcomer's attempt was not charged")
	}
}

// N1: the login limiter keys on the claimed subject, which a caller can make as long as the
// 16 KiB form allows. A stored key must stay small, and two long subjects that differ only past
// any prefix must still be two buckets, so the bound never merges distinct usernames.
func TestLoginLimiterBoundsKeySizeWithoutMergingSubjects(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	l := NewLoginLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	long := make([]byte, 16<<10)
	for i := range long {
		long[i] = 'a'
	}
	a, b := string(long), string(long[:len(long)-1])+"b"
	for i := 0; i < 2; i++ {
		l.Fail("1.1.1."+strconv.Itoa(i), a)
	}
	for key := range l.bySubject {
		if len(key) > maxLimiterKeyLen {
			t.Fatalf("stored subject key is %d bytes, want at most %d", len(key), maxLimiterKeyLen)
		}
	}
	if ok, _ := l.Allow("9.9.9.9", a); ok {
		t.Fatal("a long subject must still be limited")
	}
	if ok, _ := l.Allow("9.9.9.9", b); !ok {
		t.Fatal("a different long subject must have its own bucket")
	}
	l.Fail("8.8.8.8", "alice")
	if len(l.bySubject["alice"]) != 1 {
		t.Fatal("a short subject keeps its own readable key")
	}
	l.Reset("9.9.9.9", a)
	if ok, _ := l.Allow("7.7.7.7", a); !ok {
		t.Fatal("Reset must clear a long subject's bucket")
	}
}
