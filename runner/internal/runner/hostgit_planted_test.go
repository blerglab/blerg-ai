package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit/hostgittest"
)

// Card #136: the pod's own git (clone, wip checkout, push) runs nothing the origin's or the
// agent-written workspace's configuration names, and reads no file it names.
func TestPodGitIgnoresPlantedConfiguration(t *testing.T) {
	bare := makeBareRepo(t)
	originProbe := hostgittest.Plant(t, bare)
	cfg := Config{Home: t.TempDir(), Repo: "proj", SessionID: "planted", GitURL: bare}
	workDir, _, err := PrepareWorkspace(cfg)
	if err != nil {
		t.Fatal(err)
	}
	originProbe.Check(t)

	// The agent commits, then writes its workspace's configuration (hooks, drivers, files).
	if err := os.WriteFile(filepath.Join(workDir, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "new.txt"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "work"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	probe := hostgittest.Plant(t, filepath.Join(workDir, ".git"))
	hostgittest.Attributes(t, workDir)
	PushWip(workDir, "planted")
	probe.Check(t)
	// The push reached the planted origin: its receive-pack ran none of the origin's hooks or
	// configured commands either (the pod names its own serving command).
	originProbe.Check(t)
	if out := gitT(t, bare, "branch"); !strings.Contains(out, "wip/planted") {
		t.Fatalf("wip branch not pushed: %s", out)
	}
}

// The pod's git uses the clone URL's transport only: a remote the agent repoints at ext:: runs
// nothing.
func TestPodGitUsesOnlyTheCloneURLsTransport(t *testing.T) {
	bare := makeBareRepo(t)
	workDir, _, err := PrepareWorkspace(Config{Home: t.TempDir(), Repo: "proj", SessionID: "ext", GitURL: bare})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	hostgittest.Config(t, filepath.Join(workDir, ".git"), "remote.origin.url", "ext::sh -c touch% "+marker)
	hostgittest.Config(t, filepath.Join(workDir, ".git"), "protocol.ext.allow", "always")
	commit := exec.Command("git", "-c", "core.hooksPath=/dev/null", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "work")
	commit.Dir = workDir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	PushWip(workDir, "ext")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("an ext:: remote ran")
	}
}

// #136 re-review, nit 2: a local origin whose hardened serving command cannot be built is refused,
// never served by the plain git-<service>.
func TestPodRefusesWithoutAHardenedServingCommand(t *testing.T) {
	bare := makeBareRepo(t)
	setPodRemote(bare)
	t.Cleanup(func() { setPodRemote("") })
	t.Setenv("PATH", t.TempDir()) // no git: the hardened command cannot be built
	if flag, err := servingFlag("receive-pack"); err == nil {
		t.Fatalf("served without a hardened command: %v", flag)
	}
	if _, err := gitServed(t.TempDir(), "upload-pack", "ls-remote", "--heads", bare); err == nil {
		t.Fatal("ls-remote ran without a hardened serving command")
	}
}
