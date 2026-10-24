//go:build darwin

package daemon

import (
	"fmt"
	"os"
	"syscall"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"golang.org/x/sys/unix"
)

// os.Root pins the directory; the retained descriptor walk opens each component
// without following symlinks and revalidates its ancestry. Root.OpenFile follows final symlinks even
// if its caller supplies O_NOFOLLOW, so it is not the final open primitive.
func workspaceOpenFile(root *os.Root, name string) (*os.File, error) {
	return workspaceWalkOpen(root, name, nil)
}
func workspaceRegularFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return info.Mode().IsRegular() && ok && stat.Nlink == 1
}

func workspaceOpenDirectory(root *os.Root, name string) (*os.File, error) {
	return workspaceOpenFile(root, name)
}

func workspaceMetadataFingerprint(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d/%d/%d/%d/%d/%d", st.Dev, st.Ino, st.Ctimespec.Sec, st.Ctimespec.Nsec, info.ModTime().UnixNano(), info.Size())
}

func workspaceRootIdentity(info os.FileInfo) protocol.WorkspaceRoot {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return protocol.WorkspaceRoot{}
	}
	return protocol.WorkspaceRoot{Device: uint64(st.Dev), Inode: st.Ino}
}

func workspaceUnixIdentity(st unix.Stat_t) protocol.WorkspaceRoot {
	return protocol.WorkspaceRoot{Device: uint64(st.Dev), Inode: st.Ino}
}
