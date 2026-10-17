//go:build linux || darwin

package daemon

import (
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// os.Root pins the directory, while the descriptor walk refuses symlinks in
// every component atomically. Root.OpenFile itself follows final symlinks even
// if its caller supplies O_NOFOLLOW, so it is not the final open primitive.
func workspaceOpenFile(root *os.Root, name string) (*os.File, error) {
	anchor, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = anchor.Close() }()
	current := int(anchor.Fd())
	owned := false
	defer func() {
		if owned {
			_ = unix.Close(current)
		}
	}()
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		if owned {
			_ = unix.Close(current)
		}
		current = next
		owned = true
	}
	fd, err := unix.Openat(current, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func workspaceRegularFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return info.Mode().IsRegular() && ok && stat.Nlink == 1
}
