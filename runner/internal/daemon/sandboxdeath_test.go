package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Docker's own record decides: a stopped or removed container is dead, with its exit code, OOM
// flag and time; a running or paused one (the turn-end sync pauses it) is alive; a Docker that
// cannot answer is never taken for a dead container.
func TestSandboxContainerExitReadsDockersRecord(t *testing.T) {
	orig := dockerOutput
	t.Cleanup(func() { dockerOutput = orig })
	cases := []struct {
		name     string
		out      string
		err      error
		dead     bool
		reason   string
		wantExit agent.SandboxExit
	}{
		{name: "running", out: `{"Status":"running","Running":true,"Paused":false,"ExitCode":0}`},
		{name: "paused for the turn-end sync", out: `{"Status":"paused","Running":true,"Paused":true,"ExitCode":0}`},
		{name: "restarting", out: `{"Status":"restarting","Running":true,"Restarting":true}`},
		{name: "killed", out: `{"Status":"exited","Running":false,"Paused":false,"OOMKilled":false,"ExitCode":137,"Error":"","FinishedAt":"2026-10-21T19:57:53.134226Z"}`,
			dead: true, reason: "The sandbox container stopped (exit code 137, OOMKilled: no, at 2026-10-21T19:57:53Z); start a new session to continue the work",
			wantExit: agent.SandboxExit{Status: "exited", ExitCode: 137, FinishedAt: "2026-10-21T19:57:53.134226Z"}},
		{name: "out of memory", out: `{"Status":"exited","Running":false,"OOMKilled":true,"ExitCode":137,"Error":"","FinishedAt":"2026-10-21T19:57:53Z"}`,
			dead: true, reason: "The sandbox container stopped (exit code 137, OOMKilled: yes, at 2026-10-21T19:57:53Z); start a new session to continue the work",
			wantExit: agent.SandboxExit{Status: "exited", ExitCode: 137, OOMKilled: true, FinishedAt: "2026-10-21T19:57:53Z"}},
		{name: "docker error", out: `{"Status":"dead","Running":false,"ExitCode":-1,"Error":"cgroup gone","FinishedAt":"0001-01-01T00:00:00Z"}`,
			dead: true, reason: "The sandbox container stopped (exit code -1, OOMKilled: no, at an unknown time); Docker said: cgroup gone; start a new session to continue the work",
			wantExit: agent.SandboxExit{Status: "dead", ExitCode: -1, FinishedAt: "0001-01-01T00:00:00Z", Error: "cgroup gone"}},
		{name: "removed", err: &exec.ExitError{Stderr: []byte("Error: No such container: blerg-sandbox-x")},
			dead: true, reason: "The sandbox container is gone (it was removed, so Docker kept no exit details); start a new session to continue the work",
			wantExit: agent.SandboxExit{Gone: true}},
		{name: "docker unreachable", err: &exec.ExitError{Stderr: []byte("Cannot connect to the Docker daemon at unix:///var/run/docker.sock")}},
		{name: "no docker binary", err: errors.New("exec: docker: not found")},
		{name: "unparsable", out: "not json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dockerOutput = func(args ...string) ([]byte, error) {
				if strings.Join(args[:2], " ") != "inspect --type" || args[len(args)-1] != "blerg-sandbox-x" {
					t.Fatalf("docker %v", args)
				}
				return []byte(tc.out), tc.err
			}
			exit, dead := sandboxContainerExit("blerg-sandbox-x")
			if dead != tc.dead {
				t.Fatalf("dead = %v, want %v", dead, tc.dead)
			}
			if !dead {
				return
			}
			if exit != tc.wantExit {
				t.Fatalf("exit = %+v, want %+v", exit, tc.wantExit)
			}
			if got := sandboxExitReason(exit); got != tc.reason {
				t.Fatalf("reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

// stubSandboxExit makes every container the daemon asks about report as dead (true) or alive.
func stubSandboxExit(t *testing.T, dead *atomic.Bool, exit agent.SandboxExit) {
	t.Helper()
	orig := sandboxContainerExit
	sandboxContainerExit = func(string) (agent.SandboxExit, bool) {
		if dead.Load() {
			return exit, true
		}
		return agent.SandboxExit{}, false
	}
	t.Cleanup(func() { sandboxContainerExit = orig })
}

// fastSandboxWatch turns on the periodic look at a sandbox container for one test.
func fastSandboxWatch(t *testing.T, every time.Duration) {
	t.Helper()
	orig := sandboxWatchInterval
	sandboxWatchInterval = every
	t.Cleanup(func() { sandboxWatchInterval = orig })
}

// sandboxStopEvents are the runtime_stopped errors that carry a sandbox exit.
func (s *agentTestSender) sandboxStopEvents(t *testing.T) []agent.ErrorPayload {
	t.Helper()
	var out []agent.ErrorPayload
	for _, ev := range s.agentEvents() {
		if ev.Kind != "error" {
			continue
		}
		var p agent.ErrorPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.SandboxExit != nil {
			out = append(out, p)
		}
	}
	return out
}

func (s *agentTestSender) errorMessages(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, ev := range s.agentEvents() {
		if ev.Kind == "error" {
			var p agent.ErrorPayload
			_ = json.Unmarshal(ev.Payload, &p)
			out = append(out, p.Message)
		}
	}
	return out
}

// assertEndedForSandbox checks that a session ended once, as failed, with the container's reason,
// said once as a runtime_stopped error carrying Docker's record, and with no error status beside it.
func assertEndedForSandbox(t *testing.T, host *AgentHost, sender *agentTestSender, id string, exit agent.SandboxExit) {
	t.Helper()
	waitUntilFor(t, 20*time.Second, "the session ends", func() bool { return len(sender.sessionEnds()) > 0 })
	want := sandboxExitReason(exit)
	ends := sender.sessionEnds()
	if len(ends) != 1 || ends[0].SessionID != id || ends[0].ExitCode != 1 || ends[0].Message != want {
		t.Fatalf("session_ended = %+v, want one exit 1 for %s with %q", ends, id, want)
	}
	stops := sender.sandboxStopEvents(t)
	if len(stops) != 1 || stops[0].Code != agent.ErrorCodeRuntimeStopped || stops[0].Retryable || stops[0].Message != want || *stops[0].SandboxExit != exit {
		t.Fatalf("sandbox stop events = %+v, want one runtime_stopped with %+v", stops, exit)
	}
	if host.Has(id) {
		t.Fatal("the session is still hosted")
	}
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("error statuses beside the end: %v", errs)
	}
	for _, m := range sender.errorMessages(t) {
		if strings.Contains(m, "update Codex") || strings.Contains(m, "repeatedly failed") {
			t.Fatalf("the generic engine advice was given: %q", m)
		}
	}
}

// Claude restarts its engine per message and never gives up by itself: a sandboxed Claude session
// whose container stopped while it was idle is ended by the daemon's watch, with Docker's reason.
func TestSandboxedClaudeEndsWhenItsContainerStops(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, _, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))
	var dead atomic.Bool
	exit := agent.SandboxExit{Status: "exited", ExitCode: 137, FinishedAt: "2026-10-21T19:57:53Z"}
	stubSandboxExit(t, &dead, exit)
	fastSandboxWatch(t, 50*time.Millisecond)
	id := "s-claude-dies-" + ccUUID()[:8]
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: id, Repo: "proj", Kind: "agent", Sandbox: true, InitialPrompt: "hello"})
	cleanupSandboxHost(t, host)
	sender.waitForKind(t, "turn_done")
	time.Sleep(200 * time.Millisecond) // several looks at a live container: nothing happens
	if len(sender.sessionEnds()) > 0 {
		t.Fatalf("a live container's session ended: %+v", sender.sessionEnds())
	}
	dead.Store(true)
	assertEndedForSandbox(t, host, sender, id, exit)
}

