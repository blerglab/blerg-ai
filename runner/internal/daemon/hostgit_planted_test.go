package daemon

// Card #136: every host git call site, against the configuration attacks of the commit-lookup
// review (#131), planted in each repository the call reads: nothing they name runs, and no file
// they name is read.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit/hostgittest"
)

func plantedWorkspace(t *testing.T) (string, workspaceFileBinding, string) {
	t.Helper()
	dir, b := commitFixture(t)
	hostgittest.Attributes(t, dir)
	sha := commitFile(t, dir, "a.txt", "a\n", "A")
	return dir, b, sha
}

func TestSessionOriginIgnoresPlantedConfiguration(t *testing.T) {
	dir, b, sha := plantedWorkspace(t)
	probe := hostgittest.Plant(t, filepath.Join(dir, ".git"))
	if got, ok := readWorkspaceOrigin(t.Context(), b, nil); !ok || got != sha {
		t.Fatalf("origin: %q %t", got, ok)
	}
	probe.Check(t)
}

func TestCommitLookupIgnoresPlantedConfiguration(t *testing.T) {
	dir, b, sha := plantedWorkspace(t)
	probe := hostgittest.Plant(t, filepath.Join(dir, ".git"))
	if res := summary(t, b, sha); !res.OK {
		t.Fatalf("summary: %+v", res)
	}
	if res := readWorkspaceCommit(t.Context(), "s", b, sha, true); !res.OK {
		t.Fatalf("patch: %+v", res)
	}
	probe.Check(t)
}

// The sandbox clone's setup (rev-parse, clone --shared, checkout, for-each-ref, update-ref) in the
// person's repository, the turn-end fetch and the release, with that repository and the clone
// both planted.
func TestSandboxCloneGitIgnoresPlantedConfiguration(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := filepath.Join(t.TempDir(), "proj")
	initChildRepo(t, repo)
	hostgittest.Attributes(t, repo)
	childRepoGit(t, repo, "add", ".gitattributes")
	childRepoGit(t, repo, "commit", "-qm", "attributes")
	repoProbe := hostgittest.Plant(t, filepath.Join(repo, ".git"))
	const id = "planted-sandbox"
	clone, err := createSandboxClone(repo, id, "")
	if err != nil {
		t.Fatal(err)
	}
	childCommit(t, clone, "work.txt")
	cloneProbe := hostgittest.Plant(t, filepath.Join(clone, ".git"))
	if commits, err := syncSandboxBranch(clone, id, ""); err != nil || !commits {
		t.Fatalf("sync: %v %v", commits, err)
	}
	releaseSandboxClone(clone, id, false, true)
	repoProbe.Check(t)
	cloneProbe.Check(t)
	if got := childRepoGit(t, repo, "-c", "log.showSignature=false", "-c", "log.mailmap=false", "log", "-1", "--format=%s", "wip/"+id); got != "child work.txt" {
		t.Fatalf("the clone's commit did not arrive: %q", got)
	}
}

// A clone from a remote (here a local path) runs nothing the remote's configuration names and
// clones no submodule.
func TestRemoteCloneIgnoresTheRemotesConfiguration(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	initChildRepo(t, src)
	sub := filepath.Join(t.TempDir(), "sub")
	initChildRepo(t, sub)
	childRepoGit(t, src, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	childRepoGit(t, src, "commit", "-qm", "sub")
	probe := hostgittest.Plant(t, filepath.Join(src, ".git"))
	dest := filepath.Join(t.TempDir(), "dest")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := gitClone(ctx, time.Minute, src, dest, nil); err != nil {
		t.Fatal(err)
	}
	probe.Check(t)
	if entries, _ := os.ReadDir(filepath.Join(dest, "sub")); len(entries) != 0 {
		t.Fatal("a submodule was cloned")
	}
}
