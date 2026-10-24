//go:build unix

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func workspaceDirectoryIdentity(path string) (protocol.WorkspaceRoot, error) {
	// Reject links in every component, including the allocation's parents.
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil || !st.IsDir() {
			return protocol.WorkspaceRoot{}, errors.New("workspace directory unavailable or linked")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	st, err := os.Lstat(path) // #nosec G703 -- trusted P1/private-journal path; every component checked above, identity compared by caller.
	if err != nil || !safeSandboxGitInfo(st, true) {
		return protocol.WorkspaceRoot{}, errors.New("workspace directory is not owned and protected")
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return protocol.WorkspaceRoot{}, errors.New("workspace identity unavailable")
	}
	return protocol.WorkspaceRoot{Device: uint64(stat.Dev), Inode: stat.Ino}, nil //nolint:unconvert // Darwin and Linux use different device integer types
}
