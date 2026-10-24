//go:build linux

package daemon

import (
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// No fallback: old kernels and filesystems unable to honor openat2 are
// unavailable. NO_XDEV also refuses same-device bind mounts.
func workspaceOpenFile(root *os.Root, name string) (*os.File, error) {
	anchor, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = anchor.Close() }()
	fd, err := unix.Openat2(int(anchor.Fd()), name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func workspaceOpenDirectory(root *os.Root, name string) (*os.File, error) {
	return workspaceOpenFile(root, name)
}
func workspaceRegularFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return info.Mode().IsRegular() && ok && stat.Nlink == 1
}

func workspaceMetadataFingerprint(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d/%d/%d/%d/%d/%d", st.Dev, st.Ino, st.Ctim.Sec, st.Ctim.Nsec, info.ModTime().UnixNano(), info.Size())
}

func workspaceRootIdentity(info os.FileInfo) protocol.WorkspaceRoot {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return protocol.WorkspaceRoot{}
	}
	return protocol.WorkspaceRoot{Device: st.Dev, Inode: st.Ino}
}

func workspaceUnixIdentity(st unix.Stat_t) protocol.WorkspaceRoot {
	return protocol.WorkspaceRoot{Device: st.Dev, Inode: st.Ino}
}
