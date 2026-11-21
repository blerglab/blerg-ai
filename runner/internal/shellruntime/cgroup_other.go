//go:build !linux

package shellruntime

import "context"

func hostShellCgroupEnabled() bool { return false }

func newShellCgroup() (string, error) { return "", ErrClosed }

func validShellCgroup(string) bool { return false }

func emptyShellCgroup(context.Context, string) error { return nil }

// SweepShellCgroups has nothing to do without cgroups.
func SweepShellCgroups() error { return nil }

func RemoveEmptyShellCgroupParent() error { return nil }
