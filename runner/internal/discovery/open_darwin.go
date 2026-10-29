//go:build darwin

package discovery

import (
	"golang.org/x/sys/unix"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func openBeneath(root *os.File, name string, flags int) (*os.File, error) {
	rootInfo, err := root.Stat()
	if err != nil {
		return nil, err
	}
	dev, _ := fileIdentity(rootInfo)
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), ".")
	parts := strings.Split(name, "/")
	for i, part := range parts {
		nextFlags := unix.O_RDONLY | unix.O_DIRECTORY
		if i == len(parts)-1 {
			nextFlags = flags
		}
		child, err := unix.Openat(int(current.Fd()), part, nextFlags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = os.NewFile(uintptr(child), part)
		info, err := current.Stat()
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		childDev, _ := fileIdentity(info)
		if childDev != dev {
			_ = current.Close()
			return nil, errUnavailable
		}
	}
	return current, nil
}

func instanceIdentity(_ *os.File, info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return "birth:" + strconv.FormatInt(stat.Birthtimespec.Sec, 10) + ":" + strconv.FormatInt(stat.Birthtimespec.Nsec, 10)
}
func metadataVersion(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return "ctime:" + strconv.FormatInt(stat.Ctimespec.Sec, 10) + ":" + strconv.FormatInt(stat.Ctimespec.Nsec, 10)
}
