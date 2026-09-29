package daemon

// Sandboxed agent-kind sessions: the engine subprocess runs inside the same
// hardened container terminal sandbox sessions get, via `docker exec`, and the
// one driver that cannot be contained (OpenClaw) is refused at spawn.
// Fakes rather than a real Docker daemon: fakeDocker records every invocation
// and re-executes `docker exec`'s wrapped command on the host, where
// fakeClaude/fakeCodex/fakeHermes stand in for the binaries inside the image.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// fakeDocker writes a stub `docker` executable that records every invocation
// to $FAKE_DOCKER_LOG and, for `docker exec`, runs the wrapped command on the
// host instead. `docker run -d` / `docker rm -f` only record and succeed: the
// daemon checks nothing but their exit status. With $FAKE_DOCKER_EXEC_FAIL set
// an exec fails the way a vanished container does.
//
// A wrapped command that signals a process is recorded and NEVER executed:
// outside a container `kill` would land on a host PID — the daemon's target
// PID is one the container assigned, and on this side of the fake it could
// name any recycled process. Tests assert on the recorded command instead.
func fakeDocker(t *testing.T) (binDir, logPath string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/bash
echo "$@" >> "$FAKE_DOCKER_LOG"
# A ` + "`-e NAME`" + ` credential is read by docker from its own environment, so the
# log records that environment too — otherwise a test could not tell "passed
# safely" from "not passed at all".
if [ "$1" = "run" ]; then
  for v in ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN; do
    if [ -n "${!v}" ]; then echo "ENV $v=${!v}" >> "$FAKE_DOCKER_LOG"; fi
  done
fi
if [ "$1" = "network" ] && [ -n "$FAKE_DOCKER_NETWORK_ERROR" ]; then
  echo "Cannot connect to the Docker daemon at unix:///var/run/docker.sock" >&2
  exit 1
fi
if [ "$1" = "network" ] && [ -n "$FAKE_DOCKER_NETWORK_MISSING" ]; then
  echo "Error: No such network: $3" >&2
  exit 1
fi
if [ "$1" != "exec" ]; then exit 0; fi
if [ -n "$FAKE_DOCKER_EXEC_NOOP" ]; then exit 0; fi
if [ -n "$FAKE_DOCKER_EXEC_FAIL" ]; then
  echo "Error response from daemon: No such container" >&2
  exit 1
fi
shift
while [ $# -gt 0 ]; do
  case "$1" in
    -i|-t|-it) shift ;;
    -w|-e|-u) shift 2 ;;
    *) break ;;
  esac
done
shift   # container name
case "$1" in
  kill) exit 0 ;;                                      # never signal a host PID
  sh) case "$3" in kill*) exit 0 ;; esac ;;            # ... nor via sh -c
esac
exec "$@"
`
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Join(dir, "docker-calls.log")
}

// useScratchTurnPIDFile points the in-container PID file at a scratch path for
// the duration of a test. The fake docker re-executes wrapped commands on the
// host, so the real path would be a host file (and its contents a host PID).
func useScratchTurnPIDFile(t *testing.T) string {
	t.Helper()
	orig := sandboxTurnPIDFile
	sandboxTurnPIDFile = filepath.Join(t.TempDir(), "blerg-turn.pid")
	t.Cleanup(func() { sandboxTurnPIDFile = orig })
	return sandboxTurnPIDFile
}

// writeClaudeLogin plants the credential file claudeAvailable() looks for, so
// a sandboxed Claude spawn authenticates through the mounted ~/.claude —
// the launch sheet's default path — rather than an env credential.
func writeClaudeLogin(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.WriteFile(cred, []byte(`{"claudeAiOauth":{"accessToken":"tok"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sandboxHost builds an AgentHost whose spawns run against fakeDocker plus the
// stub engine binaries in engineBinDir, with a throwaway HOME (so credential
// mounts point at a temp dir, not the developer's own) and a daemon master
// token in the environment — every sandbox test asserts that token never
// reaches a docker argument. The returned home is that throwaway HOME.
func sandboxHost(t *testing.T, engineBinDir string, cfg AgentHostConfig) (host *AgentHost, sender *agentTestSender, logPath, home string) {
	t.Helper()
	dockerDir, logPath := fakeDocker(t)
	useScratchTurnPIDFile(t)
	t.Setenv("PATH", dockerDir+":"+engineBinDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "master-token-must-not-leak")
	// No ambient Claude credential unless a test plants one.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.ReposRoot = reposRoot
	cfg.HomeDir = t.TempDir()
	sender = &agentTestSender{}
	return NewAgentHost(sender, cfg), sender, logPath, home
}

func dockerCalls(t *testing.T, logPath string) string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("no docker invocations recorded: %v", err)
	}
	return string(raw)
}

