//go:build linux

package updatehandoff

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func processStat(pid int) (int, string) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, ""
	}
	end := strings.LastIndex(string(b), ")")
	if end < 0 {
		return 0, ""
	}
	fields := strings.Fields(string(b)[end+1:])
	if len(fields) < 20 {
		return 0, ""
	}
	parent, _ := strconv.Atoi(fields[1])
	return parent, fields[19]
}
func ProcessStart(pid int) string { _, start := processStat(pid); return start }

// systemd places timer-launched installers outside the engine's ancestry. Use
// kernel cgroup membership, never a caller-supplied session or unit identity.
func processUnit(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	return cgroupUnit(string(b))
}
func cgroupUnit(data string) string {
	unit := ""
	for _, line := range strings.Split(data, "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 || (fields[0] != "0" || fields[1] != "") && fields[1] != "name=systemd" {
			continue
		}
		if !strings.HasPrefix(fields[2], "/") {
			return ""
		}
		parts := strings.Split(fields[2], "/")
		if len(parts) > 0 && strings.HasSuffix(parts[len(parts)-1], ".service") {
			candidate := parts[len(parts)-1]
			if unit != "" && candidate != unit {
				return ""
			}
			unit = candidate
		}
	}
	return unit
}
func ancestors(pid int) map[int]string {
	out := make(map[int]string)
	for n := 0; pid > 1 && n < 128; n++ {
		if _, ok := out[pid]; ok {
			break
		}
		parent, start := processStat(pid)
		out[pid] = start
		pid = parent
	}
	return out
}
func processSession(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return ""
	}
	for _, kv := range strings.Split(string(b), "\x00") {
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
	var cred *unix.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) { cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	if int64(cred.Uid) != int64(os.Geteuid()) {
		return 0, fmt.Errorf("update caller belongs to another user")
	}
	return int(cred.Pid), nil
}
