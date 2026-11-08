package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestRecordedWorkBranchCloneAndRecovery(t *testing.T) {
	t.Setenv("BLERG_RUNNER_DAEMON_STATE_DIR", t.TempDir())
	const id = "branch-fixture"
	const branch = "dev/person/button-fix"
	msg := protocol.SpawnSession{SessionID: id, Sandbox: true, WorkBranch: branch}
	if err := recordWorkBranch(msg); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	initChildRepo(t, repo)
	clone, err := createSandboxClone(repo, id, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := childRepoGit(t, clone, "branch", "--show-current"); got != branch {
		t.Fatalf("branch = %q", got)
	}
	if got := sandboxBranch(id); got != branch {
		t.Fatalf("recovery branch = %q", got)
	}
	msg.WorkBranch = "changed/name"
	if err := recordWorkBranch(msg); err == nil {
		t.Fatal("renamed a recorded session branch")
	}
	if err := os.WriteFile(filepath.Join(clone, "work.txt"), []byte("work"), 0600); err != nil {
		t.Fatal(err)
	}
	childRepoGit(t, clone, "add", ".")
	childRepoGit(t, clone, "commit", "-qm", "Work")
	// A direct trusted fetch verifies the recorded branch's name at delivery.
	childRepoGit(t, repo, "fetch", clone, "refs/heads/"+branch+":refs/heads/"+branch)
	if got := childRepoGit(t, repo, "rev-parse", "refs/heads/"+branch); got != childRepoGit(t, clone, "rev-parse", "HEAD") {
		t.Fatal("delivery lost the branch")
	}
	if got := sandboxBranch("legacy"); got != "wip/legacy" {
		t.Fatalf("legacy = %q", got)
	}
}

func TestWorkBranchShellQuoting(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	const branch = "dev/$(echo-injected)'quote"
	if err := recordWorkBranch(protocol.SpawnSession{SessionID: "quote-test", Sandbox: true, WorkBranch: branch}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "b="+shellBranch("quote-test")+`; printf '%s' "$b"`)
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "refs/heads/"+branch {
		t.Fatalf("branch assignment = %q, %v", out, err)
	}
}

// Workers receive the branch in authenticated state, without the daemon's
// branch-record directory mounted. Nested names must also survive settlement.
func TestWorkspaceWorkerBranchWithoutDaemonRecord(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	initChildRepo(t, repo)
	clone, record := filepath.Join(root, "clone"), filepath.Join(root, "source.json")
	const branch = "dev/person/nested/button-fix"
	if _, err := createSandboxCloneAt(t.Context(), repo, "worker-session", root, clone, record, false, branch); err != nil {
		t.Fatal(err)
	}
	if got := childRepoGit(t, clone, "branch", "--show-current"); got != branch {
		t.Fatalf("worker branch=%q", got)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var source sandboxClone
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	refs, err := workspaceCloneRefs(t.Context(), clone)
	if err != nil {
		t.Fatal(err)
	}
	member := workspacePrivateMember{Clone: clone, Source: source, InitialRefsDigest: workspaceRefsDigest(refs, "refs/heads/"+branch)}
	if err := os.WriteFile(filepath.Join(clone, "uncommitted.txt"), []byte("worker work"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := settleWorkspaceMember(t.Context(), &member, "worker-session", branch); err != nil {
		t.Fatal(err)
	}
	if got := childRepoGit(t, clone, "show", "HEAD:uncommitted.txt"); got != "worker work" {
		t.Fatalf("settled file=%q", got)
	}
	if got := childRepoGit(t, clone, "branch", "--show-current"); got != branch {
		t.Fatalf("settlement renamed branch to %q", got)
	}
}

func TestWorkBranchConcurrentRecord(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	const branch = "dev/person/concurrent"
	msg := protocol.SpawnSession{SessionID: "concurrent-branch", Sandbox: true, WorkBranch: branch}
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		group.Go(func() { failures <- recordWorkBranch(msg) })
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := sandboxBranch(msg.SessionID); got != branch {
		t.Fatalf("record=%q", got)
	}
}

func TestReadableBranchSandboxDeliveryAndCleanup(t *testing.T) {
	dockerDir, dockerLog := fakeDocker(t)
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", dockerLog)
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			t.Setenv(DaemonStateDirEnv, t.TempDir())
			const id = "readable-branch"
			const branch = "wip/ticket-data-7d67a123"
			if err := recordWorkBranch(protocol.SpawnSession{SessionID: id, Sandbox: true, WorkBranch: branch}); err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(t.TempDir(), "repo")
			initChildRepo(t, repo)
			clone, err := createSandboxClone(repo, id, "")
			if err != nil {
				t.Fatal(err)
			}
			if committed {
				childCommit(t, clone, "work.txt")
			}
			releaseSandboxClone(clone, id, true, true)
			if _, err := os.Stat(clone); !os.IsNotExist(err) {
				t.Fatal("clean clone retained", err)
			}
			if committed {
				if got := childRepoGit(t, repo, "show", branch+":work.txt"); got != "work.txt" {
					t.Fatal("lost delivered work", got)
				}
			} else if _, err := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/"+branch); err == nil {
				t.Fatal("empty session branch retained")
			}
			if _, err := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/"+id); err == nil {
				t.Fatal("reconstructed branch from session ID")
			}
		})
	}
}
