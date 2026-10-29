package hostgit

import (
	"errors"

	"golang.org/x/sys/unix"
)

// Observe exit without releasing the leader's PID/PGID. Only runCommand reaps,
// after the group signal and memory-supervisor shutdown have finished.
func platformObserveExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