// Hermes waited for ever for a retry in a container that could never run its gateway. It now looks
// at the container after a gateway failure and stops at once: one launch, no retries, Docker's
// reason. The periodic watch is off here, so the driver's own check is what ends it.
func TestSandboxedHermesStopsWhenItsContainerStopped(t *testing.T) {
	engineDir := fakeHermes(t, "unused", 0)
	host, sender, logPath, _ := sandboxHost(t, engineDir, AgentHostConfig{})
	var dead atomic.Bool
	dead.Store(true)
	exit := agent.SandboxExit{Status: "exited", ExitCode: 137, OOMKilled: true, FinishedAt: "2026-10-21T19:57:53Z"}
	stubSandboxExit(t, &dead, exit)
	t.Setenv("FAKE_DOCKER_EXEC_FAIL", "1") // every exec fails the way a stopped container's does
	id := "s-hermes-dies-" + ccUUID()[:8]
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: id, Repo: "proj", Kind: "agent", Engine: "hermes", Sandbox: true, InitialPrompt: "hello"})
	cleanupSandboxHost(t, host)
	assertEndedForSandbox(t, host, sender, id, exit)
	if n := strings.Count(dockerCalls(t, logPath), hermesGatewayPIDFile+`; exec "$@" sh`); n != 1 {
		t.Fatalf("the gateway was started %d times in a stopped container, want 1:\n%s", n, dockerCalls(t, logPath))
	}
}

