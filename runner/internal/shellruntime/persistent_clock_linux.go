//go:build linux

package shellruntime

import (
	"os"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"golang.org/x/sys/unix"
)

// Native Linux CLOCK_BOOTTIME includes kernel suspend. WSL and Docker Desktop
// need separate real host-sleep evidence: their guest clock alone proves none.
func verifiedPersistentKernel(release string) bool {
	release = strings.ToLower(strings.TrimSpace(release))
	return release != "" && !strings.Contains(release, "microsoft") && !strings.Contains(release, "wsl") && !strings.Contains(release, "linuxkit") && !strings.Contains(release, "docker-desktop")
}

func readPersistentClock() (time.Time, time.Duration, error) {
	var stamp unix.Timespec
	if unix.ClockGettime(unix.CLOCK_BOOTTIME, &stamp) != nil || stamp.Sec < 0 || stamp.Nsec < 0 || stamp.Nsec >= int64(time.Second) || stamp.Sec > (int64(1<<63-1)-stamp.Nsec)/int64(time.Second) {
		return time.Time{}, 0, ErrClosed
	}
	return time.Now(), time.Duration(stamp.Sec)*time.Second + time.Duration(stamp.Nsec), nil
}

func nativePersistentClock() (ownershell.LeaseClock, error) {
	release, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil || !verifiedPersistentKernel(string(release)) {
		return nil, ErrClosed
	}
	if _, _, err = readPersistentClock(); err != nil {
		return nil, ErrClosed
	}
	return readPersistentClock, nil
}
