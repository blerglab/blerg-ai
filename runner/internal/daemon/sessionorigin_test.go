package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func originGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test"}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %s failed: %v", args[0], err)
	}
	return strings.TrimSpace(string(raw))
}
func originBinding(t *testing.T, dir string) workspaceFileBinding {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceFileBinding{root: dir, info: info, live: func() bool { return true }}
}
func TestSessionOriginCommittedUnbornAndNonRepository(t *testing.T) {
	dir := t.TempDir()
	b := originBinding(t, dir)
	if got, ok := readWorkspaceOrigin(context.Background(), b, nil); !ok || got != "" {
		t.Fatal("non-repository not unknown")
	}
	originGit(t, dir, "init", "-q")
	if got, ok := readWorkspaceOrigin(context.Background(), b, nil); !ok || got != "" {
		t.Fatal("unborn HEAD not unknown")
	}
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "Fixture")
	want := originGit(t, dir, "rev-parse", "HEAD")
	if got, ok := readWorkspaceOrigin(context.Background(), b, nil); !ok || got != want {
		t.Fatal("commit not from actual workspace")
	}
	nested := filepath.Join(dir, "not-a-repository")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if got, ok := readWorkspaceOrigin(t.Context(), originBinding(t, nested), nil); !ok || got != "" {
		t.Fatal("workspace inherited a parent repository's HEAD")
	}
}
func TestSessionOriginRefusesExpiredAndRetargetedWorkspaceLease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	b := originBinding(t, dir)
	b.live = func() bool { return false }
	if _, ok := readWorkspaceOrigin(t.Context(), b, nil); ok {
		t.Fatal("expired lease accepted")
	}
	b.live = func() bool { return true }
	old := dir + "-old"
	if err := os.Rename(dir, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, ok := readWorkspaceOrigin(t.Context(), b, nil); ok {
		t.Fatal("replacement root accepted")
	}
}
func TestSessionOriginRechecksLeaseAfterGitAndDoesNotRunWorkspaceHooks(t *testing.T) {
	dir := t.TempDir()
	b := originBinding(t, dir)
	originGit(t, dir, "init", "-q")
	originGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "Fixture")
	marker := filepath.Join(t.TempDir(), "executed")
	script := filepath.Join(dir, "fsmonitor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	originGit(t, dir, "config", "core.fsmonitor", script)
	if _, ok := readWorkspaceOrigin(t.Context(), b, nil); !ok {
		t.Fatal("safe revision read failed")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("repository fsmonitor executed")
	}
	live := true
	b.live = func() bool { return live }
	if _, ok := readWorkspaceOrigin(t.Context(), b, func() { live = false }); ok {
		t.Fatal("ended lease returned commit")
	}
}

func TestSessionOriginBoundsOutputAndUsesCleanEnvironment(t *testing.T) {
	dir := t.TempDir()
	binding := originBinding(t, dir)
	bin := t.TempDir()
	git := filepath.Join(bin, "git")
	t.Setenv("PATH", bin)
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-do-not-export")
	good := strings.Repeat("a", 40)
	script := "#!/bin/sh\nif [ -n \"${BLERG_RUNNER_SESSION_TOKEN:-}\" ] || [ \"$*\" != '-c core.hooksPath=/dev/null -c core.fsmonitor=false -c safe.directory=" + dir + " rev-parse --verify HEAD' ]; then printf bad; else printf " + good + "; fi\n"
	if err := os.WriteFile(git, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if got, ok := readWorkspaceOrigin(t.Context(), binding, nil); !ok || got != good {
		t.Fatal("fixed arguments or credential-free environment not preserved")
	}
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf "+strings.Repeat("a", 101)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, ok := readWorkspaceOrigin(t.Context(), binding, nil); ok {
		t.Fatal("oversized git reply accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, ok := readWorkspaceOrigin(ctx, binding, nil); ok {
		t.Fatal("canceled origin read succeeded")
	}
}

func TestSessionOriginDeadlineTerminatesGit(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, ok := readWorkspaceOrigin(ctx, originBinding(t, dir), nil); ok {
		t.Fatal("timed out Git returned origin")
	}
	if time.Since(start) > time.Second {
		t.Fatal("Git outlived canceled origin read")
	}
}