// Codex retried its app-server in the stopped container and then blamed its version. It now stops
// after the first failure with Docker's reason, and never says "update Codex".
func TestSandboxedCodexStopsWhenItsContainerStopped(t *testing.T) {
	engineDir := fakeCodex(t)
	host, sender, logPath, _ := sandboxHost(t, engineDir, AgentHostConfig{})
	var dead atomic.Bool
	dead.Store(true)
	exit := agent.SandboxExit{Gone: true}
	stubSandboxExit(t, &dead, exit)
	t.Setenv("FAKE_DOCKER_EXEC_FAIL", "1")
	id := "s-codex-dies-" + ccUUID()[:8]
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: id, Repo: "proj", Kind: "agent", Engine: "codex", Sandbox: true, InitialPrompt: "hello"})
	cleanupSandboxHost(t, host)
	assertEndedForSandbox(t, host, sender, id, exit)
	time.Sleep(1500 * time.Millisecond) // past the driver's first retry backoff
	if n := strings.Count(dockerCalls(t, logPath), "app-server"); n != 1 {
		t.Fatalf("the app-server was started %d times in a stopped container, want 1:\n%s", n, dockerCalls(t, logPath))
	}
}

// A stop the person asks for removes the container itself: the watch must not report that as the
// container dying, and the session ends as stopped (exit 0, no reason).
func TestSandboxStopIsNotAContainerDeath(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, _, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	orig := sandboxContainerExit
	sandboxContainerExit = func(string) (agent.SandboxExit, bool) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		return agent.SandboxExit{Gone: true}, true
	}
	t.Cleanup(func() { sandboxContainerExit = orig })
	fastSandboxWatch(t, 20*time.Millisecond)
	cleanupSandboxHost(t, host)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	id := "s-claude-stopped-" + ccUUID()[:8]
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: id, Repo: "proj", Kind: "agent", Sandbox: true, InitialPrompt: "hello"})
	sender.waitForKind(t, "turn_done")
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("watch never probed the container")
	}
	// Stop while the probe is in flight; it returns "gone" only after cancellation.
	host.Kill(id)
	releaseOnce.Do(func() { close(release) })
	done := make(chan struct{})
	go func() { host.sandboxWatches.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("watch never stopped")
	}
	ends := sender.sessionEnds()
	if len(ends) != 1 || ends[0].ExitCode != 0 || ends[0].Message != "" {
		t.Fatalf("session_ended = %+v, want one stop with exit 0", ends)
	}
	if stops := sender.sandboxStopEvents(t); len(stops) != 0 {
		t.Fatalf("a requested stop was reported as the container dying: %+v", stops)
	}
}

// The daemon guard: the reattach and the sweep only touch a container they can tie to this daemon
// (its owner label, or, for a container from before the label, a record of the session). A
// container named like a sandbox that another daemon labeled, or that nobody here has a record
// of, is never removed and never reattached.
func TestSandboxSweepOnlyRemovesThisDaemonsContainers(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	const (
		unrelated = "unrelated-1" // no owner label, no record: someone else's
		foreign   = "foreign-1"   // another daemon's label
		legacy    = "legacy-1"    // no label, but this daemon has its sandbox home
		mine      = "mine-1"      // this daemon's label
	)
	sb, _ := sandboxHomePath(legacy)
	if err := os.MkdirAll(sb, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		sandboxContainerPrefix + unrelated + "\t",
		sandboxContainerPrefix + foreign + "\tsomeone-else",
		sandboxContainerPrefix + legacy + "\t",
		sandboxContainerPrefix + mine + "\t" + sandboxOwner(),
	}
	var removed []string
	origOut, origRun, origProbe := dockerOutput, dockerRun, dockerProbe
	dockerOutput = func(args ...string) ([]byte, error) {
		if args[0] == "ps" && args[1] == "-a" {
			return nil, nil // gone after its removal
		}
		return []byte(strings.Join(lines, "\n") + "\n"), nil
	}
	dockerRun = func(args ...string) error {
		if args[0] == "rm" {
			removed = append(removed, args[len(args)-1])
		}
		return nil
	}
	tmux := false
	dockerProbe = func(time.Duration, ...string) (bool, error) {
		if tmux {
			return false, nil
		}
		return false, errNotInImage
	}
	t.Cleanup(func() {
		dockerOutput, dockerRun, dockerProbe = origOut, origRun, origProbe
		for _, id := range []string{unrelated, foreign, legacy, mine} {
			unregisterSandbox(id)
		}
	})

	sweepOrphanSandboxContainers(nil, nil)
	slices.Sort(removed)
	if want := []string{sandboxContainerPrefix + legacy, sandboxContainerPrefix + mine}; !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want only this daemon's %v", removed, want)
	}

	// The reattach listing (terminal sandboxes, with tmux) is held to the same rule; a record the
	// manager has (tied) also counts.
	removed = nil
	tmux = true
	if err := os.MkdirAll(sb, 0o700); err != nil { // the sweep released the legacy home
		t.Fatal(err)
	}
	got := listSandboxedTmuxSessions(func(id string) bool { return false })
	slices.Sort(got)
	if want := []string{legacy, mine}; !slices.Equal(got, want) {
		t.Fatalf("reattach listed %v, want %v", got, want)
	}
	if _, ok := sandboxContainer(unrelated); ok {
		t.Fatal("an unrelated container was registered as this daemon's")
	}
	if got := listSandboxedTmuxSessions(func(id string) bool { return id == unrelated }); !slices.Contains(got, unrelated) || slices.Contains(got, foreign) {
		t.Fatalf("with a record of %s: listed %v", unrelated, got)
	}
}