// waitForSessionError blocks until the host reports a session error (the
// refusal path) and returns its reason.
func (s *agentTestSender) waitForSessionError(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if errs := s.sessionErrors(); len(errs) > 0 {
			return errs[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected a session_state_changed(status=error), got none")
	return ""
}

// assertSandboxRun checks the recorded `docker run` is the same hardened
// container a terminal sandbox gets, and that the master token is not in it.
func assertSandboxRun(t *testing.T, calls, sessionID string) {
	t.Helper()
	for _, want := range []string{
		"run -d --name " + dockerContainerName(sessionID),
		"--cap-drop ALL", "--security-opt no-new-privileges",
		"--pids-limit 512", "--memory 4g", ":" + sandboxWorkdir,
		"-w " + sandboxWorkdir, sandboxImage() + " sleep infinity",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("docker run missing %q; calls:\n%s", want, calls)
		}
	}
	for _, bad := range []string{"master-token-must-not-leak", "BLERG_RUNNER_DAEMON_TOKEN"} {
		if strings.Contains(calls, bad) {
			t.Errorf("docker args leaked %q:\n%s", bad, calls)
		}
	}
}

// assertExecsInContainer checks the engine ran through the exec prefix — no
// per-exec -e block (the container's env was set at `docker run`), and under
// the pid-recording wrapper that makes Interrupt reach inside the container.
func assertExecsInContainer(t *testing.T, calls, sessionID, engineCmd string) {
	t.Helper()
	want := "exec -i -w " + sandboxWorkdir + " " + dockerContainerName(sessionID) +
		" sh -c echo $$ > " + sandboxTurnPIDFile + `; exec "$@" sh ` + engineCmd
	if !strings.Contains(calls, want) {
		t.Errorf("engine not run as %q; calls:\n%s", want, calls)
	}
}

// TestAgentHostSandboxedClaudeRunsClaudeCodeInContainer verifies the spec's
// default combination — Local sandbox + Agent + Claude — creates the hardened
// container and runs `claude -p` inside it, authenticating through the mounted
// host login. It must NOT depend on AgentHostConfig.ClaudeCode, which only the
// cluster pod sets: on a daemon that flag is off and this combination would
// otherwise select the native loop and be refused.
func TestAgentHostSandboxedClaudeRunsClaudeCodeInContainer(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-sbx", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello",
	})
	sender.waitForKind(t, "turn_done")

	calls := dockerCalls(t, logPath)
	assertSandboxRun(t, calls, "s-sbx")
	assertExecsInContainer(t, calls, "s-sbx", "claude -p hello")
	// The login is mounted, so no credential needs to travel in the env.
	if !strings.Contains(calls, "-v "+filepath.Join(home, ".claude")+":/home/agent/.claude") {
		t.Errorf("host claude login not mounted; calls:\n%s", calls)
	}
	if strings.Contains(calls, "ANTHROPIC_API_KEY") {
		t.Errorf("no API key was configured, yet one was passed:\n%s", calls)
	}
	// The BLERG_RUNNER_* session env is forwarded at run time so the in-session
	// messaging CLI works, exactly as for terminal sandboxes.
	if !strings.Contains(calls, "-e BLERG_RUNNER_SESSION_ID=s-sbx") {
		t.Errorf("session env not forwarded into the container; calls:\n%s", calls)
	}
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("sandboxed spawn errored: %v", errs)
	}

	// Ending the session tears the container down, like terminal sandboxes.
	host.Kill("s-sbx")
	if !strings.Contains(dockerCalls(t, logPath), "rm -f "+dockerContainerName("s-sbx")) {
		t.Errorf("container not removed on session end; calls:\n%s", dockerCalls(t, logPath))
	}
	if _, ok := sandboxContainer("s-sbx"); ok {
		t.Error("session still registered as a sandbox after Kill")
	}
}

