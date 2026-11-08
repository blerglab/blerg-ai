//go:build linux

package shellruntime

import (
	"golang.org/x/sys/unix"
	"os"
)

// Protect both process endpoints before transmitting bootstrap secrets. Socket
// descriptors cannot be reopened through /proc like anonymous pipe descriptors.
func protectHelperProcess() error { return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) }
func helperTransport(retained bool) (*os.File, *os.File, error) {
	if !retained {
		return os.Pipe()
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "shell-private"), os.NewFile(uintptr(fds[1]), "shell-private"), nil
}
