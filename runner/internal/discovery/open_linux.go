//go:build linux

package discovery

import (
	"golang.org/x/sys/unix"
	"os"
	"strconv"
	"syscall"
)

func openBeneath(root *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat2(int(root.Fd()), name, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK), //nolint:gosec // caller supplies positive read-only O_* flags only
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// statx birth time distinguishes a recreated directory even if its inode is
// immediately reused. Filesystems without birth time get conservative ctime
// snapshots; metadata writes may then require a fresh picker selection.
func instanceIdentity(file *os.File, info os.FileInfo) string {
	var stat unix.Statx_t
	if unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &stat) == nil && stat.Mask&unix.STATX_BTIME != 0 {
		return "birth:" + strconv.FormatInt(stat.Btime.Sec, 10) + ":" + strconv.FormatUint(uint64(stat.Btime.Nsec), 10)
	}
	return metadataVersion(info)
}
func metadataVersion(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return "ctime:" + strconv.FormatInt(stat.Ctim.Sec, 10) + ":" + strconv.FormatInt(stat.Ctim.Nsec, 10)
}
