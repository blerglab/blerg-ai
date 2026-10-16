package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageProblemWritableDirIsQuiet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BLERG_RUNNER_DATA_DIR", dir)
	if msg := StorageProblem(); msg != "" {
		t.Fatalf("a writable data dir reported a problem: %s", msg)
	}
	if _, err := os.Stat(filepath.Join(dir, "artifacts")); err != nil {
		t.Errorf("artifacts dir not created: %v", err)
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "artifacts"))
	if len(ents) != 0 {
		t.Errorf("the check left files behind: %v", ents)
	}
}

func TestStorageProblemNamesTheDirectoryUserErrorAndFix(t *testing.T) {
	// A file where the data directory should be fails the same way for every user, root included.
	blocker := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLERG_RUNNER_DATA_DIR", blocker)
	msg := StorageProblem()
	for _, want := range []string{
		"STORAGE NOT WRITABLE", blocker, "user id", "not a directory", "storage error",
		"./blerg-up.sh", "install/desktop/DAEMON.md", "Kubernetes", "fsGroup: 65532",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("must be one log line: %q", msg)
	}
}

func TestStorageProblemReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Setenv("BLERG_RUNNER_DATA_DIR", dir)
	msg := StorageProblem()
	if !strings.Contains(msg, dir) || !strings.Contains(msg, "permission denied") {
		t.Errorf("unexpected message: %s", msg)
	}
}

func TestStorageProblemArtifactsDirOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	dir := t.TempDir()
	art := filepath.Join(dir, "artifacts")
	if err := os.Mkdir(art, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(art, 0o700) })
	t.Setenv("BLERG_RUNNER_DATA_DIR", dir)
	if msg := StorageProblem(); !strings.Contains(msg, art) {
		t.Errorf("want the artifacts dir named: %s", msg)
	}
}
