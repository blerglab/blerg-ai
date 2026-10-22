package daemon

import (
	"bytes"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

const runningState = `{"Status":"running","Running":true,"Paused":false,"ExitCode":0}`

func noSuchContainer() error {
	return &exec.ExitError{Stderr: []byte("Error: No such container: x")}
}

// I1: the person switches their docker CLI to another context (another engine). The container is
// still running on the one it was started on, and every look goes there: the session goes on.
func TestADockerContextSwitchDoesNotEndASession(t *testing.T) {
	const container = "blerg-sandbox-ctx-switch"
	pinSandboxContext(container, "started-here")
	t.Cleanup(func() { forgetSandboxDeath(container) })
	orig := dockerOutput
	var calls [][]string
	dockerOutput = func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		if len(args) > 2 && args[0] == "--context" && args[1] == "started-here" {
			return []byte(runningState), nil
		}
		return nil, noSuchContainer() // the CLI's current context: another engine
	}
	t.Cleanup(func() { dockerOutput = orig })
	if exit, dead := sandboxContainerExit(container); dead {
		t.Fatalf("a running container on its own context was taken for dead: %+v (calls %v)", exit, calls)
	}
	if len(calls) == 0 || calls[0][0] != "--context" {
		t.Fatalf("the look did not name the container's context: %v", calls)
	}
}

// The context a container starts on is pinned at its start, and forgotten when it is removed.
func TestSandboxContainerIsPinnedToItsDockerContext(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	origCtx, origRun := currentDockerContext, dockerRun
	currentDockerContext = func() string { return "ctx-a" }
	dockerRun = func(args ...string) error {
		if slices.Contains(args, "run") {
			if !slices.Contains(args, "blerg.runner.docker-context=ctx-a") || !slices.Equal(args[:2], []string{"--context", "ctx-a"}) {
				t.Errorf("creation did not persist and select its context: %v", args)
			}
		}
		return nil
	}
	t.Cleanup(func() { currentDockerContext, dockerRun = origCtx, origRun })
	const id = "pin-1"
	container, err := startSandboxContainer(id, sandboxTestClone(t, id), nil, sandboxCredential{}, SandboxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pinnedDockerArgs(container, "inspect"); strings.Join(got, " ") != "--context ctx-a inspect" {
		t.Fatalf("pinned args = %v", got)
	}
	sandboxDeathReported.Store(container, struct{}{})
	removeSandboxContainer(id)
	if got := pinnedDockerArgs(container, "inspect"); len(got) != 1 {
		t.Fatalf("the pin outlived the container: %v", got)
	}
	// M4: and so does nothing else the daemon kept about it.
	if _, ok := sandboxDeathReported.Load(container); ok {
		t.Fatal("the removed container's death record was kept")
	}
}

// I1: one "No such container" is not enough; it is "gone" only when a second look, a moment later,
// says it again. A container that stopped (exited) is final at once.
func TestATransientNoSuchContainerIsNotGone(t *testing.T) {
	orig := dockerOutput
	t.Cleanup(func() { dockerOutput = orig })
	run := func(answers ...string) (agent.SandboxExit, bool, int) {
		n := 0
		dockerOutput = func(...string) ([]byte, error) {
			a := answers[min(n, len(answers)-1)]
			n++
			if a == "gone" {
				return nil, noSuchContainer()
			}
			return []byte(a), nil
		}
		exit, dead := sandboxContainerExit("blerg-sandbox-transient")
		return exit, dead, n
	}
	if exit, dead, n := run("gone", runningState); dead || n != 2 {
		t.Fatalf("one transient gone: dead=%v %+v after %d looks", dead, exit, n)
	}
	if exit, dead, n := run("gone", "gone"); !dead || !exit.Gone || n != 2 {
		t.Fatalf("gone twice: dead=%v %+v after %d looks", dead, exit, n)
	}
	exited := `{"Status":"exited","Running":false,"ExitCode":137,"FinishedAt":"2026-10-21T19:57:53Z"}`
	if exit, dead, n := run(exited); !dead || exit.ExitCode != 137 || n != 1 {
		t.Fatalf("exited: dead=%v %+v after %d looks", dead, exit, n)
	}
}

// I2: a sandboxed terminal the reattach pass stops (no record of a clone of its own) is told once,
// with the accurate reason, and is not then sent into recovery (which would tell it again, less
// accurately, and could only be refused there).
func TestAStoppedLegacySandboxIsNotRecovered(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	dockerDir, _ := fakeDocker(t)
	t.Setenv("PATH", dockerDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", filepath.Join(t.TempDir(), "docker.log"))
	t.Setenv("FAKE_DOCKER_EXEC_NOOP", "1")
	stubCloneProbe(t, false, false)
	var started []string
	oldStart, oldSource := startTerminalSandbox, containerWorkspaceSource
	startTerminalSandbox = func(id, path string, _, _ []string, _ SandboxOptions) error {
		started = append(started, id)
		return errors.New("not in this test")
	}
	containerWorkspaceSource = func(string) (string, bool) { return "", false }
	origOut, origProbe := dockerOutput, dockerProbe
	const id = "legacy-term-1"
	dockerOutput = func(args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "ps" && (len(args) < 2 || args[1] != "-a") {
			return []byte(dockerContainerName(id) + "\t" + sandboxOwner() + "\n"), nil
		}
		return nil, nil
	}
	dockerProbe = func(time.Duration, ...string) (bool, error) { return false, nil } // a tmux server inside
	t.Cleanup(func() {
		startTerminalSandbox, containerWorkspaceSource = oldStart, oldSource
		dockerOutput, dockerProbe = origOut, origProbe
		unregisterSandbox(id)
	})
	sender := newRecordingSender(16)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: t.TempDir()})
	if err := mgr.writeRecord(SessionRecord{Version: recordSchemaVersion, SessionID: id, ProjectPath: "/home/someone/repos/app", Sandbox: true}); err != nil {
		t.Fatal(err)
	}
	mgr.reattachPass(listSessionRecords(mgr.reposRoot()), nil)

	var told []string
	for _, m := range sender.messages() {
		if strings.Contains(m, `"session_state_changed"`) && strings.Contains(m, `"`+id+`"`) {
			told = append(told, m)
		}
	}
	if len(told) != 1 || !strings.Contains(told[0], "this daemon has no record of a clone of its own") {
		t.Fatalf("the stopped session was told %d times: %v", len(told), told)
	}
	if len(started) != 0 {
		t.Fatalf("recovery tried to start it again: %v", started)
	}
}

