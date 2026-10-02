package daemon

import (
	"bytes"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSandboxRegistry verifies the register/lookup/unregister lifecycle that
// tmux.go's helpers rely on to decide bare-host vs docker-exec routing.
func TestSandboxRegistry(t *testing.T) {
	const id = "test-registry-session"
	t.Cleanup(func() { unregisterSandbox(id) })

	if _, ok := sandboxContainer(id); ok {
		t.Fatalf("sandboxContainer(%q) reported registered before any register call", id)
	}

	registerSandbox(id, "blerg-sandbox-"+id)
	container, ok := sandboxContainer(id)
	if !ok || container != "blerg-sandbox-"+id {
		t.Fatalf("sandboxContainer(%q) = (%q, %v), want (%q, true)", id, container, ok, "blerg-sandbox-"+id)
	}

	unregisterSandbox(id)
	if _, ok := sandboxContainer(id); ok {
		t.Fatalf("sandboxContainer(%q) still registered after unregisterSandbox", id)
	}
}

// TestRunTmuxForRouting verifies runTmuxFor builds a bare `tmux` command for an
// unregistered session and a `docker exec <container> tmux` command for a
// registered one — the single branch point every tmux.go helper shares.
func TestRunTmuxForRouting(t *testing.T) {
	const bareID = "bare-session"
	const sandboxID = "sandbox-session"
	const container = "blerg-sandbox-sandbox-session"

	registerSandbox(sandboxID, container)
	t.Cleanup(func() { unregisterSandbox(sandboxID) })

	bareCmd := runTmuxFor(bareID, "capture-pane", "-p")
	wantBare := []string{"tmux", "capture-pane", "-p"}
	if !reflect.DeepEqual(bareCmd.Args, wantBare) {
		t.Errorf("runTmuxFor(bare) args = %v, want %v", bareCmd.Args, wantBare)
	}

	sandboxCmd := runTmuxFor(sandboxID, "capture-pane", "-p")
	wantSandbox := []string{"docker", "exec", container, "tmux", "capture-pane", "-p"}
	if !reflect.DeepEqual(sandboxCmd.Args, wantSandbox) {
		t.Errorf("runTmuxFor(sandbox) args = %v, want %v", sandboxCmd.Args, wantSandbox)
	}
}

// TestTmuxAttachCommandRouting mirrors TestRunTmuxForRouting for
// tmuxAttachCommand specifically: it needs -it (an interactive PTY, unlike the
// one-shot helpers) and is built independently since its result is a []string
// handed to NewSession rather than an *exec.Cmd.
func TestTmuxAttachCommandRouting(t *testing.T) {
	const bareID = "bare-attach-session"
	const sandboxID = "sandbox-attach-session"
	const container = "blerg-sandbox-sandbox-attach-session"

	registerSandbox(sandboxID, container)
	t.Cleanup(func() { unregisterSandbox(sandboxID) })

	got := tmuxAttachCommand(bareID)
	want := []string{"tmux", "attach-session", "-d", "-t", tmuxSessionName(bareID)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tmuxAttachCommand(bare) = %v, want %v", got, want)
	}

	got = tmuxAttachCommand(sandboxID)
	want = []string{"docker", "exec", "-it", container, "tmux", "attach-session", "-d", "-t", tmuxSessionName(sandboxID)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tmuxAttachCommand(sandbox) = %v, want %v", got, want)
	}
}

// TestSandboxRunArgsAreHardened verifies the docker run flags built by
// sandboxRunArgs (desktop-security I7 / R1 part 2): capability drop,
// no-new-privileges, pids/memory limits, no --restart, only the engine
// config dirs that actually exist under home get mounted, and env
// forwarding never leaks the host HOME/PATH or the daemon master token.
// A restricted session (a cron, a grant) runs claude with file tools only: another engine's login
// (codex, hermes) has no business inside its container, so only the claude login is mounted.
func TestSandboxRunArgsClaudeOnlyMountsNoOtherEngineLogin(t *testing.T) {
	home := t.TempDir()
	for _, p := range []string{".claude", ".codex", ".hermes"} {
		if err := os.MkdirAll(filepath.Join(home, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{".claude.json", ".hermes/config.yaml", ".hermes/.env"} {
		if err := os.WriteFile(filepath.Join(home, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	all := " " + strings.Join(sandboxRunArgs("c", "/p", home, nil, sandboxRunExtras{}), " ") + " "
	for _, want := range []string{"/.claude:", "/.claude.json:", "/.codex:", "/.hermes/.env:"} {
		if !strings.Contains(all, want) {
			t.Errorf("an ordinary sandbox lost the %s mount: %s", want, all)
		}
	}
	only := " " + strings.Join(sandboxRunArgs("c", "/p", home, nil, sandboxRunExtras{claudeOnly: true}), " ") + " "
	for _, want := range []string{"/.claude:", "/.claude.json:"} {
		if !strings.Contains(only, want) {
			t.Errorf("a restricted sandbox needs the claude login (%s): %s", want, only)
		}
	}
	for _, bad := range []string{"/.codex:", "/.hermes"} {
		if strings.Contains(only, bad) {
			t.Errorf("a restricted sandbox must not mount %s: %s", bad, only)
		}
	}
}

func TestSandboxRunArgsAreHardened(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := sandboxRunArgs("blerg-sandbox-c1", "/home/dev/repos/app", home,
		[]string{"BLERG_RUNNER_SESSION_ID=s1", "BLERG_RUNNER_DAEMON_TOKEN=leak", "HOME=/x", "PATH=/usr/bin",
			// A board-started session that explicitly chose the sandbox: its
			// board linkage travels in, even though reaching a host-side board
			// from the container is still follow-up work.
			"BLERG_BOARD_BOARD=b1", "BLERG_BOARD_TOKEN=bt"}, sandboxRunExtras{})
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		" run -d --name blerg-sandbox-c1 ", " --cap-drop ALL ", " --security-opt no-new-privileges ",
		" --pids-limit 512 ", " --memory 4g ", " -v /home/dev/repos/app:/workspace ", " -w /workspace ",
		" -v " + home + "/.claude:/home/agent/.claude ", " -e BLERG_RUNNER_SESSION_ID=s1 ",
		" -e BLERG_BOARD_BOARD=b1 ", " -e BLERG_BOARD_TOKEN=bt ", " sleep infinity ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for _, bad := range []string{"--restart", "BLERG_RUNNER_DAEMON_TOKEN", "HOME=/x", "PATH=/usr/bin", "/.codex:"} {
		if strings.Contains(joined, bad) {
			t.Errorf("must not contain %q: %s", bad, joined)
		}
	}
	// The image is always the last thing before the command.
	if args[len(args)-3] != sandboxImage() || args[len(args)-2] != "sleep" || args[len(args)-1] != "infinity" {
		t.Errorf("tail = %v, want [<image> sleep infinity]", args[len(args)-3:])
	}
}

// TestDockerContainerName documents the naming convention listSandboxedTmuxSessions
// relies on to recover a session ID from `docker ps` output.
func TestDockerContainerName(t *testing.T) {
	got := dockerContainerName("abc-123")
	want := "blerg-sandbox-abc-123"
	if got != want {
		t.Errorf("dockerContainerName(%q) = %q, want %q", "abc-123", got, want)
	}
}

// TestKillTmuxSessionRemovesSandboxContainer verifies killTmuxSession's docker
// cleanup runs only for a registered session and unregisters it afterward,
// without actually invoking docker or tmux — both binaries here run against
// nonexistent names/containers and are expected to fail silently (killTmuxSession
// only ever best-effort `_ = cmd.Run()`s them).
func TestKillTmuxSessionRemovesSandboxContainer(t *testing.T) {
	const id = "kill-test-session"
	registerSandbox(id, "blerg-sandbox-"+id)

	killTmuxSession(id)

	if _, ok := sandboxContainer(id); ok {
		t.Errorf("killTmuxSession(%q) left the session registered as a sandbox", id)
	}
}

// ─── sandbox network, URL rewrite, CLI mount ────────────────────────────────

// recordDockerRun swaps the dockerRun and dockerOutput seams for one recorder.
// networkExists picks the answer `docker network inspect` gets: success, or
// docker's own "No such network"; every other call succeeds.
func recordDockerRun(t *testing.T, networkExists bool) *[][]string {
	t.Helper()
	var inspectErr error
	if !networkExists {
		inspectErr = errors.New("Error response from daemon: network blerg-sandbox not found")
	}
	return recordDockerCalls(t, inspectErr)
}

// recordDockerCalls is recordDockerRun with the inspect's error given verbatim.
func recordDockerCalls(t *testing.T, inspectErr error) *[][]string {
	t.Helper()
	var calls [][]string
	isInspect := func(args []string) bool {
		return len(args) >= 2 && args[0] == "network" && args[1] == "inspect"
	}
	origRun, origOut := dockerRun, dockerOutput
	dockerRun = func(args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		if isInspect(args) {
			return inspectErr
		}
		return nil
	}
	dockerOutput = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if isInspect(args) {
			return nil, inspectErr
		}
		return nil, nil
	}
	t.Cleanup(func() { dockerRun, dockerOutput = origRun, origOut })
	return &calls
}

// TestSandboxNetworkStateClassifiesInspectErrors: only docker's own "no such
// network" answer means the network is missing; anything else is an error the
// spawn must not paper over.
func TestSandboxNetworkStateClassifiesInspectErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"exists", nil, false},
		// Docker's real "missing" answers.
		{"no such network", errors.New("Error: No such network: blerg-sandbox"), false},
		{"not found (older docker)", errors.New("Error response from daemon: network blerg-sandbox not found"), false},
		{"not found, any case", errors.New("Error response from daemon: Network BLERG-SANDBOX Not Found"), false},
		// Everything else is an error, however close it looks.
		{"docker unreachable", errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"), true},
		{"permission", errors.New("permission denied while trying to connect to the Docker daemon socket"), true},
		{"permission mentioning network", errors.New("permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock: Get \"http://%2Fvar%2Frun%2Fdocker.sock/v1.45/networks/blerg-sandbox\": network"), true},
		{"no docker binary", errors.New(`exec: "docker": executable file not found in $PATH`), true},
		{"network is unreachable", errors.New("dial tcp 192.0.2.1:2376: connect: network is unreachable"), true},
		{"spoof: network … not found later", errors.New("network is unreachable; docker: executable file not found"), true},
		{"another network not found", errors.New("Error response from daemon: network other-net not found"), true},
	} {
		recordDockerCalls(t, tc.err)
		exists, err := sandboxNetworkState("blerg-sandbox")
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
		if exists != (tc.err == nil) {
			t.Errorf("%s: exists = %v", tc.name, exists)
		}
	}
}

// A generic inspect failure fails the start (no container, no registration)
// and leaves the once-only missing-network warning unspent.
func TestStartSandboxContainerFailsWhenTheNetworkCannotBeChecked(t *testing.T) {
	calls := recordDockerCalls(t, errors.New("Cannot connect to the Docker daemon"))
	logs := captureLog(t)
	sandboxNetworkWarned.Store(false)
	t.Cleanup(func() { unregisterSandbox("s-dockerdown") })

	_, err := startSandboxContainer("s-dockerdown", t.TempDir(), hostSessionEnv(), sandboxCredential{},
		SandboxOptions{Network: "blerg-sandbox"})
	if err == nil || !strings.Contains(err.Error(), "could not check the sandbox network: ") ||
		!strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Fatalf("err = %v, want a sandbox-network check failure with docker's reason", err)
	}
	for _, c := range *calls {
		if c[0] == "run" {
			t.Errorf("container started despite the failed check: %v", c)
		}
	}
	if _, ok := sandboxContainer("s-dockerdown"); ok {
		t.Error("session left registered after a failed start")
	}
	if sandboxNetworkWarned.Load() || strings.Contains(logs.String(), "not found") {
		t.Errorf("missing-network warning consumed by an unrelated error: %q", logs.String())
	}
}

// The terminal kind returns the same error from createDockerSandbox.
func TestCreateDockerSandboxFailsWhenTheNetworkCannotBeChecked(t *testing.T) {
	recordDockerCalls(t, errors.New("permission denied"))
	t.Cleanup(func() { unregisterSandbox("s-term-down") })
	err := createDockerSandbox("s-term-down", t.TempDir(), []string{"claude"}, nil,
		SandboxOptions{Network: "blerg-sandbox"})
	if err == nil || !strings.Contains(err.Error(), "could not check the sandbox network: docker network inspect failed: permission denied") {
		t.Fatalf("err = %v, want the network check failure", err)
	}
}

// TestDockerErrorTextPrefersStderrElseNamesTheCommand: docker's own words when
// it said anything; otherwise the command that failed, so the reason is usable.
func TestDockerErrorTextPrefersStderrElseNamesTheCommand(t *testing.T) {
	_, err := exec.Command("sh", "-c", "echo 'Error: No such network: x' >&2; exit 1").Output()
	if got := dockerErrorText(err, "docker network inspect"); got != "Error: No such network: x" {
		t.Errorf("with stderr: got %q", got)
	}
	_, err = exec.Command("sh", "-c", "exit 1").Output()
	if got := dockerErrorText(err, "docker network inspect"); got != "docker network inspect failed: exit status 1" {
		t.Errorf("empty stderr: got %q", got)
	}
	if got := dockerErrorText(errors.New("boom"), "docker network inspect"); got != "docker network inspect failed: boom" {
		t.Errorf("non-exit error: got %q", got)
	}
}

// captureLog redirects the standard logger for one test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

// runCall returns the recorded `docker run` invocation, joined and padded.
func runCall(t *testing.T, calls [][]string) (string, []string) {
	t.Helper()
	for _, c := range calls {
		if len(c) > 0 && c[0] == "run" {
			return " " + strings.Join(c, " ") + " ", c
		}
	}
	t.Fatalf("no docker run recorded; calls = %v", calls)
	return "", nil
}

// hostSessionEnv is what buildSessionEnv hands a desktop session: a loopback
// board URL (compose's), the daemon's own websocket URL, the master token
// (which must never get through) and unrelated host variables.
func hostSessionEnv() []string {
	return []string{
		"BLERG_RUNNER_SESSION_ID=s1",
		"BLERG_RUNNER_SESSION_TOKEN=st",
		"BLERG_RUNNER_SERVER_URL=ws://localhost:8083/ws/daemon",
		"BLERG_RUNNER_DAEMON_TOKEN=master-token-must-not-leak",
		"BLERG_BOARD_URL=http://localhost:8082",
		"BLERG_BOARD_TOKEN=bt",
		"HOME=/x",
	}
}

func assertNoMasterToken(t *testing.T, joined string) {
	t.Helper()
	for _, bad := range []string{"master-token-must-not-leak", "BLERG_RUNNER_DAEMON_TOKEN"} {
		if strings.Contains(joined, bad) {
			t.Errorf("docker run leaked %q: %s", bad, joined)
		}
	}
}

func TestSandboxNetworkEnvRewritesLoopbackURLs(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://localhost:8082", "http://blerg-board:8080"},
		{"http://localhost", "http://blerg-board:8080"},
		{"http://127.0.0.1:8082", "http://blerg-board:8080"},
		{"http://[::1]:8082", "http://blerg-board:8080"},
		{"https://LOCALHOST:8082/", "http://blerg-board:8080/"},
		// A remote board is reachable by egress: leave it alone.
		{"https://board.example.com", "https://board.example.com"},
		{"http://192.0.2.5:8082", "http://192.0.2.5:8082"},
		{"http://localhost.example.com", "http://localhost.example.com"},
		// Parser tricks: the host is evil.example in every one of these.
		{"http://localhost@evil.example/", "http://localhost@evil.example/"},
		{"http://localhost:8082@evil.example", "http://localhost:8082@evil.example"},
		{"http://evil.example#@localhost", "http://evil.example#@localhost"},
		{"http://evil.example?@localhost", "http://evil.example?@localhost"},
		// Loopback forms: the whole 127.0.0.0/8, ::1, any case of localhost;
		// path and query kept.
		{"http://127.0.0.2:8082", "http://blerg-board:8080"},
		{"http://LOCALHOST", "http://blerg-board:8080"},
		{"http://localhost:8082/x?y=1", "http://blerg-board:8080/x?y=1"},
	} {
		got := sandboxNetworkEnv([]string{"BLERG_BOARD_URL=" + tc.in})
		want := []string{"BLERG_BOARD_URL=" + tc.want}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("board URL %q: env = %v, want %v", tc.in, got, want)
		}
	}
}

