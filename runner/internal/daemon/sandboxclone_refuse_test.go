package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Security round 1 (#136, I1b): what no override reaches on the host's upload-pack side is refused
// before git runs, with what the session should do; read as plain files, never by git (a FIFO
// here would hang any reader).
func TestSandboxFetchRefusesWhatTheHostWouldParse(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, gitDir string)
		want  string
	}{
		{"info/attributes", fifoAt("info/attributes"), ".git/info/attributes"},
		{"info/grafts", fifoAt("info/grafts"), ".git/info/grafts"},
		{"objects/info/grafts", fifoAt("objects/info/grafts"), ".git/objects/info/grafts"},
		{"config.worktree", fileAt("config.worktree", "[core]\n"), ".git/config.worktree"},
		{"include", appendConfig("[include]\n\tpath = /dev/zero\n"), "[include]"},
		{"includeIf", appendConfig("[includeIf \"gitdir:/\"]\n\tpath = x\n"), "[includeIf]"},
		{"spaced and cased", appendConfig("[ InClUdE ]\n\tpath = x\n"), "[include]"},
		{"old subsection syntax", appendConfig("[include.x]\n\tpath = x\n"), "[include]"},
		{".git/.gitattributes", fileAt(".gitattributes", "* delta\n"), ".git/.gitattributes"},
		{".git/<dir>/.gitattributes", fileAt("refs/.GitAttributes", "* delta\n"), ".git/refs/.GitAttributes"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := "refuse-" + string(rune('a'+i))
			repo, clone := cloneFixture(t, id)
			childCommit(t, clone, "work.txt")
			before := childRepoGit(t, repo, "for-each-ref")
			c.plant(t, filepath.Join(clone, ".git"))
			_, err := syncSandboxBranch(clone, id, "")
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "remove it in the sandbox") {
				t.Fatalf("not refused clearly: %v", err)
			}
			if after := childRepoGit(t, repo, "for-each-ref"); after != before {
				t.Fatal("a refused fetch changed the repository's refs")
			}
		})
	}
	// A clone with none of these (its own alternates included, rewritten from the record) syncs.
	repo, clone := cloneFixture(t, "refuse-none")
	childCommit(t, clone, "work.txt")
	if commits, err := syncSandboxBranch(clone, "refuse-none", ""); err != nil || !commits {
		t.Fatalf("clean clone: %v %v", commits, err)
	}
	_ = repo

	// Everything under .git counts toward a cap, not only the objects (#136 re-review, nit 3).
	prev := sandboxCloneMaxMetaBytes
	sandboxCloneMaxMetaBytes = 64 << 10
	t.Cleanup(func() { sandboxCloneMaxMetaBytes = prev })
	_, big := cloneFixture(t, "refuse-meta")
	childCommit(t, big, "work.txt")
	if err := os.WriteFile(filepath.Join(big, ".git", "packed-refs"), make([]byte, 128<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := syncSandboxBranch(big, "refuse-meta", ""); err == nil || !strings.Contains(err.Error(), "outside its objects") {
		t.Fatalf("a large packed-refs was not refused: %v", err)
	}
}

func fifoAt(name string) func(*testing.T, string) {
	return func(t *testing.T, gitDir string) {
		t.Helper()
		path := filepath.Join(gitDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func fileAt(name, content string) func(*testing.T, string) {
	return func(t *testing.T, gitDir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func appendConfig(text string) func(*testing.T, string) {
	return func(t *testing.T, gitDir string) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(gitDir, "config"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
}
