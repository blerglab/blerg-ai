package hostgit

import (
	"os"
	"path/filepath"
)

var resolvedDarwinGit = cachedDarwinGit(func() (string, error) {
	return discoverDarwinGit(runDarwinGitDiscovery, filepath.EvalSymlinks, os.Lstat, os.Geteuid())
}, filepath.EvalSymlinks, os.Lstat, os.Geteuid())

func platformGitBinary() (string, error) { return resolvedDarwinGit() }
func platformInitializeGit() error       { _, err := Binary(); return err }
