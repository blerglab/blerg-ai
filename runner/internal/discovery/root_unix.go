//go:build linux || darwin

package discovery

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The configured root is an explicit filesystem choice. Walk its absolute
// ancestors without symlinks; mounts are allowed up to that choice. Every
// descendant open is then constrained by openBeneath, including bind mounts.
func openConfiguredRoot(name string) (*os.File, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, errUnavailable
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "/")
	for _, part := range strings.Split(strings.TrimPrefix(name, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = os.NewFile(uintptr(next), part)
	}
	return current, nil
}
func fileIdentity(info os.FileInfo) (uint64, uint64) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Dev), uint64(stat.Ino) //nolint:unconvert // Darwin and Linux use different stat field widths
	}
	return 0, 0
}
func regularPrivateMetadata(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && stat.Nlink == 1
}

const directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY
const regularFlags = unix.O_RDONLY