// TestSandboxedAgentJoinsTheSandboxNetworkWithTheCLI: an agent-kind sandbox
// (what a board-started session runs as) joins the sandbox network, reaches
// the board and runner by service name, and has the messaging CLI mounted.
func TestSandboxedAgentJoinsTheSandboxNetworkWithTheCLI(t *testing.T) {
	engineDir := fakeClaude(t)
	repoRoot := t.TempDir()
	cli := writeMessagingCLI(t, repoRoot)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{
		Sandbox: SandboxOptions{Network: "blerg-sandbox", RepoRoot: repoRoot},
	})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))
	t.Setenv("BLERG_RUNNER_SERVER_URL", "ws://localhost:8083/ws/daemon")

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-board", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello",
		ExtraEnv: map[string]string{"BLERG_BOARD_URL": "http://127.0.0.1:8082"},
	})
	sender.waitForKind(t, "turn_done")
	t.Cleanup(func() { host.Kill("s-board") })

	calls := dockerCalls(t, logPath)
	assertSandboxRun(t, calls, "s-board")
	for _, want := range []string{
		"network inspect blerg-sandbox",
		"--network blerg-sandbox",
		"-e BLERG_BOARD_URL=http://blerg-board:8080",
		"-e BLERG_RUNNER_SERVER_HTTP=http://blerg-runner:8080",
		"--mount type=bind,src=" + cli + ",dst=/home/agent/.local/bin/blerg-runner,readonly",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q; calls:\n%s", want, calls)
		}
	}
	for _, bad := range []string{"BLERG_RUNNER_SERVER_URL", "127.0.0.1:8082"} {
		if strings.Contains(calls, bad) {
			t.Errorf("host-side URL %q forwarded; calls:\n%s", bad, calls)
		}
	}
}

// TestSandboxedAgentFailsWhenTheNetworkCannotBeChecked: docker failing for any
// reason other than "no such network" fails the spawn with a clear session
// error instead of starting on the bridge with unreachable loopback URLs.
func TestSandboxedAgentFailsWhenTheNetworkCannotBeChecked(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{
		Sandbox: SandboxOptions{Network: "blerg-sandbox"},
	})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_DOCKER_NETWORK_ERROR", "1")

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-netfail", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello",
	})
	reason := sender.waitForSessionError(t)
	if !strings.Contains(reason, "could not check the sandbox network: ") ||
		!strings.Contains(reason, "Cannot connect to the Docker daemon") {
		t.Errorf("session error = %q, want the network check failure with docker's reason", reason)
	}
	if strings.Contains(dockerCalls(t, logPath), "run -d") {
		t.Errorf("container started despite the failed check:\n%s", dockerCalls(t, logPath))
	}
}

// TestSandboxedAgentWithoutTheNetworkKeepsItsEnv: no network (an older
// compose, or a daemon talking to a remote server) — default bridge, env as
// given, and the session still starts.
func TestSandboxedAgentWithoutTheNetworkKeepsItsEnv(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{
		Sandbox: SandboxOptions{Network: "blerg-sandbox"},
	})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))
	t.Setenv("FAKE_DOCKER_NETWORK_MISSING", "1")
	t.Setenv("BLERG_RUNNER_SERVER_URL", "wss://runner.example.com/ws/daemon")

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-remote", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello",
		ExtraEnv: map[string]string{"BLERG_BOARD_URL": "https://board.example.com"},
	})
	sender.waitForKind(t, "turn_done")
	t.Cleanup(func() { host.Kill("s-remote") })

	calls := dockerCalls(t, logPath)
	assertSandboxRun(t, calls, "s-remote")
	if strings.Contains(calls, "--network") {
		t.Errorf("joined a missing network; calls:\n%s", calls)
	}
	for _, want := range []string{
		"-e BLERG_BOARD_URL=https://board.example.com",
		"-e BLERG_RUNNER_SERVER_URL=wss://runner.example.com/ws/daemon",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q; calls:\n%s", want, calls)
		}
	}
}

