//go:build unix

package pluginspec

import (
	"os"
	"path/filepath"
	"syscall"
)

// The separate lock file survives atomic replacement of the policy file and is
// released by the OS when a writer exits, including during a rolling update.
func lockPolicy(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // G304: trusted holder state path, never request input.
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
