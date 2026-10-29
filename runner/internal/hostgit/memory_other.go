//go:build !darwin

package hostgit

import "os/exec"

// Linux keeps the inherited RLIMIT_AS shell wrapper; other platforms are unchanged.
func platformMemoryWatch(_ Options, _ *exec.Cmd) *memoryWatch { return nil }