// Local stack: the daemon's server URL is loopback, so all three host-side
// URLs are rewritten or dropped.
func TestSandboxNetworkEnvLocalStackRewritesEverything(t *testing.T) {
	for _, server := range []string{
		"ws://localhost:8083/ws/daemon", "ws://127.0.0.1:8083/ws/daemon", "ws://[::1]:8083/ws/daemon",
	} {
		got := sandboxNetworkEnv([]string{
			"BLERG_RUNNER_SESSION_ID=s1",
			"BLERG_RUNNER_SERVER_URL=" + server,
			"BLERG_RUNNER_SERVER_HTTP=http://localhost:8083",
			"BLERG_RUNNER_SERVER_HTTP_EXTRA=kept",
			"BLERG_RUNNER_PREVIEW_URL=http://localhost:8083/api/preview",
			"BLERG_BOARD_URL=http://localhost:8082",
			"OTHER=1",
		})
		want := []string{
			"BLERG_RUNNER_SESSION_ID=s1",
			"BLERG_RUNNER_SERVER_HTTP_EXTRA=kept",
			"BLERG_BOARD_URL=http://blerg-board:8080",
			"OTHER=1",
			"BLERG_RUNNER_SERVER_HTTP=http://blerg-runner:8080",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("server %q: env = %v, want %v", server, got, want)
		}
	}
}

