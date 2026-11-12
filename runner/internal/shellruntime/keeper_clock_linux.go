//go:build linux

package shellruntime

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

func keeperRestartClock() (ownershell.LeaseClock, error) {
	release, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return nil, bridgeFailure("restart_kernel", err)
	}
	return keeperClockForKernel(string(release), readPersistentClock)
}

func keeperClockForKernel(release string, read ownershell.LeaseClock) (ownershell.LeaseClock, error) {
	if verifiedPersistentKernel(release) {
		if _, elapsed, err := read(); err != nil || elapsed < 0 {
			return nil, bridgeFailure("restart_clock", ErrClosed)
		}
		return read, nil
	}
	kernel := strings.ToLower(release)
	if !strings.Contains(kernel, "microsoft") && !strings.Contains(kernel, "wsl") {
		return nil, bridgeFailure("restart_kernel_unverified", ErrClosed)
	}
	return restartOnlyClock(read)
}

// This is not a sleep clock. Every helper continuously samples it; a sampling
// lapse, rollback or disagreement between wall and elapsed time is terminal.
// The existing nativePersistentClock/verifiedPersistentKernel sleep refusal is
// unchanged. BLG-400 is still required for WSL to use that sleep contract.
func restartOnlyClock(read ownershell.LeaseClock) (ownershell.LeaseClock, error) {
	wall, elapsed, err := read()
	if err != nil || elapsed < 0 {
		return nil, bridgeFailure("restart_clock_initial", ErrClosed)
	}
	wall = wall.Round(0) // compare actual wall time, not Go's embedded monotonic value
	var mu sync.Mutex
	failed := false
	return func() (time.Time, time.Duration, error) {
		mu.Lock()
		defer mu.Unlock()
		next, step, err := read()
		next = next.Round(0)
		delta, duration := next.Sub(wall), step-elapsed
		if err != nil || step < elapsed || delta < 0 || delta > 5*time.Second || duration > 5*time.Second || delta-duration > time.Second || duration-delta > time.Second {
			failed = true
		}
		if failed {
			return time.Time{}, 0, bridgeFailure("restart_clock_lapse", ErrClosed)
		}
		wall, elapsed = next, step
		return next, step, nil
	}, nil
}
