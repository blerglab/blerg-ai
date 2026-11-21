package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// runSettle runs the stop-time script against clone on the host (it is written for the container's
// /workspace) and returns its exit code.
func runSettle(t *testing.T, clone, id string) int {
	t.Helper()
	script := strings.Replace(sandboxSettleScript(id), "cd /workspace", "cd '"+clone+"'", 1)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(interface{ ExitCode() int }); ok {
			return exit.ExitCode()
		}
		t.Fatal(err)
	}
	return 0
}

// M5: the stop-time check does not trust the session's own Git configuration. Untracked work that
// status.showUntrackedFiles=no, .git/info/exclude or core.excludesFile would hide is committed, not
// left behind in a clone that is then removed. Files the work tree's .gitignore excludes stay out.
func TestSettleSeesWorkTheSessionHid(t *testing.T) {
	for name, hide := range map[string]func(t *testing.T, clone string){
		"showUntrackedFiles=no": func(t *testing.T, clone string) { childRepoGit(t, clone, "config", "status.showUntrackedFiles", "no") },
		"info/exclude": func(t *testing.T, clone string) {
			if err := os.WriteFile(filepath.Join(clone, ".git", "info", "exclude"), []byte("*\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"core.excludesFile": func(t *testing.T, clone string) {
			f := filepath.Join(t.TempDir(), "ignore")
			if err := os.WriteFile(f, []byte("*.txt\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			childRepoGit(t, clone, "config", "core.excludesFile", f)
		},
	} {
		t.Run(name, func(t *testing.T) {
			id := "hide-" + strings.NewReplacer("=", "-", "/", "-", ".", "-").Replace(name)
			_, clone := cloneFixture(t, id)
			if err := os.MkdirAll(filepath.Join(clone, ".git", "info"), 0o755); err != nil {
				t.Fatal(err)
			}
			hide(t, clone)
			if err := os.WriteFile(filepath.Join(clone, "draft.txt"), []byte("draft\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(clone, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(clone, "build"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(clone, "build", "out.bin"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if code := runSettle(t, clone, id); code != 0 {
				t.Fatalf("settle exit %d, want 0 after committing the work", code)
			}
			files := childRepoGit(t, clone, "ls-tree", "-r", "--name-only", "wip/"+id)
			if !strings.Contains(files, "draft.txt") {
				t.Fatalf("the hidden draft was not committed: %s", files)
			}
			if strings.Contains(files, "build/out.bin") {
				t.Fatalf("a file the work tree's .gitignore excludes was committed: %s", files)
			}
		})
	}
}

// M4: an empty folder inside the person's checkout is never made a repository of its own (it would
// be embedded in theirs): the start is refused and nothing is created there.
func TestEmptyFolderInsideACheckoutIsRefused(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	repo := filepath.Join(t.TempDir(), "app")
	initChildRepo(t, repo)
	empty := filepath.Join(repo, "emptydir")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := createSandboxClone(empty, "s-embedded", "")
	if err == nil || !strings.Contains(err.Error(), "inside the Git repository app") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(empty, ".git")); err == nil {
		t.Fatal("a repository was made inside the person's checkout")
	}

	// R-M1: a repos root that itself sits inside a repository (a home directory kept in Git) is
	// not a checkout: a New folder directly under it is made a repository of its own as usual,
	// while an empty folder inside a checkout under that root is still refused.
	home := filepath.Join(t.TempDir(), "home")
	initChildRepo(t, home)
	root := filepath.Join(home, "repositories")
	newFolder := filepath.Join(root, "newproj")
	if err := os.MkdirAll(newFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := createSandboxClone(newFolder, "s-dotfiles", root); err != nil {
		t.Fatalf("a New folder under a repos root inside a repository was refused: %v", err)
	}
	app := filepath.Join(root, "app")
	initChildRepo(t, app)
	inner := filepath.Join(app, "emptydir")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := createSandboxClone(inner, "s-dotfiles-inner", root); err == nil || !strings.Contains(err.Error(), "inside the Git repository app") {
		t.Fatalf("an empty folder inside a checkout under the root: err = %v", err)
	}
}

// The sandbox `docker run` never pulls in-band, and a `docker run` carrying a credential in its
// environment is bounded like every other docker call.
func TestSandboxDockerRunNeverPullsAndIsBounded(t *testing.T) {
	args := sandboxRunArgs("c", "/p", "", nil, sandboxRunExtras{})
	if strings.Join(args[:5], " ") != "run -d --name c --pull=never" {
		t.Fatalf("run args %v", args[:5])
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	old := dockerCallTimeout
	dockerCallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { dockerCallTimeout = old })
	start := time.Now()
	if err := dockerRunEnv([]string{"SOME_CREDENTIAL=x"}, "run", "-d", "c"); err == nil {
		t.Fatal("a hung docker run with a credential env succeeded")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("docker run with a credential env took %s: unbounded", took)
	}
}

// M2: when the container could not be removed, the clone is not fetched unpaused: it is kept, and
// its branch does not reach the repository.
func TestFinishSandboxDoesNotFetchWhenTheContainerRemains(t *testing.T) {
	repo, clone := cloneFixture(t, "stuck-1")
	dockerDir, _ := fakeDocker(t)
	t.Setenv("PATH", dockerDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", filepath.Join(t.TempDir(), "docker.log"))
	stubCloneProbe(t, true, true)
	old := sandboxContainerGone
	sandboxContainerGone = func(string) bool { return false }
	t.Cleanup(func() { sandboxContainerGone = old })
	childCommit(t, clone, "work.txt")
	finishSandbox("stuck-1")
	if _, err := childRepoGitErr(repo, "rev-parse", "--verify", "refs/heads/wip/stuck-1"); err == nil {
		t.Fatal("the clone was fetched while its container could still be running")
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatal("the clone was removed")
	}
}

// M6: with wip/<id> checked out in the person's repository, a sync neither fails nor moves their
// branch under their working tree: the commits go to wip/<id>.incoming and the sync says so.
func TestSyncIntoACheckedOutWipBranch(t *testing.T) {
	repo, clone := cloneFixture(t, "co-1")
	dockerDir, _ := fakeDocker(t)
	t.Setenv("PATH", dockerDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", filepath.Join(t.TempDir(), "docker.log"))
	childCommit(t, clone, "one.txt")
	if _, err := syncSandboxBranch(clone, "co-1", ""); err != nil {
		t.Fatal(err)
	}
	childRepoGit(t, repo, "checkout", "-q", "wip/co-1")
	first := childRepoGit(t, repo, "rev-parse", "HEAD")
	childCommit(t, clone, "two.txt")
	commits, err := syncSandboxBranch(clone, "co-1", "")
	var checkedOut *sandboxCheckedOutError
	if !commits || err == nil || !strings.Contains(err.Error(), "wip/co-1.incoming") {
		t.Fatalf("commits=%v err=%v", commits, err)
	}
	if !errors.As(err, &checkedOut) {
		t.Fatalf("not the checked-out notice: %v", err)
	}
	if got := childRepoGit(t, repo, "rev-parse", "HEAD"); got != first {
		t.Fatal("the person's checked-out branch moved")
	}
	if got := childRepoGit(t, repo, "log", "-1", "--format=%s", "wip/co-1.incoming"); got != "child two.txt" {
		t.Fatalf("incoming: %q", got)
	}
	if out := childRepoGit(t, repo, "status", "--porcelain"); out != "" {
		t.Fatalf("the person's working tree changed: %s", out)
	}
}

// M1: a FIFO swapped over a clone file between the check and the open does not block the read
// (the session controls its clone while it runs), and one session cannot hold more than one of the
// daemon's shared origin-read slots.
func TestCloneReadsNeverBlockOnAFifo(t *testing.T) {
	_, clone := cloneFixture(t, "fifo-1")
	head := filepath.Join(clone, ".git", "HEAD")
	content, err := os.ReadFile(head)
	if err != nil {
		t.Fatal(err)
	}
	fifo, plain := head+".fifo", head+".plain"
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	swapped := make(chan struct{})
	go func() {
		defer close(swapped)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.WriteFile(plain, content, 0o644)
			_ = os.Rename(plain, head)
			_ = os.Link(fifo, head+".tmp")
			_ = os.Rename(head+".tmp", head)
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3000; i++ {
			readCloneHead(clone)
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		close(stop)
		<-swapped
		// Unblock the stuck reader so the test process can finish.
		if f, err := os.OpenFile(head, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
		t.Fatal("reading the clone's HEAD blocked on a FIFO")
	}
	close(stop)
	<-swapped

	// One session, one slot.
	release := make(chan struct{})
	info, _ := os.Stat(clone)
	binding := workspaceFileBinding{root: clone, info: info, live: func() bool { <-release; return true }}
	sender := &originSender{ch: make(chan protocol.SessionOriginResult, 4)}
	queueSessionOrigin(context.Background(), sender, protocol.ReadSessionOrigin{RequestID: "r1", SessionID: "fifo-1"}, binding)
	queueSessionOrigin(context.Background(), sender, protocol.ReadSessionOrigin{RequestID: "r2", SessionID: "fifo-1"}, binding)
	if n := len(sessionOriginReads); n != 1 {
		close(release)
		t.Fatalf("one session holds %d origin-read slots", n)
	}
	close(release)
	<-sender.ch
}

// M7: after a daemon restart nothing hosts a sandboxed agent session again; the sweep commits its
// work in its container, removes the container, and brings the branch back.
func TestRestartSweepBringsAnOrphansWorkBack(t *testing.T) {
	repo, clone := cloneFixture(t, "orphan-1")
	stubCloneProbe(t, true, true)
	childCommit(t, clone, "work.txt")
	var removed []string
	origOut, origRun := dockerOutput, dockerRun
	dockerOutput = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "ps" && args[len(args)-1] != "{{.Names}}" {
			return []byte(dockerContainerName("orphan-1") + "\n"), nil
		}
		return nil, nil // ps -a for the removed container: gone
	}
	dockerRun = func(args ...string) error {
		if args[0] == "rm" {
			removed = append(removed, args[len(args)-1])
		}
		return nil
	}
	t.Cleanup(func() { dockerOutput, dockerRun = origOut, origRun })
	sweepOrphanSandboxContainers(nil, nil)
	if len(removed) != 1 || removed[0] != dockerContainerName("orphan-1") {
		t.Fatalf("removed %v", removed)
	}
	if got := childRepoGit(t, repo, "log", "-1", "--format=%s", "wip/orphan-1"); got != "child work.txt" {
		t.Fatalf("the orphan's work did not come back: %q", got)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatal("the clean clone was kept")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}