// TestSandboxedClaudeWithOnlyAnAPIKeyPassesItIntoTheContainer covers the
// daemon that has no `claude` login but does have ANTHROPIC_API_KEY: the
// engine still runs inside the container, so that one variable travels with it.
func TestSandboxedClaudeWithOnlyAnAPIKeyPassesItIntoTheContainer(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, _ := sandboxHost(t, engineDir, AgentHostConfig{APIKey: "sk-ant-test"})
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-key", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello",
	})
	sender.waitForKind(t, "turn_done")
	t.Cleanup(func() { host.Kill("s-key") })

	calls := dockerCalls(t, logPath)
	assertSandboxRun(t, calls, "s-key")
	// The variable is named in the args, never valued there: an argv is world-
	// readable out of `ps` for as long as the `docker run` takes. The value
	// travels in the docker client's own environment instead.
	if !strings.Contains(calls, "-e ANTHROPIC_API_KEY ") {
		t.Errorf("API key not passed into the container; calls:\n%s", calls)
	}
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "ENV ") {
			continue
		}
		if strings.Contains(line, "sk-ant-test") {
			t.Errorf("the API key's value reached a docker argument: %q", line)
		}
	}
	if !strings.Contains(calls, "ENV ANTHROPIC_API_KEY=sk-ant-test") {
		t.Errorf("the API key did not reach the docker client's environment; calls:\n%s", calls)
	}
	assertExecsInContainer(t, calls, "s-key", "claude -p hello")
	if errs := sender.sessionErrors(); len(errs) > 0 {
		t.Fatalf("sandboxed spawn errored: %v", errs)
	}
}

// TestSandboxedClaudeWithoutAnyCredentialIsRefused: with neither a login nor a
// key there is nothing the container could authenticate with, so the spawn is
// refused with the spec's reason and no container is created.
func TestSandboxedClaudeWithoutAnyCredentialIsRefused(t *testing.T) {
	host, sender, logPath, _ := sandboxHost(t, t.TempDir(), AgentHostConfig{})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-nocred", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello",
	})

	got := sender.waitForSessionError(t)
	want := "no Claude login or API key is available to the sandbox — run `claude` login on this machine, set ANTHROPIC_API_KEY for the daemon, pick Codex/Hermes, or choose This machine"
	if got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Errorf("docker was invoked for a refused spawn:\n%s", dockerCalls(t, logPath))
	}
	if _, ok := sandboxContainer("s-nocred"); ok {
		t.Error("a container stayed registered for a refused spawn")
	}
}

