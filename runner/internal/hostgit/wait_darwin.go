package hostgit

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// kern.proc.pid includes unreaped zombies. Unlike Wait4 this observation never
// releases the child identity. Darwin's public x/sys API has no Waitid wrapper;
// use the supported typed sysctl rather than a private waitid ABI or raw trap.
func platformObserveExit(pid int) error {
	return observeDarwinExit(pid, func(pid int) (int, int8, error) {
		info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err != nil {
			return 0, 0, fmt.Errorf("observe Git exit: %w", err)
		}
		return int(info.Proc.P_pid), info.Proc.P_stat, nil
	})
}
