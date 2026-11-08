//go:build linux

package activity

import (
	"golang.org/x/sys/unix"
	"runtime"
	"time"
)

// SampleMachine reports the runtime that ran this session, never the server's
// machine. No hostname, address, path, or process inventory is collected.
func SampleMachine(session, path string) *Machine {
	var sys unix.Sysinfo_t
	var disk unix.Statfs_t
	if unix.Sysinfo(&sys) != nil || unix.Statfs(path, &disk) != nil || disk.Bsize < 0 {
		return nil
	}
	return &Machine{SessionID: session, At: time.Now().UTC(), Load1: float64(sys.Loads[0]) / 65536, CPUs: runtime.GOMAXPROCS(0), DiskFreeBytes: disk.Bavail * uint64(disk.Bsize)}
}