// TestAgentHostSandboxedCLIEnginesRunInContainer mirrors the Claude case for
// each registered CLI engine that can be contained: same container, same exec
// wrapping, and the bare binary name (a host absolute path does not exist in
// the image).
func TestAgentHostSandboxedCLIEnginesRunInContainer(t *testing.T) {
	cases := []struct {
		engine   string
		logVar   string
		binDir   func(t *testing.T) string
		wantCmd  string
		model    string
		effort   string
		wantArgs string // the model/effort flags as the exec'd command carries them
	}{
		{"codex", "FAKE_CODEX_LOG", fakeCodex, "codex exec", "gpt-5.6-terra", "ultra",
			"--model gpt-5.6-terra -c model_reasoning_effort=ultra"},
		{"hermes", "FAKE_HERMES_LOG", func(t *testing.T) string { return fakeHermes(t, "hi there", 0) }, "hermes chat",
			"qwen3-30b", "minimal", "-m qwen3-30b --reasoning minimal"},
	}
	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			engineDir := tc.binDir(t)
			sessionID := "s-" + tc.engine
			host, sender, logPath, _ := sandboxHost(t, engineDir, AgentHostConfig{})
			t.Setenv(tc.logVar, filepath.Join(engineDir, "engine-calls.log"))

			host.Spawn(protocol.SpawnSession{
				Type: "spawn_session", SessionID: sessionID, Repo: "proj", Kind: "agent",
				Engine: tc.engine, Sandbox: true, InitialPrompt: "hello",
				Model: tc.model, Effort: tc.effort,
			})
			sender.waitForKind(t, "turn_done")
			t.Cleanup(func() { host.Kill(sessionID) })

			calls := dockerCalls(t, logPath)
			assertSandboxRun(t, calls, sessionID)
			assertExecsInContainer(t, calls, sessionID, tc.wantCmd)
			if !strings.Contains(calls, tc.wantArgs) {
				t.Errorf("model/effort flags %q not in the exec'd command; calls:\n%s", tc.wantArgs, calls)
			}
			if errs := sender.sessionErrors(); len(errs) > 0 {
				t.Fatalf("sandboxed spawn errored: %v", errs)
			}
		})
	}
}

// TestAgentHostSandboxRefusesOpenclaw verifies OpenClaw stays host-only.
func TestAgentHostSandboxRefusesOpenclaw(t *testing.T) {
	host, sender, logPath, _ := sandboxHost(t, t.TempDir(), AgentHostConfig{})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-oc", Repo: "proj", Kind: "agent",
		Engine: "openclaw", Sandbox: true, InitialPrompt: "hello",
	})

	if got, want := sender.waitForSessionError(t), "OpenClaw runs only on the host"; got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Errorf("docker was invoked for a refused spawn:\n%s", dockerCalls(t, logPath))
	}
}

// TestUnsandboxedAgentSpawnTouchesNoDocker pins the other half of the switch:
// Sandbox=false is today's behaviour exactly — the engine runs on the host and
// docker is never invoked.
func TestUnsandboxedAgentSpawnTouchesNoDocker(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, _ := sandboxHost(t, engineDir, AgentHostConfig{ClaudeCode: true})
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-host", Repo: "proj", Kind: "agent",
		InitialPrompt: "hello",
	})
	sender.waitForKind(t, "turn_done")
	t.Cleanup(func() { host.Kill("s-host") })

	if _, err := os.Stat(logPath); err == nil {
		t.Errorf("an unsandboxed agent spawn invoked docker:\n%s", dockerCalls(t, logPath))
	}
}

// TestSandboxedInterruptSignalsInsideTheContainer: killing the host-side
// `docker exec` client does not signal the engine in the container, so
// Interrupt must send the signal in there, to the PID the turn recorded.
func TestSandboxedInterruptSignalsInsideTheContainer(t *testing.T) {
	dockerDir, logPath := fakeDocker(t)
	pidFile := useScratchTurnPIDFile(t)
	t.Setenv("PATH", dockerDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", logPath)

	d := newClaudeCodeDriver(t.TempDir(), "", "", &collectEmitter{}, nil)
	d.useSandbox(sandboxExecPrefix("blerg-sandbox-s-int"))
	d.Interrupt()

	want := "exec blerg-sandbox-s-int sh -c kill -TERM $(cat " + pidFile + ")"
	if calls := dockerCalls(t, logPath); !strings.Contains(calls, want) {
		t.Errorf("interrupt did not signal inside the container (want %q); calls:\n%s", want, calls)
	}
	// The fake records the signal but must never deliver one: nothing outside a
	// container is a legitimate target. It reports success without running
	// anything — a kill actually executed here would have exited non-zero,
	// there being no such PID file on this side.
	if _, err := os.Stat(pidFile); err == nil {
		t.Errorf("the test wrote a real PID file at %s", pidFile)
	}
	if err := d.prefix.interrupt(); err != nil {
		t.Errorf("fake docker executed the kill instead of recording it: %v", err)
	}
}

// TestSandboxedTurnClearsItsPIDFile: a finished turn's PID is removed inside
// the container, so a later Interrupt cannot signal a recycled PID.
func TestSandboxedTurnClearsItsPIDFile(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-pid", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello",
	})
	sender.waitForKind(t, "turn_done")
	t.Cleanup(func() { host.Kill("s-pid") })

	want := "exec " + dockerContainerName("s-pid") + " rm -f " + sandboxTurnPIDFile
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(dockerCalls(t, logPath), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("turn did not clear its PID file (want %q); calls:\n%s", want, dockerCalls(t, logPath))
}