// M1: in the sweep, an unlabeled agent container from before the owner label counts as this
// daemon's when it has only a session record (no clone record, no sandbox home), as DAEMON.md says.
func TestSweepCountsASessionRecord(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	const id = "recorded-only-1"
	origOut, origRun, origProbe := dockerOutput, dockerRun, dockerProbe
	var removed []string
	dockerOutput = func(args ...string) ([]byte, error) {
		if args[0] == "ps" && args[1] == "-a" {
			return nil, nil
		}
		return []byte(dockerContainerName(id) + "\t\n"), nil
	}
	dockerRun = func(args ...string) error {
		if args[0] == "rm" {
			removed = append(removed, args[len(args)-1])
		}
		return nil
	}
	dockerProbe = func(time.Duration, ...string) (bool, error) { return false, errNotInImage }
	t.Cleanup(func() { dockerOutput, dockerRun, dockerProbe = origOut, origRun, origProbe })
	sweepOrphanSandboxContainers(nil, nil)
	if len(removed) != 0 {
		t.Fatalf("an unrecorded container was removed: %v", removed)
	}
	sweepOrphanSandboxContainers(nil, func(s string) bool { return s == id })
	if !slices.Equal(removed, []string{dockerContainerName(id)}) {
		t.Fatalf("removed %v, want the recorded session's container", removed)
	}
}

// M2: a container that is not this daemon's is logged once per daemon run, not at every reconnect.
func TestAForeignContainerIsLoggedOnce(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	name := sandboxContainerPrefix + "foreign-log-" + ccUUID()[:8]
	orig := dockerOutput
	dockerOutput = func(...string) ([]byte, error) { return []byte(name + "\tanother-daemon\n"), nil }
	t.Cleanup(func() { dockerOutput = orig })
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for range 3 {
		if got := listOwnSandboxContainers(nil); len(got) != 0 {
			t.Fatalf("listed %v", got)
		}
	}
	if n := strings.Count(buf.String(), name); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, buf.String())
	}
}

// M3: the same state directory reached through a symlink is the same owner.
func TestSandboxOwnerResolvesSymlinks(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DaemonStateDirEnv, target)
	own := sandboxOwner()
	t.Setenv(DaemonStateDirEnv, link)
	if got := sandboxOwner(); got != own || own == "" {
		t.Fatalf("owner through the symlink %q, through the real path %q", got, own)
	}
}

// M5: the periodic look lists this daemon's containers once per interval for every session, and
// inspects only a container the listing does not show running, paused or restarting.
func TestThePeriodicLookIsOneListingForAllSessions(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	fastSandboxWatch(t, time.Hour)
	sandboxSnapMu.Lock()
	sandboxSnaps = map[string]*sandboxSnapshot{}
	sandboxSnapMu.Unlock()
	orig := dockerOutput
	var lists int
	dockerOutput = func(args ...string) ([]byte, error) {
		if args[0] != "ps" || !slices.Contains(args, "label="+sandboxOwnerLabel+"="+sandboxOwner()) {
			t.Fatalf("docker %v", args)
		}
		lists++
		return []byte("blerg-sandbox-a\trunning\nblerg-sandbox-b\tpaused\nblerg-sandbox-c\texited\n"), nil
	}
	t.Cleanup(func() { dockerOutput = orig })
	for _, c := range []string{"blerg-sandbox-a", "blerg-sandbox-b"} {
		if !sandboxLooksAlive(c) {
			t.Fatalf("%s not taken as alive", c)
		}
	}
	for _, c := range []string{"blerg-sandbox-c", "blerg-sandbox-missing"} {
		if sandboxLooksAlive(c) {
			t.Fatalf("%s taken as alive: it must be inspected", c)
		}
	}
	if lists != 1 {
		t.Fatalf("%d listings for four looks, want 1", lists)
	}
	// A Docker that cannot answer the listing knows nothing dead.
	sandboxSnapMu.Lock()
	sandboxSnaps = map[string]*sandboxSnapshot{}
	sandboxSnapMu.Unlock()
	dockerOutput = func(...string) ([]byte, error) { return nil, errors.New("cannot connect") }
	if !sandboxLooksAlive("blerg-sandbox-c") {
		t.Fatal("an unreachable Docker made a container a candidate")
	}
}
