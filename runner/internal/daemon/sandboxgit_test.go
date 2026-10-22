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
	git("init", "-q", "-b", "main")
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

// The 0.4.2 linked-worktree path, now a clone like every sandbox: a session given a linked worktree
// works in its own clone of it, can commit and build there, and its commit lands on wip/<id> in
// the repository while neither checkout moves. This uses a disposable fixture and a prebuilt
// image, never a running daemon, real repository, engine login, host Git home or remote
// credentials.
func TestSandboxLinkedWorktreeCommitAndBuild(t *testing.T) {
	if os.Getenv("BLERG_SANDBOX_CHILD_TEST") != "1" {
		t.Skip("set BLERG_SANDBOX_CHILD_TEST=1 with a built sandbox image")
	}
	labelTestContainers(t)
	repo, child := sandboxGitFixture(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	before := childRepoGit(t, repo, "rev-parse", "HEAD")
	id := "worktree-test-" + ccUUID()
	clone, err := createSandboxClone(child, id, "")
	if err != nil {
		t.Fatalf("clone of the linked worktree: %v", err)
	}
	t.Cleanup(func() { discardSandboxClone(clone, id) })
	name, err := startSandboxContainer(id, clone, nil, sandboxCredential{}, SandboxOptions{})
	if err != nil {
		t.Fatalf("fixture sandbox failed: %v", err)
	}
	t.Cleanup(func() { releaseTestSandbox(id) })
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
node --test child.test.mjs`
	cmd := exec.Command("docker", "exec", "-e", "HOME=/tmp", "-e", "GOCACHE=/tmp/go-cache", "-e", "GOPATH=/tmp/go", name, "sh", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit/build in the clone failed: %s", out)
	}
	finishSandbox(id)
	if got := childRepoGit(t, repo, "rev-parse", "HEAD"); got != before {
		t.Fatal("the session moved the main checkout's HEAD")
	}
	if got := childRepoGit(t, child, "rev-parse", "--abbrev-ref", "HEAD"); got != "child" {
		t.Fatalf("the linked worktree is on %q", got)
	}
	// The build files were uncommitted at stop: committed for the session, on wip/<id>.
	if got := childRepoGit(t, repo, "log", "-2", "--format=%s", "wip/"+id); got != sandboxSnapshotMessage+"\nchild change" {
		t.Fatalf("wip/%s holds %q", id, got)
	}
	if out := childRepoGit(t, child, "status", "--porcelain"); out != "" {
		t.Fatalf("the linked worktree was written to:\n%s", out)
	}
}

// The last check before Docker: only the session's own recorded clone is ever mounted. A checkout,
// a linked worktree, a folder inside a checkout, a plain folder, a folder with a repository below
// it, a bare repository, another session's clone and a clone whose record is gone are all refused.
func TestSandboxMountRefusesACheckout(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	repo, worktree := sandboxGitFixture(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := t.TempDir()
	initChildRepo(t, filepath.Join(nested, "inner"))
	bare := filepath.Join(t.TempDir(), "bare.git")
	childRepoGit(t, filepath.Dir(bare), "init", "-q", "--bare", bare)
	refused := map[string]string{
		"checkout": repo, "linked worktree": worktree, "folder inside a checkout": sub,
		"plain folder": t.TempDir(), "folder with a repository below it": nested, "bare repository": bare,
	}
	for name, path := range refused {
		if err := sandboxMountRefusal(path, "s-mount"); err == nil {
			t.Fatalf("%s was accepted for the sandbox mount", name)
		}
		if _, err := startSandboxContainer("s-mount", path, nil, sandboxCredential{}, SandboxOptions{}); err == nil {
			removeSandboxContainer("s-mount")
			t.Fatalf("%s was mounted into a sandbox", name)
		}
	}
	clone, err := createSandboxClone(repo, "s-mount", "")
	if err != nil {
		t.Fatal(err)
	}
	// Again now that the session has a valid clone record: the path half of the check alone must
	// refuse every folder that is not that clone.
	for name, path := range refused {
		if err := sandboxMountRefusal(path, "s-mount"); err == nil {
			t.Fatalf("with a clone record, %s was accepted for the sandbox mount", name)
		}
		if _, err := startSandboxContainer("s-mount", path, nil, sandboxCredential{}, SandboxOptions{}); err == nil {
			removeSandboxContainer("s-mount")
			t.Fatalf("with a clone record, %s was mounted into a sandbox", name)
		}
	}
	if err := sandboxMountRefusal(clone, "s-mount"); err != nil {
		t.Fatalf("the session's own clone was refused: %v", err)
	}
	if err := sandboxMountRefusal(clone, "another-session"); err == nil {
		t.Fatal("another session's clone was accepted")
	}
	_, record, _ := sandboxClonePaths("s-mount")
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err := sandboxMountRefusal(clone, "s-mount"); err == nil {
		t.Fatal("a clone with no record was accepted")
	}
}
