package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Real Docker (BLERG_SANDBOX_CHILD_TEST=1 and a built sandbox image), fake engines: every container
// these tests start carries a label of the test's own and is removed only by it. The only
// containers they stop, pause or kill are their own, by their exact name.

// realSandboxHost is an AgentHost whose sandboxed sessions run in real containers of the sandbox
// image, with the fake engine binaries (name -> host file) mounted read-only over the image's
// own. No login, network or person's file is used.
func realSandboxHost(t *testing.T, engines map[string]string) (*AgentHost, *agentTestSender) {
	t.Helper()
	if os.Getenv("BLERG_SANDBOX_CHILD_TEST") != "1" {
		t.Skip("set BLERG_SANDBOX_CHILD_TEST=1 with a built sandbox image")
	}
	labelTestContainers(t)
	useRealDockerContext(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeClaudeLogin(t, home)
	prev := sandboxTestMounts
	for name, file := range engines {
		sandboxTestMounts = append(sandboxTestMounts, "type=bind,src="+file+",dst=/usr/local/bin/"+name+",readonly")
	}
	t.Cleanup(func() { sandboxTestMounts = prev })
	reposRoot := t.TempDir()
	initChildRepo(t, filepath.Join(reposRoot, "proj"))
	sender := &agentTestSender{}
	return NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir()}), sender
}

func dockerTest(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// startRealSandboxSession starts a sandboxed session of engine ("" = Claude) and waits for its
// first turn; it returns the session id and its container, checked to carry a test label.
func startRealSandboxSession(t *testing.T, host *AgentHost, sender *agentTestSender, engine string) (string, string) {
	t.Helper()
	id := "death-" + strings.ToLower(engine+"x") + "-" + ccUUID()[:8]
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: id, Repo: "proj", Kind: "agent", Engine: engine, Sandbox: true, InitialPrompt: "hello"})
	t.Cleanup(func() { host.Kill(id) })
	waitUntilFor(t, 90*time.Second, "the first turn", func() bool {
		if errs := sender.sessionErrors(); len(errs) > 0 {
			t.Fatalf("spawn failed: %v", errs)
		}
		return sender.countKind("turn_done") > 0
	})
	container := dockerContainerName(id)
	if pinned := pinnedDockerArgs(container); len(pinned) != 2 || pinned[0] != "--context" {
		t.Fatalf("the container is not pinned to the docker context it started on: %v", pinned)
	}
	if labels := dockerTest(t, "inspect", "-f", "{{json .Config.Labels}}", container); !strings.Contains(labels, "blerg-test") {
		t.Fatalf("the test's container carries no test label: %s", labels)
	}
	return id, container
}

// A container killed out from under a running session ends that session with the real reason:
// Docker's exit code, OOM flag and time, said once as a runtime_stopped error carrying them. For
// Claude (which never gives up by itself) and Codex (which blamed its version), with the periodic
// watch on; for Codex also with it off, where the driver's own check must do it.
func TestSandboxContainerKilledEndsSessionInDocker(t *testing.T) {
	cases := []struct {
		name   string
		engine string
		fake   func(t *testing.T) string
		watch  time.Duration
	}{
		{"claude", "", func(t *testing.T) string { return filepath.Join(fakeClaude(t), "claude") }, 250 * time.Millisecond},
		{"codex", "codex", func(t *testing.T) string { return filepath.Join(fakeCodex(t), "codex") }, 250 * time.Millisecond},
		{"codex without the periodic watch", "codex", func(t *testing.T) string { return filepath.Join(fakeCodex(t), "codex") }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := "claude"
			if tc.engine != "" {
				bin = tc.engine
			}
			host, sender := realSandboxHost(t, map[string]string{bin: tc.fake(t)})
			fastSandboxWatch(t, tc.watch)
			id, container := startRealSandboxSession(t, host, sender, tc.engine)

			dockerTest(t, "kill", container) // this test's own container, by its exact name

			waitUntilFor(t, 30*time.Second, "the session ends", func() bool { return len(sender.sessionEnds()) > 0 })
			ends := sender.sessionEnds()
			if len(ends) != 1 || ends[0].SessionID != id || ends[0].ExitCode != 1 {
				t.Fatalf("session_ended = %+v, want one exit 1", ends)
			}
			wantStart := "The sandbox container stopped (exit code 137, OOMKilled: no, at 20"
			wantEnd := "Z); start a new session to continue the work"
			if m := ends[0].Message; !strings.HasPrefix(m, wantStart) || !strings.HasSuffix(m, wantEnd) {
				t.Fatalf("end reason = %q", m)
			}
			stops := sender.sandboxStopEvents(t)
			if len(stops) != 1 || stops[0].Code != agent.ErrorCodeRuntimeStopped || stops[0].Message != ends[0].Message {
				t.Fatalf("sandbox stop events = %+v", stops)
			}
			if e := stops[0].SandboxExit; e.Gone || e.ExitCode != 137 || e.OOMKilled || e.Status != "exited" || e.FinishedAt == "" {
				t.Fatalf("recorded exit = %+v", e)
			}
			for _, m := range sender.errorMessages(t) {
				if strings.Contains(m, "update Codex") || strings.Contains(m, "repeatedly failed") {
					t.Fatalf("the generic engine advice was given: %q", m)
				}
			}
			if errs := sender.sessionErrors(); len(errs) > 0 {
				t.Fatalf("error statuses beside the end: %v", errs)
			}
			if host.Has(id) {
				t.Fatal("the session is still hosted")
			}
		})
	}
}

