package releaseperf

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// HostState is the host-quiet precondition's input, read from /proc (or
// injected directly in tests).
type HostState struct {
	Load1           float64
	LogicalCPUs     int
	AvailableMemGiB float64
}

const minAvailableMemGiB = 4.0
const loadFractionOfCPUs = 0.5

// CheckHostQuiet refuses a run on a host that is not quiet enough: either
// the 1-minute load average is more than half the logical CPUs, or available
// memory is under the floor one suite needs. This runs alone, with no
// fallback degraded mode, so it refuses rather than guesses.
func CheckHostQuiet(h HostState) error {
	if threshold := loadFractionOfCPUs * float64(h.LogicalCPUs); h.Load1 > threshold {
		return fmt.Errorf("load average %.2f exceeds %.2f (0.5 * %d logical CPUs); host is not quiet", h.Load1, threshold, h.LogicalCPUs)
	}
	if h.AvailableMemGiB < minAvailableMemGiB {
		return fmt.Errorf("available memory %.2f GiB is under the %.0f GiB floor", h.AvailableMemGiB, minAvailableMemGiB)
	}
	return nil
}

// ErrConcurrentRun means a pidfile names a process that is still alive: a
// real concurrent release-perf run, not a crash.
var ErrConcurrentRun = errors.New("a release-perf run is already in progress")

// ErrNoPidfile means no pidfile is present (removed, or never written).
var ErrNoPidfile = errors.New("no pidfile present")

func currentPID() int { return os.Getpid() }

// WritePidfile records a PID so a later invocation can tell a live run from
// a crash's leftovers.
func WritePidfile(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600)
}

// ReadPidfile returns the recorded PID, or ErrNoPidfile if none is present.
func ReadPidfile(path string) (int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is UISuiteLockPath's own hash-keyed cache path, never caller input
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNoPidfile
		}
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("pidfile %s: %w", path, err)
	}
	return pid, nil
}

// CheckPidfile distinguishes a genuinely concurrent run from a crashed
// prior run's leftovers: no pidfile is fine; a pidfile naming a dead process
// is stale and is removed so the run proceeds; a pidfile naming a live
// process is a real concurrent run and refuses.
func CheckPidfile(path string) error {
	pid, err := ReadPidfile(path)
	if errors.Is(err, ErrNoPidfile) {
		return nil
	}
	if err != nil {
		return err
	}
	if processAlive(pid) {
		return fmt.Errorf("%w: pid %d (pidfile %s)", ErrConcurrentRun, pid, path)
	}
	// Stale: a crashed prior run never cleaned up after itself. Self-heal.
	if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
		return fmt.Errorf("removing stale pidfile %s: %w", path, rerr)
	}
	return nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal 0 only checks existence/permission; it never actually signals
	// the process.
	return syscall.Kill(pid, 0) == nil
}
