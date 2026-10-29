//go:build darwin

package updatehandoff

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func ProcessStart(pid int) string {
	b, err := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
func ancestors(pid int) map[int]string {
	out := make(map[int]string)
	for n := 0; pid > 1 && n < 128; n++ {
		if _, ok := out[pid]; ok {
			break
		}
		out[pid] = ProcessStart(pid)
		b, err := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "ppid=").Output()
		if err != nil {
			break
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	return out
}
func processSession(pid int) string {
	b, err := exec.Command("/bin/ps", "eww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	for _, kv := range strings.Fields(string(b)) {
		if s, ok := strings.CutPrefix(kv, "BLERG_RUNNER_SESSION_ID="); ok {
			return s
		}
	}
	return ""
}
func peerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var sockErr error
	err = raw.Control(func(fd uintptr) { pid, sockErr = unix.GetsockoptInt(int(fd), 0, unix.LOCAL_PEERPID) })
	if err != nil {
		return 0, err
	}
	if sockErr != nil {
		return 0, sockErr
	}
	if pid <= 1 {
		return 0, fmt.Errorf("update caller unavailable")
	}
	return pid, nil
}