// Remote server (a cluster), remote board: nothing to rewrite — the CLI
// derives the right https URL from the forwarded server URL.
func TestSandboxNetworkEnvRemoteServerIsUntouched(t *testing.T) {
	in := []string{
		"BLERG_RUNNER_SESSION_ID=s1",
		"BLERG_RUNNER_SERVER_URL=wss://runner.example.com/ws/daemon",
		"BLERG_RUNNER_SERVER_HTTP=https://runner.example.com",
		"BLERG_RUNNER_PREVIEW_URL=https://runner.example.com/api/preview",
		"BLERG_BOARD_URL=https://board.example.com",
	}
	if got := sandboxNetworkEnv(in); !reflect.DeepEqual(got, in) {
		t.Errorf("env = %v, want it unchanged %v", got, in)
	}
}

// Mixed: remote server, loopback board and preview — only the loopback ones
// change, each on its own.
func TestSandboxNetworkEnvMixedRewritesOnlyLoopback(t *testing.T) {
	got := sandboxNetworkEnv([]string{
		"BLERG_RUNNER_SERVER_URL=wss://runner.example.com/ws/daemon",
		"BLERG_RUNNER_PREVIEW_URL=http://127.0.0.1:8083/api/preview",
		"BLERG_BOARD_URL=http://localhost:8082",
	})
	want := []string{
		"BLERG_RUNNER_SERVER_URL=wss://runner.example.com/ws/daemon",
		"BLERG_BOARD_URL=http://blerg-board:8080",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
}

// Remote server with the network present: the container still joins it, the
// server env is forwarded as-is, and only the loopback board URL is rewritten.
func TestStartSandboxContainerRemoteServerOnTheNetwork(t *testing.T) {
	calls := recordDockerRun(t, true)
	t.Cleanup(func() { unregisterSandbox("s-remote-net") })

	if _, err := startSandboxContainer("s-remote-net", t.TempDir(), []string{
		"BLERG_RUNNER_SESSION_ID=s1",
		"BLERG_RUNNER_SERVER_URL=wss://runner.example.com/ws/daemon",
		"BLERG_RUNNER_DAEMON_TOKEN=master-token-must-not-leak",
		"BLERG_BOARD_URL=http://localhost:8082",
	}, sandboxCredential{}, SandboxOptions{Network: "blerg-sandbox"}); err != nil {
		t.Fatalf("startSandboxContainer: %v", err)
	}
	joined, _ := runCall(t, *calls)
	for _, want := range []string{
		" --network blerg-sandbox ",
		" -e BLERG_RUNNER_SERVER_URL=wss://runner.example.com/ws/daemon ",
		" -e BLERG_BOARD_URL=http://blerg-board:8080 ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "BLERG_RUNNER_SERVER_HTTP") {
		t.Errorf("SERVER_HTTP set for a remote server: %s", joined)
	}
	assertNoMasterToken(t, joined)
}

func TestStartSandboxContainerJoinsTheSandboxNetwork(t *testing.T) {
	calls := recordDockerRun(t, true)
	t.Cleanup(func() { unregisterSandbox("s-net") })

	if _, err := startSandboxContainer("s-net", t.TempDir(), hostSessionEnv(), sandboxCredential{},
		SandboxOptions{Network: "blerg-sandbox"}); err != nil {
		t.Fatalf("startSandboxContainer: %v", err)
	}
	var inspected bool
	for _, c := range *calls {
		if strings.Join(c, " ") == "network inspect blerg-sandbox" {
			inspected = true
		}
	}
	if !inspected {
		t.Errorf("network not inspected; calls = %v", *calls)
	}
	joined, args := runCall(t, *calls)
	for _, want := range []string{
		" --network blerg-sandbox ",
		" -e BLERG_BOARD_URL=http://blerg-board:8080 ",
		" -e BLERG_RUNNER_SERVER_HTTP=http://blerg-runner:8080 ",
		" -e BLERG_RUNNER_SESSION_ID=s1 ", " -e BLERG_RUNNER_SESSION_TOKEN=st ", " -e BLERG_BOARD_TOKEN=bt ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for _, bad := range []string{"BLERG_RUNNER_SERVER_URL", "localhost", "HOME=/x"} {
		if strings.Contains(joined, bad) {
			t.Errorf("must not contain %q: %s", bad, joined)
		}
	}
	assertNoMasterToken(t, joined)
	if args[len(args)-3] != sandboxImage() {
		t.Errorf("image is not right before the command: %v", args)
	}
}

func TestStartSandboxContainerWithoutTheNetworkStaysOnTheBridge(t *testing.T) {
	calls := recordDockerRun(t, false)
	logs := captureLog(t)
	sandboxNetworkWarned.Store(false)
	t.Cleanup(func() { unregisterSandbox("s-nonet"); unregisterSandbox("s-nonet-2") })

	if _, err := startSandboxContainer("s-nonet", t.TempDir(), hostSessionEnv(), sandboxCredential{},
		SandboxOptions{Network: "blerg-sandbox"}); err != nil {
		t.Fatalf("startSandboxContainer: %v", err)
	}
	// A second spawn does not repeat the warning: once per daemon process.
	if _, err := startSandboxContainer("s-nonet-2", t.TempDir(), hostSessionEnv(), sandboxCredential{},
		SandboxOptions{Network: "blerg-sandbox"}); err != nil {
		t.Fatalf("second startSandboxContainer: %v", err)
	}
	joined, _ := runCall(t, *calls)
	if strings.Contains(joined, "--network") {
		t.Errorf("joined a network that does not exist: %s", joined)
	}
	// The env is exactly as given: a daemon talking to a remote server needs
	// its public URLs.
	for _, want := range []string{
		" -e BLERG_BOARD_URL=http://localhost:8082 ",
		" -e BLERG_RUNNER_SERVER_URL=ws://localhost:8083/ws/daemon ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "blerg-board:8080") || strings.Contains(joined, "blerg-runner:8080") {
		t.Errorf("env rewritten without the network: %s", joined)
	}
	assertNoMasterToken(t, joined)

	out := logs.String()
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("want exactly one log line, got %d: %q", n, out)
	}
	if !strings.Contains(out, `"blerg-sandbox"`) || !strings.Contains(out, "board") {
		t.Errorf("log line does not name the network and the board consequence: %q", out)
	}
}

