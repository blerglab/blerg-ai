//go:build linux && persistent_shell_test

package shellruntime

import (
	"flag"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

// This entire file is absent from ordinary builds. Even an accidentally tagged
// daemon cannot activate the seam: testing.Init must have registered test.v.
func requirePersistentWireTestBinary() {
	if flag.Lookup("test.v") == nil {
		panic("persistent shell clock seam requires a Go test binary")
	}
}

var persistentWireHelperClockFile string

// SetPersistentWireHelperClock is only for the helper test subprocess. Production
// probe, identity resolution, bootstrap, containment and control are unchanged.
func SetPersistentWireHelperClock(path string) {
	requirePersistentWireTestBinary()
	persistentWireHelperClockFile = path
}
func persistentWireClock(path string) (time.Time, time.Duration, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: test-owned clock fixture; this file is excluded from production builds.
	if err != nil {
		return time.Time{}, 0, err
	}
	shift, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || shift < 0 {
		return time.Time{}, 0, ErrClosed
	}
	wall, elapsed, err := readPersistentClock()
	if err != nil {
		return time.Time{}, 0, err
	}
	return wall.Add(time.Duration(shift)), elapsed + time.Duration(shift), nil
}
func persistentClock() (ownershell.LeaseClock, error) {
	if persistentWireHelperClockFile == "" {
		return nativePersistentClock()
	}
	requirePersistentWireTestBinary()
	return func() (time.Time, time.Duration, error) { return persistentWireClock(persistentWireHelperClockFile) }, nil
}

// SetPersistentWireServiceClock supplies the same virtual time to the real
// Service and returns it for the Manager's existing clock parameter. Call before
// registering a launch or starting the daemon client. No probe is replaced.
func SetPersistentWireServiceClock(s *Service, path string) func() time.Time {
	requirePersistentWireTestBinary()
	now := func() time.Time {
		wall, _, err := persistentWireClock(path)
		if err != nil {
			return time.Time{}
		}
		return wall
	}
	s.now = now
	return now
}