// TestSandboxedTurnFailureIsEmittedAsAnError: a `docker exec` that exits
// non-zero without the engine ever producing a result line must surface as an
// error event carrying docker's own message, not an empty turn.
func TestSandboxedTurnFailureIsEmittedAsAnError(t *testing.T) {
	dockerDir, logPath := fakeDocker(t)
	t.Setenv("PATH", dockerDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("FAKE_DOCKER_EXEC_FAIL", "1")

	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "", "", em, nil)
	d.useSandbox(sandboxExecPrefix("blerg-sandbox-s-gone"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.Enqueue("hello", "chat")

	deadline := time.After(5 * time.Second)
	for !contains(em.kinds(), "turn_done") {
		select {
		case <-deadline:
			t.Fatalf("no turn_done; kinds: %v", em.kinds())
		case <-time.After(10 * time.Millisecond):
		}
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, ev := range em.events {
		if ev.Kind != "error" {
			continue
		}
		p, ok := ev.Payload.(agent.ErrorPayload)
		if !ok {
			t.Fatalf("error payload type %T", ev.Payload)
		}
		if !strings.Contains(p.Message, "No such container") {
			t.Errorf("error message %q does not carry docker's reason", p.Message)
		}
		return
	}
	t.Fatalf("a failed sandboxed turn emitted no error event; kinds: %v", em.kinds())
}

// TestStartSandboxContainerClearsAStaleContainerFirst: a container left by an
// earlier life of the session still owns the name, and `docker run --name`
// would fail on the collision.
func TestStartSandboxContainerClearsAStaleContainerFirst(t *testing.T) {
	var calls [][]string
	orig := dockerRun
	dockerRun = func(args ...string) error {
		calls = append(calls, args)
		return nil
	}
	t.Cleanup(func() { dockerRun = orig; unregisterSandbox("s-stale") })

	if _, err := startSandboxContainer("s-stale", t.TempDir(), nil, sandboxCredential{}, SandboxOptions{}); err != nil {
		t.Fatalf("startSandboxContainer: %v", err)
	}
	if len(calls) < 2 {
		t.Fatalf("docker calls = %v, want a remove then a run", calls)
	}
	name := dockerContainerName("s-stale")
	if got := strings.Join(calls[0], " "); got != "rm -f "+name {
		t.Errorf("first call = %q, want %q", got, "rm -f "+name)
	}
	if got := strings.Join(calls[1], " "); !strings.HasPrefix(got, "run -d --name "+name) {
		t.Errorf("second call = %q, want the docker run", got)
	}
}

// TestListSandboxedTmuxSessionsSkipsAgentContainers: an agent sandbox has no
// tmux server inside it, so the reconnect pass must not treat it as a
// reattachable terminal session.
func TestListSandboxedTmuxSessionsSkipsAgentContainers(t *testing.T) {
	origOut, origRun := dockerOutput, dockerRun
	dockerOutput = func(args ...string) ([]byte, error) {
		return []byte(sandboxContainerPrefix + "term-1\n" + sandboxContainerPrefix + "agent-1\n"), nil
	}
	dockerRun = func(args ...string) error {
		// `docker exec <container> tmux ls` fails for the agent container.
		if len(args) > 1 && args[1] == sandboxContainerPrefix+"agent-1" {
			return errNotInImage
		}
		return nil
	}
	t.Cleanup(func() {
		dockerOutput, dockerRun = origOut, origRun
		unregisterSandbox("term-1")
		unregisterSandbox("agent-1")
	})

	got := listSandboxedTmuxSessions()
	if len(got) != 1 || got[0] != "term-1" {
		t.Fatalf("listSandboxedTmuxSessions() = %v, want [term-1]", got)
	}
	if _, ok := sandboxContainer("agent-1"); ok {
		t.Error("an agent container was registered as a reattachable tmux sandbox")
	}
}

// TestPreflightSandboxedAgentChecksTheImageNotTheHostPath verifies preflight
// for a sandboxed agent-kind spawn: the image must exist, the engine binary is
// looked for inside the image, and the host PATH is never consulted.
func TestPreflightSandboxedAgentChecksTheImageNotTheHostPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing on the host PATH
	sender := &agentTestSender{}
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: t.TempDir()})
	mgr.agents = NewAgentHost(sender, AgentHostConfig{HomeDir: t.TempDir()})
	msg := protocol.SpawnSession{Type: "spawn_session", SessionID: "s1", Repo: "proj", Kind: "agent", Sandbox: true}

	// 1. Missing image → the existing, actionable image error.
	mgr.sandboxImagePresent = func() bool { return false }
	mgr.sandboxEnginePresent = func(string) error { return nil }
	err := mgr.preflightEngine(msg)
	if err == nil || !strings.Contains(err.Error(), "sandbox image "+sandboxImage()+" missing") {
		t.Fatalf("missing image: err = %v, want the sandbox-image error", err)
	}

	// 2. Image present, engine missing inside it → names the binary and the
	//    image. The binary is `claude` even though this daemon's host-side
	//    claude-code flag is off: sandboxed Claude always shells to the CLI.
	mgr.sandboxImagePresent = func() bool { return true }
	var checked string
	mgr.sandboxEnginePresent = func(bin string) error {
		checked = bin
		return errNotInImage
	}
	err = mgr.preflightEngine(msg)
	if err == nil || !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), sandboxImage()) {
		t.Fatalf("missing engine in image: err = %v", err)
	}
	if checked != "claude" {
		t.Errorf("checked binary = %q, want %q", checked, "claude")
	}

	// 3. Both present → no error, even with an empty host PATH.
	mgr.sandboxEnginePresent = func(string) error { return nil }
	if err := mgr.preflightEngine(msg); err != nil {
		t.Fatalf("sandboxed agent preflight consulted the host PATH: %v", err)
	}
}