func TestStartSandboxContainerUsesACustomNetworkName(t *testing.T) {
	calls := recordDockerRun(t, true)
	t.Cleanup(func() { unregisterSandbox("s-custom") })

	if _, err := startSandboxContainer("s-custom", t.TempDir(), hostSessionEnv(), sandboxCredential{},
		SandboxOptions{Network: "my-agents"}); err != nil {
		t.Fatalf("startSandboxContainer: %v", err)
	}
	var inspected bool
	for _, c := range *calls {
		if strings.Join(c, " ") == "network inspect my-agents" {
			inspected = true
		}
	}
	if !inspected {
		t.Errorf("custom network not inspected; calls = %v", *calls)
	}
	joined, _ := runCall(t, *calls)
	if !strings.Contains(joined, " --network my-agents ") || strings.Contains(joined, "--network blerg-sandbox") {
		t.Errorf("custom network not used: %s", joined)
	}
}

func TestStartSandboxContainerWithTheNetworkDisabledSkipsTheCheck(t *testing.T) {
	calls := recordDockerRun(t, true)
	t.Cleanup(func() { unregisterSandbox("s-off") })

	if _, err := startSandboxContainer("s-off", t.TempDir(), hostSessionEnv(), sandboxCredential{},
		SandboxOptions{}); err != nil {
		t.Fatalf("startSandboxContainer: %v", err)
	}
	for _, c := range *calls {
		if c[0] == "network" {
			t.Errorf("network checked while disabled: %v", c)
		}
	}
	joined, _ := runCall(t, *calls)
	if strings.Contains(joined, "--network") || !strings.Contains(joined, " -e BLERG_BOARD_URL=http://localhost:8082 ") {
		t.Errorf("disabled network still changed the run: %s", joined)
	}
	assertNoMasterToken(t, joined)
}

