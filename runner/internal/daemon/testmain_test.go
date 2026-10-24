package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

// The driver tests written before mid-turn steering drive the per-turn engine with a stub `claude` that
// ignores stdin, so the package runs with the kill switch on. The steering tests build their driver with
// newSteeringDriver, which turns the streaming engine on for that one driver.
func TestMain(m *testing.M) {
	if workspaceRound2CrashMain(os.Args[1:]) {
		os.Exit(0)
	}
	if workspaceCrashTestMain(os.Args[1:]) {
		os.Exit(0)
	}
	if WorkspaceWorkerMain(os.Args[1:]) {
		os.Exit(0)
	}
	_ = os.Setenv("BLERG_CLAUDE_STEERING", "0")
	// No test starts a CLI to read its --help: the fake would take it for a session. Tests of the
	// flag probe replace this themselves.
	ccHelpText = func(context.Context, sandboxExec, string, []string) ([]byte, error) {
		return nil, errors.New("no --help probe in tests")
	}
	// The marker key stays in memory: no test writes the daemon's real state directory.
	markerKeyDir = func() string { return "" }
	// Nor its clones, sandbox homes or tokens: a test that does not choose a state directory gets
	// a scratch one, never the person's (whose records tie live sandbox containers to a daemon).
	// A helper process the tests start from this binary (a fake engine) shares its parent's.
	stateDir, owned := os.Getenv(testStateDirEnv), false
	if stateDir == "" {
		// Named, not made: whatever needs it creates it (as the daemon does its own), so a helper
		// process started with an environment of its own leaves nothing behind.
		stateDir = filepath.Join(os.TempDir(), "blerg-daemon-test-state-"+strconv.Itoa(os.Getpid())+"-"+ccUUID()[:8])
		owned = true
		_ = os.Setenv(testStateDirEnv, stateDir)
	}
	_ = os.Setenv(DaemonStateDirEnv, stateDir)
	// The periodic look at a sandbox container would add `docker inspect` calls to every fake
	// docker log; the tests of that watch turn it on themselves.
	sandboxWatchInterval = 0
	// A second "gone" look comes at once, and no fake docker log gains a `docker context show`; the
	// real-Docker tests put the real context lookup back (realDockerContext).
	sandboxGoneConfirm = 10 * time.Millisecond
	realDockerContext = currentDockerContext
	currentDockerContext = func() string { return "" }
	// Every sandbox container a test starts carries this run's own label, and only containers that
	// carry it are removed at the end: never by name or name prefix, never a live session's.
	label := testContainerLabel()
	sandboxTestLabels = []string{label}
	code := testsupport.Run(m)
	if owned {
		removeTestContainers(label)
		_ = os.RemoveAll(stateDir)
	}
	os.Exit(code)
}

// realDockerContext is the daemon's own currentDockerContext, for the tests that use real Docker.
var realDockerContext func() string

// testStateDirEnv carries the test run's scratch state directory to the helper processes it starts.
const testStateDirEnv = "BLERG_DAEMON_TEST_STATE_DIR"

// testContainerLabel is a label of this test run's own (key=value).
func testContainerLabel() string {
	return "blerg-test-run=" + ccUUID()
}

// removeTestContainers removes every container, running or not, that carries label, and nothing
// else. Without Docker it does nothing.
func removeTestContainers(label string) {
	if !strings.HasPrefix(label, "blerg-test") {
		return // never a filter that is not a test label
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label="+label).Output()
	if err != nil {
		return
	}
	for _, id := range strings.Fields(string(out)) {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
	}
}
