package runner

import (
	"testing"
	"time"
)

func TestIdleTimeoutFromEnv(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"":      0,
		"0":     0,
		"-5":    0,
		"junk":  0,
		"86400": 24 * time.Hour,
	} {
		if got := idleTimeoutFromEnv(in); got != want {
			t.Errorf("idleTimeoutFromEnv(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIdleTrackerResetsOnTouch(t *testing.T) {
	tr := newIdleTracker()
	tr.last.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	if tr.idleFor() < time.Hour {
		t.Fatalf("expected ~2h idle, got %v", tr.idleFor())
	}
	tr.touch()
	if tr.idleFor() > time.Minute {
		t.Fatalf("touch should reset idleness, got %v", tr.idleFor())
	}
}

func TestIdleCheckEvery(t *testing.T) {
	if got := idleCheckEvery(24 * time.Hour); got != time.Minute {
		t.Errorf("long limit: %v", got)
	}
	if got := idleCheckEvery(5 * time.Second); got != time.Second {
		t.Errorf("short limit: %v", got)
	}
}
