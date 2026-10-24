//go:build linux || darwin

package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// External metadata is permitted only by registered-worktree proof. Refuse
// symlinks in every component before reading any of its pointer files.
func openWorkspaceExternalRoot(name string) (*os.Root, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, os.ErrPermission
	}
	anchor, err := os.OpenRoot("/")
	if err != nil {
		return nil, err
	}
	defer func() { _ = anchor.Close() }()
	relative := strings.TrimPrefix(name, "/")
	if relative == "" {
		relative = "."
	}
	file, err := workspaceWalkOpenOptions(anchor, relative, nil, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	prefix := "/proc/self/fd/"
	if runtime.GOOS == "darwin" {
		prefix = "/dev/fd/"
	}
	return os.OpenRoot(prefix + strconv.FormatUint(uint64(file.Fd()), 10))
}
