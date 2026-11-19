package testsupport

import (
	"testing"
	"time"
)

func TestFixtureTimingDisabled(t *testing.T) {
	if timingEnabled {
		t.Skip("requires timing disabled")
	}
	if got := Phase(t, "probe"); !got.start.IsZero() {
		t.Fatal("disabled timing read the clock")
	}
	if n := testing.AllocsPerRun(100, func() { p := Phase(t, "probe"); p.End(); p.Cleanup() }); n != 0 {
		t.Fatalf("disabled timing allocated: %v", n)
	}
}

func TestFixtureTimingEnabled(t *testing.T) {
	if !timingEnabled {
		t.Skip("requires timing enabled")
	}
	p := Phase(t, "probe")
	if p.start.IsZero() || p.start.After(time.Now()) {
		t.Fatal("enabled timing did not start")
	}
	p.End()
}
