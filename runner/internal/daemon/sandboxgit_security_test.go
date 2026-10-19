package daemon

import (
	"context"
	"encoding/csv"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSandboxGitRecheckRejectsChangedAuthorization(t *testing.T) {
	for _, kind := range []string{"pointer", "backlink", "world writable metadata"} {
		t.Run(kind, func(t *testing.T) {
			repo, child := sandboxGitFixture(t)
			metadata, err := sandboxWorktreeGit(child)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "pointer":
				err = os.WriteFile(filepath.Join(child, ".git"), []byte("gitdir: /another/repo.git/worktrees/child\n"), 0644)
			case "backlink":
				err = os.WriteFile(filepath.Join(repo, ".git", "worktrees", "child", "gitdir"), []byte(t.TempDir()), 0644)
			case "world writable metadata":
				err = os.Chmod(filepath.Join(repo, ".git", "objects"), 0777)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := metadata.validate(); err == nil {
				t.Fatal("changed checkout authority was accepted before bind")
			}
		})
	}
}

func TestSandboxGitMountCSVDoesNotCreateAdditionalFields(t *testing.T) {
	for _, source := range []string{"/repos/code:child/.git", "/repos/code,readonly/.git", "/repos/\"quoted\"/.git", "/repos/code,dst=/etc/.git"} {
		target := "/target/child.git"
		fields, err := csv.NewReader(strings.NewReader(sandboxBindMount(source, target))).Read()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fields, []string{"type=bind", "src=" + source, "dst=" + target}) {
			t.Fatalf("source became extra Docker mount fields: %q", fields)
		}
	}
}

func TestSandboxGitMountTargetCannotShadowRuntimeRoots(t *testing.T) {
	for _, path := range []string{"/", "/workspace", "/workspace/nested.git", "/home/agent/.git", "/etc/repo.git", "/usr/local/repo.git", "/proc/123/git", "/run/repo.git", "/root/repo.git"} {
		if safeSandboxGitTarget(path) {
			t.Fatalf("reserved mount target accepted: %s", path)
		}
	}
	for _, path := range []string{"/workspace-other/repo.git", "/home/another/repo.git", "/tmp/repo.git", "/srv/repos/repo/.git"} {
		if !safeSandboxGitTarget(path) {
			t.Fatalf("ordinary absolute metadata target rejected: %s", path)
		}
	}
}

// Subprocess timeout makes a FIFO regression fail without leaking a blocked reader
// into this package process. The helper gets only a temporary fixture path.
func TestSandboxGitAlternatesAreBoundedRegularFiles(t *testing.T) {
	if path := os.Getenv("BLERG_TEST_GIT_METADATA_READ"); path != "" {
		if _, err := sandboxGitRead(path); err == nil {
			t.Fatal("unsafe metadata file accepted")
		}
		return
	}
	for _, kind := range []string{"FIFO", "large file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			repo, child := sandboxGitFixture(t)
			path := filepath.Join(repo, ".git", "objects", "info", "alternates")
			switch kind {
			case "FIFO":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "large file":
				if err := os.WriteFile(path, []byte(strings.Repeat(" ", 8193)), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "empty")
				if err := os.WriteFile(target, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestSandboxGitAlternatesAreBoundedRegularFiles$")
			cmd.Env = []string{"BLERG_TEST_GIT_METADATA_READ=" + path}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("metadata rejection blocked or failed: %s: %v", out, err)
			}
			if _, err := sandboxWorktreeGit(child); err == nil {
				t.Fatal("unsafe object alternates accepted for container mount")
			}
		})
	}
}