// errNotInImage stands in for docker's non-zero exit from an in-image check.
var errNotInImage = os.ErrNotExist

// TestEngineInSandboxImageRunsTheCheckInTheImage pins the real check's shape:
// a throwaway `docker run --rm <image> which <bin>`, never a host lookup.
func TestEngineInSandboxImageRunsTheCheckInTheImage(t *testing.T) {
	var got []string
	orig := dockerRun
	dockerRun = func(args ...string) error { got = args; return nil }
	t.Cleanup(func() { dockerRun = orig })

	if err := engineInSandboxImage("codex"); err != nil {
		t.Fatalf("engineInSandboxImage: %v", err)
	}
	if want := "run --rm " + sandboxImage() + " which codex"; strings.Join(got, " ") != want {
		t.Fatalf("docker args = %v, want %q", got, want)
	}
}

// TestRemoveSandboxContainerFallsBackToTheNameAfterARestart: the registry is
// in-memory, so a kill that arrives after a daemon restart finds nothing in it
// — and must still remove the container the previous life left running, which
// is deterministically named after the session.
func TestRemoveSandboxContainerFallsBackToTheNameAfterARestart(t *testing.T) {
	var calls [][]string
	orig := dockerRun
	dockerRun = func(args ...string) error {
		calls = append(calls, args)
		return nil
	}
	t.Cleanup(func() { dockerRun = orig })

	// An empty registry is what a restarted daemon has.
	removeSandboxContainer("s-restarted")

	name := dockerContainerName("s-restarted")
	if len(calls) != 1 || strings.Join(calls[0], " ") != "rm -f "+name {
		t.Fatalf("docker calls = %v, want a single %q", calls, "rm -f "+name)
	}
}