// A paused container (the turn-end sync pauses it around its fetch) is alive: the watch leaves the
// session alone through many looks, and once it is unpaused the session takes its next turn.
func TestPausedSandboxIsNotDeadInDocker(t *testing.T) {
	host, sender := realSandboxHost(t, map[string]string{"claude": filepath.Join(fakeClaude(t), "claude")})
	fastSandboxWatch(t, 100*time.Millisecond)
	id, container := startRealSandboxSession(t, host, sender, "")

	dockerTest(t, "pause", container)
	unpaused := false
	t.Cleanup(func() {
		if !unpaused {
			_ = exec.Command("docker", "unpause", container).Run()
		}
	})
	if exit, dead := sandboxContainerExit(container); dead {
		t.Fatalf("a paused container was taken for dead: %+v", exit)
	}
	time.Sleep(1500 * time.Millisecond) // fifteen looks
	if ends := sender.sessionEnds(); len(ends) > 0 {
		t.Fatalf("a paused container's session ended: %+v", ends)
	}
	dockerTest(t, "unpause", container)
	unpaused = true
	host.UserMessage(id, "again", "chat")
	waitUntilFor(t, 60*time.Second, "the next turn", func() bool { return sender.countKind("turn_done") >= 2 })
	if ends := sender.sessionEnds(); len(ends) > 0 || len(sender.sandboxStopEvents(t)) > 0 {
		t.Fatalf("the session was ended: %+v", ends)
	}
}

// The daemon guard against real containers: the sweep removes a container this daemon labeled as
// its own, and leaves one whose name starts with blerg-sandbox- but that another daemon labeled, or
// that carries no owner label and that this daemon has no record of. `docker ps` is held to this
// test's own label, so the sweep never even sees a container the test did not make.
func TestSandboxSweepLeavesUnrelatedContainersInDocker(t *testing.T) {
	if os.Getenv("BLERG_SANDBOX_CHILD_TEST") != "1" {
		t.Skip("set BLERG_SANDBOX_CHILD_TEST=1 with a built sandbox image")
	}
	label := labelTestContainers(t)
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	origOut := dockerOutput
	dockerOutput = func(args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "ps" {
			args = append([]string{"ps", "--filter", "label=" + label}, args[1:]...)
		}
		return origOut(args...)
	}
	t.Cleanup(func() { dockerOutput = origOut })
	suffix := ccUUID()[:8]
	start := func(id string, owner string) string {
		name := sandboxContainerPrefix + id + "-" + suffix
		args := []string{"run", "-d", "--pull=never", "--name", name, "--label", label, "--network", "none"}
		if owner != "" {
			args = append(args, "--label", sandboxOwnerLabel+"="+owner)
		}
		dockerTest(t, append(args, sandboxImage(), "sleep", "infinity")...)
		return name
	}
	unrelated := start("unrelated", "")
	foreign := start("foreign", "another-daemon")
	mine := start("mine", sandboxOwner())

	sweepOrphanSandboxContainers(nil, nil)

	running := strings.Fields(dockerTest(t, "ps", "--filter", "label="+label, "--format", "{{.Names}}"))
	if !slices.Contains(running, unrelated) || !slices.Contains(running, foreign) {
		t.Fatalf("the sweep removed a container that is not this daemon's; running: %v", running)
	}
	if slices.Contains(running, mine) {
		t.Fatalf("the sweep left this daemon's own orphan; running: %v", running)
	}
}

// useRealDockerContext pins containers to the docker context they start on, as the daemon does.
func useRealDockerContext(t *testing.T) {
	t.Helper()
	orig := currentDockerContext
	currentDockerContext = realDockerContext
	t.Cleanup(func() { currentDockerContext = orig })
}
