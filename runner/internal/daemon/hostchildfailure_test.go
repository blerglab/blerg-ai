//go:build linux

package daemon

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
)

func TestHostChildPopulatedRepositoryRetry(t *testing.T) {
	_, parent, repo := hostChildSelectionFixture(t)
	workspaceFixture(t, repo, ".claude/settings.json", "{}\n")
	childRepoGit(t, repo, "add", ".claude/settings.json")
	childRepoGit(t, repo, "commit", "-qm", "add settings fixture")
	for i := 0; i < 30; i++ {
		childRepoGit(t, repo, "worktree", "add", "--detach", filepath.Join(repo, ".claude", "worktrees", fmt.Sprintf("existing-%d", i)), "HEAD")
	}
	hooks := filepath.Join(repo, ".githooks")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	childRepoGit(t, repo, "config", "core.hooksPath", hooks)
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte("parent changes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := childRepoGit(t, repo, "status", "--porcelain=v1")
	record, _ := workspaceRecord("child")
	w := &LocalWorkspace{record: record, state: workspacePrivateManifest{Host: true, Session: "child", Launch: parent.Launch}}
	lock := filepath.Join(repo, ".git", "refs", "heads", "wip", "child.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := w.allocateHostChild(t.Context(), parent)
	if err == nil {
		t.Fatal("expected checkout failure")
	}
	if !strings.Contains(err.Error(), "lock") {
		t.Errorf("failure lacks Git cause: %v", err)
	}
	if len(w.state.HostChildren) != 0 {
		t.Errorf("failed allocation retained: %d", len(w.state.HostChildren))
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	pins, err := w.allocateHostChild(t.Context(), parent)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	pins.Close()
	if got := childRepoGit(t, repo, "status", "--porcelain=v1"); got != before {
		t.Fatal("parent changed")
	}
}

func TestHostChildGitDiagnosticsPrivacy(t *testing.T) {
	_, parent, repo := hostChildSelectionFixture(t)
	const canary = "private-canary-credential"
	branch := "refs/heads/" + canary
	childRepoGit(t, repo, "update-ref", branch, "HEAD")
	lock := filepath.Join(repo, ".git", "refs", "heads", canary+".lock")
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { failurelog.Default = old })
	_, err := hostChildGit(t.Context(), repo, parent.Sources[0].RootIdentity, "update-ref", branch, "HEAD")
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("missing cause: %v", err)
	}
	combined := logs.String() + err.Error()
	for _, secret := range []string{canary, repo, "\x1b"} {
		if strings.Contains(combined, secret) {
			t.Fatal("diagnostics disclosed untrusted data")
		}
	}
	if !strings.Contains(logs.String(), `"git_stderr":"Git lock file already exists"`) {
		t.Fatal("safe stderr cause absent from logs")
	}
	for _, raw := range []string{"fatal: unknown private-canary-credential\nforged", "fatal: cannot lock ref 'https://user:private-canary-credential@example.com/repo'", "fatal: Permission denied private-canary-credential\x1b"} {
		if strings.Contains(hostChildGitReason(raw), canary) {
			t.Fatal("stderr canary escaped sanitizer")
		}
	}
}

func TestHostChildPartialCheckoutRollback(t *testing.T) {
	_, parent, repo := hostChildSelectionFixture(t)
	wrapper := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
worktree=no
add=no
for arg do
 if [ "$arg" = worktree ]; then worktree=yes; fi
 if [ "$worktree" = yes ] && [ "$arg" = add ]; then add=yes; fi
done
/usr/bin/git "$@"
result=$?
if [ "$result" = 0 ] && [ "$add" = yes ]; then
 printf '%s\n' "fatal: unable to checkout (private-canary-credential)" >&2
 exit 128
fi
exit "$result"
`
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	restore := hostgit.SetBinaryForTest(wrapper)
	record, _ := workspaceRecord("child")
	w := &LocalWorkspace{record: record, state: workspacePrivateManifest{Host: true, Session: "child", Launch: parent.Launch}}
	_, err := w.allocateHostChild(t.Context(), parent)
	restore()
	if err == nil || !strings.Contains(err.Error(), "Git checkout failed") {
		t.Fatalf("missing checkout cause: %v", err)
	}
	if len(w.state.HostChildren) != 0 {
		t.Fatal("partial allocation retained")
	}
	if _, err := os.Stat(filepath.Join(DaemonStateDir(), "host-children", "child")); !os.IsNotExist(err) {
		t.Fatal("namespace retained", err)
	}
	if _, err := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/child"); err == nil {
		t.Fatal("created branch retained")
	}
	saved, err := readPrivateWorkspace(record)
	if err != nil || len(saved.HostChildren) != 0 {
		t.Fatal("journal retained allocation", err)
	}
	pins, err := w.allocateHostChild(t.Context(), parent)
	if err != nil {
		t.Fatal("retry failed", err)
	}
	pins.Close()
}

func TestHostChildRollbackPreservesChangedCheckout(t *testing.T) {
	_, parent, _ := hostChildSelectionFixture(t)
	w := hostChildAllocationFixture(t, parent)
	a := w.state.HostChildren[0]
	workspaceFixture(t, a.Path, "keep.txt", "uncommitted child work")
	namespace := filepath.Join(DaemonStateDir(), "host-children", "child")
	identity, err := workspaceDirectoryIdentity(namespace)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.rollbackHostChild(t.Context(), namespace, identity, 0); err == nil {
		t.Fatal("changed checkout erased")
	}
	if raw, err := os.ReadFile(filepath.Join(a.Path, "keep.txt")); err != nil || string(raw) != "uncommitted child work" {
		t.Fatal("child bytes lost", err)
	}
}