// TestStartSandboxContainerClaimsTheNameBeforeRunning: the sweep works from
// container names and removes any it finds unregistered, so a spawn must claim
// its name before `docker run` — otherwise a reconnect landing mid-spawn kills
// the container that spawn just created.
func TestStartSandboxContainerClaimsTheNameBeforeRunning(t *testing.T) {
	var swept []string
	origRun := dockerRun
	dockerRun = func(args ...string) error {
		switch {
		case len(args) > 1 && args[0] == "run":
			// A reconnect sweep, racing this spawn: the container exists now.
			dockerOutputSwap(t, sandboxContainerPrefix+"s-race")
			sweepOrphanSandboxContainers(nil)
		case len(args) > 2 && args[0] == "exec" && args[2] == "tmux":
			// An agent sandbox holds no tmux — the sweep's other excuse gone,
			// so only the registration can save this container.
			return errNotInImage
		case len(args) == 3 && args[0] == "rm":
			swept = append(swept, args[2])
		}
		return nil
	}
	t.Cleanup(func() { dockerRun = origRun; unregisterSandbox("s-race") })

	if _, err := startSandboxContainer("s-race", t.TempDir(), nil, sandboxCredential{}, SandboxOptions{}); err != nil {
		t.Fatalf("startSandboxContainer: %v", err)
	}
	// The pre-run `rm -f` of a stale name is expected; a second one would be
	// the sweep having removed the live container.
	for i, name := range swept {
		if i > 0 && name == dockerContainerName("s-race") {
			t.Fatalf("the sweep removed the container being spawned: %v", swept)
		}
	}
	if _, ok := sandboxContainer("s-race"); !ok {
		t.Error("the session is not registered after a successful start")
	}
}

// dockerOutputSwap points `docker ps` at a fixed list of container names for
// the rest of the test.
func dockerOutputSwap(t *testing.T, names ...string) {
	t.Helper()
	orig := dockerOutput
	dockerOutput = func(...string) ([]byte, error) {
		return []byte(strings.Join(names, "\n")), nil
	}
	t.Cleanup(func() { dockerOutput = orig })
}

// TestSweepOrphanSandboxContainers: the reconnect pass removes the agent-kind
// sandbox containers nothing owns any more, and leaves alone both the terminal
// sandboxes it can still reattach to and the sessions it manages right now.
func TestSweepOrphanSandboxContainers(t *testing.T) {
	const (
		orphan  = "agent-orphan"
		term    = "term-live"
		managed = "agent-managed"
		known   = "agent-registered"
	)
	var removed []string
	origOut, origRun := dockerOutput, dockerRun
	dockerOutput = func(args ...string) ([]byte, error) {
		out := ""
		for _, n := range []string{orphan, term, managed, known} {
			out += sandboxContainerPrefix + n + "\n"
		}
		return []byte(out), nil
	}
	dockerRun = func(args ...string) error {
		switch {
		case len(args) > 2 && args[0] == "exec" && args[2] == "tmux":
			// Only the terminal sandbox has a tmux server inside it.
			if args[1] == sandboxContainerPrefix+term {
				return nil
			}
			return errNotInImage
		case len(args) == 3 && args[0] == "rm":
			removed = append(removed, args[2])
			return nil
		}
		return nil
	}
	registerSandbox(known, dockerContainerName(known))
	t.Cleanup(func() {
		dockerOutput, dockerRun = origOut, origRun
		unregisterSandbox(known)
	})

	sweepOrphanSandboxContainers(func(id string) bool { return id == managed })

	if len(removed) != 1 || removed[0] != sandboxContainerPrefix+orphan {
		t.Fatalf("removed = %v, want just the orphaned agent container", removed)
	}
}
