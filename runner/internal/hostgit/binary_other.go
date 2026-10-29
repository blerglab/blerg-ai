//go:build !darwin

package hostgit

import (
	"os/exec"
	"path/filepath"
)

func platformInitializeGit() error { return nil }

func platformGitBinary() (string, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return "", ErrNoGit
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path, nil
}
