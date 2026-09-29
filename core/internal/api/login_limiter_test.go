package api

import (
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
