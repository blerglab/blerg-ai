package testsupport

import (
	"os"
	"testing"
	"time"
)

// Read once: disabled instrumentation does no clock reads, logging or allocation.
var timingEnabled = os.Getenv("BLERG_TEST_FIXTURE_TIMING") == "1"

// Timer measures an owned fixture phase. Phases are non-overlapping within a fixture.
type Timer struct {
	t     testing.TB
	phase string
	start time.Time
}

// Phase starts opt-in timing; End is safe on a disabled timer.
func Phase(t testing.TB, phase string) Timer {
	if !timingEnabled {
		return Timer{}
	}
	return Timer{t: t, phase: phase, start: time.Now()}
}

// End logs only the phase and elapsed nanoseconds, never connection details.
func (p Timer) End() {
	if p.start.IsZero() {
		return
	}
	p.t.Logf("fixture_phase=%s ns=%d", p.phase, time.Since(p.start).Nanoseconds())
}

// Cleanup ends a body phase before earlier fixture cleanups. Disabled timing
// must not add a cleanup callback or allocate its captured timer.
func (p Timer) Cleanup() {
	if p.start.IsZero() {
		return
	}
	p.t.Cleanup(p.End)
}