// writeMessagingCLI plants the CLI script where RepoRootFromExecutable's repo
// keeps it, and returns its path.
func writeMessagingCLI(t *testing.T, repoRoot string) string {
	t.Helper()
	p := filepath.Join(repoRoot, "runner", ".claude", "skills", "session-messaging", "blerg-runner")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStartSandboxContainerMountsTheMessagingCLI(t *testing.T) {
	calls := recordDockerRun(t, true)
	t.Cleanup(func() { unregisterSandbox("s-cli") })
	repoRoot := t.TempDir()
	cli := writeMessagingCLI(t, repoRoot)

	if _, err := startSandboxContainer("s-cli", t.TempDir(), nil, sandboxCredential{},
		SandboxOptions{RepoRoot: repoRoot}); err != nil {
		t.Fatalf("startSandboxContainer: %v", err)
	}
	joined, _ := runCall(t, *calls)
	if want := " --mount type=bind,src=" + cli + ",dst=/home/agent/.local/bin/blerg-runner,readonly "; !strings.Contains(joined, want) {
		t.Errorf("missing %q in %s", want, joined)
	}
}

// TestCLIMountArgSurvivesAwkwardPaths: --mount (not -v) so a colon in the
// repo path cannot split the spec, and a comma or quote is CSV-quoted the way
// docker's --mount parser reads it.
func TestCLIMountArgSurvivesAwkwardPaths(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/opt/blerg/cli", "type=bind,src=/opt/blerg/cli,dst=/home/agent/.local/bin/blerg-runner,readonly"},
		{"/opt/a:b/cli", "type=bind,src=/opt/a:b/cli,dst=/home/agent/.local/bin/blerg-runner,readonly"},
		{"/opt/a,b/cli", `type=bind,"src=/opt/a,b/cli",dst=/home/agent/.local/bin/blerg-runner,readonly`},
		{`/opt/a"b/cli`, `type=bind,"src=/opt/a""b/cli",dst=/home/agent/.local/bin/blerg-runner,readonly`},
	} {
		if got := cliMountArg(tc.path); got != tc.want {
			t.Errorf("cliMountArg(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestStartSandboxContainerSkipsAMissingMessagingCLI(t *testing.T) {
	for name, repoRoot := range map[string]string{"no script": t.TempDir(), "no repo root": ""} {
		calls := recordDockerRun(t, true)
		if _, err := startSandboxContainer("s-nocli", t.TempDir(), nil, sandboxCredential{},
			SandboxOptions{RepoRoot: repoRoot}); err != nil {
			t.Fatalf("%s: startSandboxContainer: %v", name, err)
		}
		unregisterSandbox("s-nocli")
		if joined, _ := runCall(t, *calls); strings.Contains(joined, ".local/bin/blerg-runner") {
			t.Errorf("%s: mounted a CLI that does not exist: %s", name, joined)
		}
	}
}

// TestCreateDockerSandboxGetsTheNetworkAndCLI: a terminal-kind sandbox goes
// through the same container start as an agent-kind one. The fake docker
// records the run and succeeds every in-container tmux call without running it.
func TestCreateDockerSandboxGetsTheNetworkAndCLI(t *testing.T) {
	dockerDir, logPath := fakeDocker(t)
	t.Setenv("PATH", dockerDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("FAKE_DOCKER_EXEC_NOOP", "1")
	t.Setenv("HOME", t.TempDir())
	orig := dockerRun
	dockerRun = func(args ...string) error { return exec.Command("docker", args...).Run() }
	t.Cleanup(func() { dockerRun = orig; unregisterSandbox("s-term") })
	repoRoot := t.TempDir()
	cli := writeMessagingCLI(t, repoRoot)

	if err := createDockerSandbox("s-term", t.TempDir(), []string{"claude"}, hostSessionEnv(),
		SandboxOptions{Network: "blerg-sandbox", RepoRoot: repoRoot}); err != nil {
		t.Fatalf("createDockerSandbox: %v", err)
	}
	calls := dockerCalls(t, logPath)
	for _, want := range []string{
		"network inspect blerg-sandbox",
		"--network blerg-sandbox",
		"-e BLERG_BOARD_URL=http://blerg-board:8080",
		"-e BLERG_RUNNER_SERVER_HTTP=http://blerg-runner:8080",
		"--mount type=bind,src=" + cli + ",dst=/home/agent/.local/bin/blerg-runner,readonly",
		"new-session -d -s " + tmuxSessionName("s-term"),
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q; calls:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "BLERG_RUNNER_SERVER_URL") {
		t.Errorf("host websocket URL forwarded:\n%s", calls)
	}
	assertNoMasterToken(t, calls)
}

// TestManagerPassesSandboxOptionsToBothKinds: the manager's settings reach the
// terminal path (sandboxOptions) and the agent host alike.
func TestManagerPassesSandboxOptionsToBothKinds(t *testing.T) {
	cfg := ManagerConfig{ReposRoot: t.TempDir(), SandboxNetwork: "blerg-sandbox", RepoRoot: "/opt/blerg",
		Command: []string{"unused"}}
	want := SandboxOptions{Network: "blerg-sandbox", RepoRoot: "/opt/blerg"}
	if got := cfg.sandboxOptions(); got != want {
		t.Errorf("sandboxOptions = %+v, want %+v", got, want)
	}
	mgr := NewManager(NewWSClient(Config{}), cfg)
	if got := mgr.agents.cfg.Sandbox; got != want {
		t.Errorf("agent host Sandbox = %+v, want %+v", got, want)
	}
}
