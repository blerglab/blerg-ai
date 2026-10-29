package hostgit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxGitLookupRemainsDynamic(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := Initialize(); err != nil {
		t.Fatalf("Linux initialization must not require Git: %v", err)
	}
	if _, err := Binary(); !errors.Is(err, ErrNoGit) {
		t.Fatalf("missing Git: %v", err)
	}
	for range 2 {
		dir := t.TempDir()
		path := filepath.Join(dir, "git")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		if got, err := Binary(); err != nil || got != path {
			t.Fatalf("Linux PATH change was cached: %q %v", got, err)
		}
	}
}

func TestLinuxGitAncestorPolicyRefusesAdminWrite(t *testing.T) {
	const path = "/Applications/Xcode.app/Contents/Developer/usr/bin/git"
	lookup := cachedDarwinGit(func() (string, error) { return path, nil },
		func(p string) (string, error) { return p, nil },
		func(p string) (os.FileInfo, error) {
			info := darwinGitTestInfo(p, 0).(gitValidationInfo)
			if p == "/Applications" {
				info.mode = os.ModeDir | 0775
				info.gid = 80
			}
			return info, nil
		}, 0)
	if got, err := lookup(); got != "" || !errors.Is(err, ErrNoGit) {
		t.Fatalf("Linux accepted root:admin 0775: %q %v", got, err)
	}
}
