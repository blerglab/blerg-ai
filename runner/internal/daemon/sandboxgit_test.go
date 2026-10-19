package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sandboxGitFixture(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "main")
	child := filepath.Join(base, "child")
	if err := os.Mkdir(repo, 0755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + base, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git failed: %s", out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "Example Developer")
	git("config", "user.email", "developer@example.com")
	if err := os.WriteFile(filepath.Join(repo, "initial.txt"), []byte("initial\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "initial")
	git("worktree", "add", "-qb", "child", child)
	return repo, child
}

// This uses a disposable fixture and a prebuilt image, never a running daemon,
// real repository, engine login, host Git home or remote credentials.
func TestSandboxChildWorktreeCommitAndBuild(t *testing.T) {
	if os.Getenv("BLERG_SANDBOX_CHILD_TEST") != "1" {
		t.Skip("set BLERG_SANDBOX_CHILD_TEST=1 with a built sandbox image")
	}
	repo, child := sandboxGitFixture(t)
	before, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the production launch path with an empty fixture home: no
	// engine login, Git config or credentials from the operator enter it.
	t.Setenv("HOME", t.TempDir())
	id := "child-test-" + ccUUID()
	name, err := startSandboxContainer(id, child, nil, sandboxCredential{}, SandboxOptions{})
	if err != nil {
		t.Fatalf("fixture sandbox failed: %v", err)
	}
	t.Cleanup(func() { removeSandboxContainer(id) })
	script := `set -eu
printf 'child\n' > child.txt
git add child.txt
git -c user.name='Example Child' -c user.email=child@example.com commit -qm 'child change'
printf 'module example.com/child\n\ngo 1.26.9\n' > go.mod
printf 'package child\nfunc Answer() int { return 42 }\n' > child.go
printf 'package child\nimport "testing"\nfunc TestAnswer(t *testing.T) { if Answer()!=42 { t.Fatal("answer") } }\n' > child_test.go
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test ./...
printf 'import test from "node:test"; import assert from "node:assert/strict"; test("child",()=>assert.equal(6*7,42));\n' > child.test.mjs
node -e 'const [major,minor]=process.versions.node.split(".").map(Number);if(major<22||(major===22&&minor<12))process.exit(1)'
node --test child.test.mjs
git status --porcelain`
	cmd := exec.Command("docker", "exec", "-e", "HOME=/tmp", "-e", "GOCACHE=/tmp/go-cache", "-e", "GOPATH=/tmp/go", name, "sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child commit/build failed: %s", out)
	}
	after, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if string(after) != string(before) {
		t.Fatal("child changed parent checkout HEAD")
	}
	if out, err := exec.Command("git", "-C", repo, "log", "-1", "--format=%s", "child").Output(); err != nil || strings.TrimSpace(string(out)) != "child change" {
		t.Fatal("parent cannot see child commit")
	}
}

func TestSandboxWorktreeGitBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string, string)
	}{
		{"world writable", func(t *testing.T, r, w string) {
			if err := os.Chmod(filepath.Join(r, ".git"), 0777); err != nil {
				t.Fatal(err)
			}
		}},
		{"wrong backlink", func(t *testing.T, r, w string) {
			if err := os.WriteFile(filepath.Join(r, ".git", "worktrees", "child", "gitdir"), []byte(filepath.Join(r, ".git")), 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{"arbitrary directory", func(t *testing.T, r, w string) {
			if err := os.WriteFile(filepath.Join(w, ".git"), []byte("gitdir: "+t.TempDir()), 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlinked objects", func(t *testing.T, r, w string) {
			p := filepath.Join(r, ".git", "objects")
			if err := os.Rename(p, p+"-old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), p); err != nil {
				t.Fatal(err)
			}
		}},
		{"external alternates", func(t *testing.T, r, w string) {
			if err := os.WriteFile(filepath.Join(r, ".git", "objects", "info", "alternates"), []byte(t.TempDir()), 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{"foreign owner", func(t *testing.T, r, w string) {
			old := ownedByDaemon
			ownedByDaemon = func(os.FileInfo) bool { return false }
			t.Cleanup(func() { ownedByDaemon = old })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := sandboxGitFixture(t)
			tc.mutate(t, r, w)
			if _, err := sandboxWorktreeGit(w); err == nil {
				t.Fatal("unsafe metadata was accepted")
			}
		})
	}
}
func TestSandboxWorktreeGitMountScope(t *testing.T) {
	repo, child := sandboxGitFixture(t)
	m, err := sandboxWorktreeGit(child)
	if err != nil {
		t.Fatal(err)
	}
	if m.source != filepath.Join(repo, ".git") || m.target != m.source {
		t.Fatal("metadata must be mounted at the pointer's absolute path")
	}
	args := sandboxRunArgs("fixture", child, "", []string{"GITHUB_TOKEN=secret-sentinel", "SSH_AUTH_SOCK=/socket", "GIT_ASKPASS=/secret"}, sandboxRunExtras{gitMetadata: m})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "src="+m.source+",dst="+m.target) || strings.Contains(joined, "readonly") {
		t.Fatal("shared Git metadata must be writable")
	}
	for _, bad := range []string{"secret-sentinel", "SSH_AUTH_SOCK", "GIT_ASKPASS", ".git-credentials", ".ssh"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("sandbox exposed push authority: %s", bad)
		}
	}
	if m, err := sandboxWorktreeGit(repo); err != nil || m != nil {
		t.Fatal("ordinary checkout needs no extra Git mount")
	}
}
func TestSandboxWorktreeRelativeGitPointer(t *testing.T) {
	repo, child := sandboxGitFixture(t)
	rel, _ := filepath.Rel(child, filepath.Join(repo, ".git", "worktrees", "child"))
	if err := os.WriteFile(filepath.Join(child, ".git"), []byte("gitdir: "+rel+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := sandboxWorktreeGit(child)
	if err != nil {
		t.Fatal(err)
	}
	if m.target != "/main/.git" {
		t.Fatalf("relative pointer mount target %q", m.target)
	}
}

func TestSandboxWorktreeRejectsMetadataReplacement(t *testing.T) {
	repo, child := sandboxGitFixture(t)
	m, err := sandboxWorktreeGit(child)
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(repo, ".git")
	if err := os.Rename(common, common+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), common); err != nil {
		t.Fatal(err)
	}
	if err := m.validate(); err == nil {
		t.Fatal("metadata replacement before Docker bind was accepted")
	}
}