// The owner label goes on every sandbox container the daemon starts, and differs between daemons
// with different state directories.
func TestSandboxContainersCarryTheirDaemonsOwnerLabel(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	own := sandboxOwner()
	args := strings.Join(sandboxRunArgs("c1", "/w", "", nil, sandboxRunExtras{owner: own}), " ")
	if own == "" || !strings.Contains(args, " --label "+sandboxOwnerLabel+"="+own+" ") {
		t.Fatalf("no owner label in %q", args)
	}
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	if sandboxOwner() == own {
		t.Fatal("two state directories gave the same owner")
	}
}

// The Codex driver itself, with no host watch around it: after an app-server failure in a stopped
// container it says what Docker recorded and returns at once, without the retries (1 s, then 2 s)
// and without "update Codex".
func TestCodexDriverDoesNotRetryInAStoppedContainer(t *testing.T) {
	engineDir := fakeCodex(t)
	dockerDir, logPath := fakeDocker(t)
	useScratchTurnPIDFile(t)
	t.Setenv("PATH", dockerDir+":"+engineDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("FAKE_DOCKER_EXEC_FAIL", "1")
	var dead atomic.Bool
	dead.Store(true)
	exit := agent.SandboxExit{Status: "exited", ExitCode: 137, FinishedAt: "2026-10-21T19:57:53Z"}
	stubSandboxExit(t, &dead, exit)
	container := "blerg-sandbox-codex-driver-" + ccUUID()[:8]
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	d.useSandbox(sandboxExecPrefix(container))
	done := make(chan struct{})
	start := time.Now()
	go func() { d.Run(t.Context()); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if took := time.Since(start); took > 900*time.Millisecond {
		t.Fatalf("Run took %s: it retried in the stopped container", took)
	}
	var stops []agent.ErrorPayload
	for _, ev := range em.snapshot() {
		if p, ok := ev.Payload.(agent.ErrorPayload); ok && ev.Kind == "error" {
			if strings.Contains(p.Message, "update Codex") {
				t.Fatalf("the generic advice was given: %q", p.Message)
			}
			if p.SandboxExit != nil {
				stops = append(stops, p)
			}
		}
	}
	if len(stops) != 1 || stops[0].Code != agent.ErrorCodeRuntimeStopped || stops[0].Message != sandboxExitReason(exit) {
		t.Fatalf("stop errors = %+v", stops)
	}
	if n := strings.Count(dockerCalls(t, logPath), "app-server"); n != 1 {
		t.Fatalf("app-server started %d times, want 1", n)
	}
	// A message after the stop is refused, not run.
	d.EnqueueInput(agent.Input{Text: "again", ClientID: "late"})
	if n := strings.Count(dockerCalls(t, logPath), "app-server"); n != 1 {
		t.Fatalf("a later message started the app-server again (%d)", n)
	}
}

// A session_ended notification precedes the watch goroutine's return. Join the watch before
// restoring any process-global probe, timer, environment or PID-file stub.
func cleanupSandboxHost(t *testing.T, host *AgentHost) {
	t.Helper()
	t.Cleanup(func() {
		host.mu.Lock()
		var ids []string
		for id := range host.sessions {
			ids = append(ids, id)
		}
		host.mu.Unlock()
		for _, id := range ids {
			host.Kill(id)
		}
		done := make(chan struct{})
		go func() { host.sandboxWatches.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("sandbox watcher did not stop")
		}
	})
}
