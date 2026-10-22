package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// sandboxImage returns the Docker image "Local sandbox" sessions run in.
// Built by runner/sandbox/Dockerfile; see DAEMON.md for the build step.
// Override with BLERG_RUNNER_SANDBOX_IMAGE to pin a specific tag or point at
// a private registry instead of the locally-built ":latest". A function
// (not a package var) so it re-reads the env on every call — matches
// openclawContextBudgetChars's pattern and stays testable with t.Setenv.
func sandboxImage() string {
	return envOr("BLERG_RUNNER_SANDBOX_IMAGE", "blerg-runner-sandbox:latest")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

const sandboxContainerPrefix = "blerg-sandbox-"

// sandboxWorkdir is the fixed in-container mount point for the session's repo.
// A session's tmux pane (and Claude's cwd) always run here, never at the
// host's real path — the container has no reason to know it.
const sandboxWorkdir = "/workspace"

func dockerContainerName(sessionID string) string {
	return sandboxContainerPrefix + sessionID
}

// sandboxOwnerLabel is the label every sandbox container carries with the identity of the daemon
// that started it (sandboxOwner). The name prefix says only "a Blerg sandbox": another daemon on the
// same Docker, or a test, makes containers with the same prefix, and a cleanup pass must never
// remove those.
const sandboxOwnerLabel = "blerg.runner.owner"

const sandboxContextLabel = "blerg.runner.docker-context"

// sandboxOwner is this daemon's owner label value: derived from its state directory, which holds
// the records of its sessions (two daemons never share one). "" when there is no state directory.
func sandboxOwner() string {
	state := DaemonStateDir()
	if state == "" {
		return ""
	}
	if abs, err := filepath.Abs(state); err == nil {
		state = abs
	}
	// The same directory reached through a symlink is the same daemon.
	if resolved, err := filepath.EvalSymlinks(state); err == nil {
		state = resolved
	}
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:8])
}

// dockerRun runs `docker <args>` and reports only whether it succeeded — the
// shape every sandbox lifecycle call (run/rm, and the in-image engine check)
// needs. A package var so tests can record invocations, or stub them, without
// a Docker daemon.
var dockerRun = func(args ...string) error {
	timeout := dockerCallTimeout
	if len(args) > 0 && args[0] == "exec" {
		timeout = sandboxTmuxTimeout // a command inside a container: the session's to stall
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // literal docker binary, args assembled by the daemon (no shell)
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// dockerCallTimeout bounds every dockerRun and dockerOutput call: no docker call may hold a
// spawn, a kill or the reattach pass for ever. sandboxTmuxTimeout bounds a tmux call or any other
// short command (dockerRun "exec") inside a sandbox, and sandboxProbeTimeout the tmux probe of the
// reattach and cleanup passes.
var (
	dockerCallTimeout   = 3 * time.Minute
	sandboxTmuxTimeout  = 15 * time.Second
	sandboxProbeTimeout = 10 * time.Second
)

// dockerRunEnv is dockerRun with extra environment for the docker client
// process itself. It exists for `docker run -e NAME` (no `=value`): docker
// reads such a variable's value from its own environment, which is how a
// secret reaches the container without ever appearing in the daemon's argv.
// With nothing to add it is exactly dockerRun, so every existing test stub
// still sees the call.
var dockerRunEnv = func(env []string, args ...string) error {
	if len(env) == 0 {
		return dockerRun(args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerCallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // literal docker binary, argv assembled by the daemon (no shell); container name is generated, remote and image come from validated config
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// dockerOutput is dockerRun's sibling for the calls whose stdout matters
// (`docker ps`). Same reason for being a var: tests stub it.
var dockerOutput = func(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerCallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // literal docker binary, args assembled by the daemon (no shell)
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// sandboxRegistry maps a sandboxed session's ID to its container name, so the
// tmux helpers in tmux.go (capture, attach, kill, refresh, ...) can
// transparently route through `docker exec` instead of running tmux directly
// on the host. Absence means "bare host tmux" — the default, non-sandboxed
// path — so every existing call site needs no changes at all. Repopulated
// from live `docker ps` state (not persisted directly) on daemon restart —
// see listSandboxedTmuxSessions.
var sandboxRegistry sync.Map // sessionID string -> container string

func registerSandbox(sessionID, container string) { sandboxRegistry.Store(sessionID, container) }
func unregisterSandbox(sessionID string)          { sandboxRegistry.Delete(sessionID) }

func sandboxContainer(sessionID string) (string, bool) {
	v, ok := sandboxRegistry.Load(sessionID)
	if !ok {
		return "", false
	}
	name, ok := v.(string)
	return name, ok
}

// runTmuxFor returns the exec.Cmd for a tmux subcommand targeting sessionID,
// routed through `docker exec` when sessionID is a registered sandbox.
func runTmuxFor(sessionID string, args ...string) *exec.Cmd {
	if container, ok := sandboxContainer(sessionID); ok {
		full := append([]string{"exec", container, "tmux"}, args...)
		// Bounded: the tmux inside a sandbox is the session's to break (a socket that never
		// answers), and none of these calls may hold the daemon's reattach, poll or kill paths.
		ctx, cancel := context.WithTimeout(context.Background(), sandboxTmuxTimeout)
		context.AfterFunc(ctx, cancel)
		cmd := exec.CommandContext(ctx, "docker", full...) //nolint:gosec // literal docker binary, `exec <container> tmux ...` argv assembled by the daemon (no shell)
		cmd.WaitDelay = time.Second
		return cmd
	}
	return tmuxCmd(args...)
}

// sandboxEnvArgs is the `-e KEY=VAL` list forwarded into `docker run`: the
// BLERG_RUNNER_* entries of env (what lets `blerg-runner ask/update/note`
// inside the session talk back to the server) and the BLERG_BOARD_* ones (what
// identifies the board a card session answers to) — never the daemon master
// token even if a caller passed it.
//
// BLERG_BOARD_* is forwarded so a board-started session has its board linkage
// inside the container. Reaching that board from there is the sandbox
// network's job: startSandboxContainer rewrites a loopback BLERG_BOARD_URL (and
// the runner server URLs) to service names when the container joins it — see
// sandboxNetworkEnv.
func sandboxEnvArgs(env []string) []string {
	var args []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "BLERG_RUNNER_DAEMON_TOKEN=") {
			continue
		}
		// A host path: the CLI inside finds its token file under its own HOME
		// (sessiontoken.go), not under the daemon's state directory.
		if strings.HasPrefix(kv, DaemonStateDirEnv+"=") {
			continue
		}
		if strings.HasPrefix(kv, "BLERG_RUNNER_") || strings.HasPrefix(kv, "BLERG_BOARD_") {
			args = append(args, "-e", kv)
		}
	}
	return args
}

// SandboxOptions are the daemon-wide settings every sandbox container start
// (terminal and agent kind alike) applies. The zero value joins no network
// and mounts no CLI — what the cluster pod and most tests get.
type SandboxOptions struct {
	// Network is the docker network a sandbox joins when it exists
	// (BLERG_RUNNER_SANDBOX_NETWORK, default blerg-sandbox; "" = never join).
	// The desktop compose declares it and attaches only the board and the
	// runner server to it, so a container on it reaches exactly those two by
	// service name — and not Postgres or core.
	Network string
	// RepoRoot is the checkout the daemon runs from (RepoRootFromExecutable);
	// the messaging CLI is mounted from it. "" = no mount.
	RepoRoot string
	// Engine is the session's engine ("" = Claude), set per session: only its login and
	// configuration go into the sandbox (sandboxhome.go).
	Engine string
	// RestrictedEngine, for a restricted session, overrides Engine.
	RestrictedEngine string
	// PluginCache is set per session when it loads always-on plugins: the
	// session's plugin snapshot (pluginworkshop.go), bind-mounted read-only at
	// sandboxPluginPath. "" = no mount.
	PluginCache string
	// CodexHome is a private per-session CODEX_HOME; exact-path mounts preserve native paths.
	CodexHome string
	// CodexSourceHome is the host path a private CODEX_HOME's links point to (unrestricted sessions
	// only): the session's own Codex copy is mounted there, never the person's directory.
	CodexSourceHome string
}

// sandboxPluginPath is where a sandboxed session sees its plugin snapshot;
// the --plugin-dir flags of such a session are translated to it.
const sandboxPluginPath = "/blerg/plugins"

// Service-name URLs a container on the sandbox network uses in place of the
// host's loopback ones: the compose service names and their in-network port.
const (
	sandboxBoardURL  = "http://blerg-board:8080"
	sandboxRunnerURL = "http://blerg-runner:8080"
)

// sandboxCLIMountPath is where the messaging CLI lands inside the container:
// the image's ~/.local/bin, which is already on its PATH.
const sandboxCLIMountPath = "/home/agent/.local/bin/blerg-runner"

// messagingCLIPath is the host path of the blerg-runner CLI script under
// repoRoot — the same file Provision symlinks into the host's ~/.local/bin —
// or "" when there is no repo root or no script there.
func messagingCLIPath(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	p := filepath.Join(repoRoot, "runner", ".claude", "skills", "session-messaging", "blerg-runner")
	if info, err := os.Stat(p); err != nil || info.IsDir() {
		return ""
	}
	return p
}

// sandboxNetworkWarned records that the missing-network warning was logged.
// A var (not a sync.Once) so tests can reset it.
var sandboxNetworkWarned atomic.Bool

// sandboxNetworkState reports whether the named docker network exists.
//
// Only docker's own "no such network" answer means it does not: then the
// container falls back to the default bridge, and the line explaining what
// the operator loses (a desktop stack's loopback board and runner URLs are
// unreachable from there) is logged once per daemon process, not per spawn.
// Any other failure — docker unreachable, permission, no binary — is returned
// as an error: starting on the bridge with loopback URLs would be a session
// that looks fine and cannot reach anything, and it must not spend the
// once-only warning either.
func sandboxNetworkState(name string) (bool, error) {
	_, err := dockerOutput("network", "inspect", name)
	if err == nil {
		return true, nil
	}
	reason := dockerErrorText(err, "docker network inspect")
	if !isNoSuchNetwork(reason, name) {
		return false, errors.New("could not check the sandbox network: " + reason)
	}
	if !sandboxNetworkWarned.Swap(true) {
		log.Printf("sandbox: docker network %q not found; the container uses the default bridge, so board access from the sandbox will not work on a desktop stack without it", name)
	}
	return false, nil
}

// dockerErrorText is what docker said about a failed call: its stderr when
// the process ran and wrote any (exec's Output captures it). Otherwise — a
// silent non-zero exit, no binary — the error prefixed with the command that
// failed, so "exit status 1" never arrives on its own as a reason.
func dockerErrorText(err error, command string) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(bytes.TrimSpace(exitErr.Stderr)) > 0 {
		return strings.TrimSpace(string(exitErr.Stderr))
	}
	return command + " failed: " + err.Error()
}

// isNoSuchNetwork matches only docker's own "network is missing" answers, for
// this network name, case-insensitively: current clients print "Error: No
// such network: <name>", older daemons "Error response from daemon: network
// <name> not found". Anything looser would read an unrelated failure that
// happens to say "network" and "not found" (network is unreachable, docker
// binary not found) as "missing" and quietly fall back to the bridge.
func isNoSuchNetwork(text, name string) bool {
	t, n := strings.ToLower(text), strings.ToLower(name)
	return strings.Contains(t, "no such network") ||
		strings.Contains(t, "network "+n+" not found")
}

// loopbackURL parses raw and reports whether its host is this machine's
// loopback interface — the one predicate every rewrite below shares. The host
// is what net/url resolves, so userinfo ("localhost@evil"), a fragment or a
// query containing "@localhost" cannot pass for it.
func loopbackURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, false
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return u, true
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return u, true // all of 127.0.0.0/8, and ::1
	}
	return u, false
}

// sandboxNetworkEnv rewrites a session env for a container on the sandbox
// network, where the host's localhost is unreachable (a container's localhost
// is itself):
//
// Only loopback URLs are touched, each variable on its own: a remote URL is
// reachable by egress from the default bridge and the network alike.
//
//   - BLERG_BOARD_URL pointing at loopback becomes the board's service URL
//     (path kept).
//   - BLERG_RUNNER_BOARD_GATEWAY_URL pointing at loopback becomes the
//     runner's service URL (path kept): the gateway is on the runner's port.
//   - When the daemon's own server URL (BLERG_RUNNER_SERVER_URL) is loopback —
//     the daemon talks to the local desktop stack — it is dropped (a host-side
//     websocket URL, meaningless inside) and BLERG_RUNNER_SERVER_HTTP is set to
//     the runner's service URL, which the messaging CLI prefers over every
//     other source. A remote server URL is forwarded as-is and SERVER_HTTP is
//     left alone: the CLI derives the right https URL from it.
func sandboxNetworkEnv(env []string) []string {
	localServer := false
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "BLERG_RUNNER_SERVER_URL="); ok {
			_, loop := loopbackURL(v)
			localServer = localServer || loop
		}
		if v, ok := strings.CutPrefix(kv, "BLERG_RUNNER_SERVER_HTTP="); ok {
			_, loop := loopbackURL(v)
			localServer = localServer || loop
		}
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		switch {
		case localServer && (strings.HasPrefix(kv, "BLERG_RUNNER_SERVER_URL=") ||
			strings.HasPrefix(kv, "BLERG_RUNNER_SERVER_HTTP=")):
			continue
		case strings.HasPrefix(kv, "BLERG_BOARD_URL="):
			if u, loop := loopbackURL(strings.TrimPrefix(kv, "BLERG_BOARD_URL=")); loop {
				kv = "BLERG_BOARD_URL=" + sandboxBoardURL + u.EscapedPath()
				if u.RawQuery != "" {
					kv += "?" + u.RawQuery
				}
			}
		case strings.HasPrefix(kv, "BLERG_RUNNER_BOARD_GATEWAY_URL="):
			// The selected board's gateway address, dialled through the server
			// (MCPGatewayConfig.ViaServer): the runner's own port on the host.
			if u, loop := loopbackURL(strings.TrimPrefix(kv, "BLERG_RUNNER_BOARD_GATEWAY_URL=")); loop {
				kv = "BLERG_RUNNER_BOARD_GATEWAY_URL=" + sandboxRunnerURL + u.EscapedPath()
			}
		}
		out = append(out, kv)
	}
	if localServer {
		out = append(out, "BLERG_RUNNER_SERVER_HTTP="+sandboxRunnerURL)
	}
	return out
}

// cliMountArg is the --mount value for the messaging CLI. --mount rather than
// -v because -v splits on ':' and a repo path may contain one; --mount is a
// CSV list, so a field holding a comma or quote is CSV-quoted the way docker's
// parser reads it.
func cliMountArg(hostPath string) string {
	src := "src=" + hostPath
	if strings.ContainsAny(hostPath, `,"`) {
		src = `"` + strings.ReplaceAll(src, `"`, `""`) + `"`
	}
	return "type=bind," + src + ",dst=" + sandboxCLIMountPath + ",readonly"
}

// pluginCacheMountArg is the --mount value for the plugin cache: read-only,
// so a plugin that writes into its own directory fails loudly rather than
// changing what the next session loads. Quoted like cliMountArg.
func pluginCacheMountArg(hostPath string) string {
	src := "src=" + hostPath
	if strings.ContainsAny(hostPath, `,"`) {
		src = `"` + strings.ReplaceAll(src, `"`, `""`) + `"`
	}
	return "type=bind," + src + ",dst=" + sandboxPluginPath + ",readonly"
}

// codexHomeMountArg mounts native state at its original absolute path, so private home
// symlinks and saved rollout paths mean the same thing on the host and in the container.
func codexHomeMountArg(path string) string {
	field := func(key string) string {
		value := key + "=" + path
		if strings.ContainsAny(path, `,"`) {
			return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
		}
		return value
	}
	return "type=bind," + field("src") + "," + field("dst")
}

// sandboxTestLabels are `--label` values a test puts on every container it starts, so it removes
// only its own containers (by label, never by name prefix). Always empty in the daemon.
var sandboxTestLabels []string

// sandboxTestMounts are extra `--mount` values a test gives every container it starts (a fake engine
// over the image's own). Always empty in the daemon.
var sandboxTestMounts []string

// sandboxRunExtras are the per-start additions to `docker run` that depend on
// the host's state at spawn time rather than on the session.
type sandboxRunExtras struct {
	// cloneObjects is the read-only mount of a sandbox clone's borrowed objects (sandboxclone.go); "" = none.
	cloneObjects string
	network      string // --network <name>; "" = default bridge
	cliPath      string // host path of the messaging CLI to mount; "" = none
	// engine is the session's engine: only its login and configuration are mounted.
	engine           string
	restrictedEngine string
	// pluginCache is the daemon's plugin cache to mount read-only; "" = none.
	pluginCache string
	codexHome   string
	// codexSourceHome is the person's Codex directory a private CODEX_HOME links into: the session's
	// own Codex copy is mounted at that path too, never the person's directory.
	codexSourceHome string
	// sandboxHome is the session's home (prepareSandboxHome); "" = no engine login at all.
	sandboxHome string
	// noProxy is the NO_PROXY / no_proxy value of a container on the sandbox network; "" =
	// leave both to Docker. See sandboxNoProxy.
	noProxy string
	// owner is this daemon's sandboxOwner, set as the sandboxOwnerLabel; "" = no label.
	owner string
}

// sandboxRunArgs is the `docker run` argument list for a session sandbox:
// pure, so the flags are testable without Docker. home is the host home dir
// whose engine configuration is mounted read-only over the session's own
// sandbox home (extras.sandboxHome, see sandboxhome.go); env is
// the session env, of which only BLERG_RUNNER_* entries are forwarded
// (sandboxEnvArgs — never the master token, which Task 3 already strips;
// asserted again here as defence in depth).
//
// extraEnvArgs are additional `-e KEY=VAL` pairs the caller vouches for,
// outside that allowlist. The one user today is a sandboxed agent-kind
// session's Claude credential (see sandboxClaudeCredentialEnv): the engine
// runs *inside* the container, so a credential the host-side driver would
// have read from its own environment has to travel with it. That is the same
// exposure as the login copy in the session's sandbox home, and it is never the daemon
// master token — no caller may pass one (assertSandboxRun pins that).
//
// extras adds the sandbox network (--network) and the read-only messaging CLI
// mount; the env rewrite that goes with the network is the caller's
// (startSandboxContainer), since env arrives here already final.
func sandboxRunArgs(container, hostProjectPath, home string, env []string, extras sandboxRunExtras, extraEnvArgs ...string) []string {
	args := []string{
		"run", "-d", "--name", container,
		// Never an in-band pull under the bounded run: the preflight requires the image to be
		// local, and a missing one fails at once ("No such image") instead.
		"--pull=never",
		// Hardening (desktop-security I7): the container is a containment
		// boundary for the engine's shell, so it gets no capabilities, no
		// setuid escalation, and a process/memory ceiling. No --restart: a
		// sandbox that outlives its session record is recoverSession's job to
		// bring back, not Docker's.
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		// Reap orphaned tool/test descendants: sleep as PID 1 never waits for
		// them, so zombies otherwise consume the process ceiling permanently.
		"--init",
		"--pids-limit", "512",
		"--memory", "4g",
		"--mount", sandboxBindMount(hostProjectPath, sandboxWorkdir),
		"-w", sandboxWorkdir,
	}
	if extras.cloneObjects != "" {
		args = append(args, "--mount", extras.cloneObjects)
	}
	if extras.network != "" {
		args = append(args, "--network", extras.network)
	}
	if extras.noProxy != "" {
		// Explicit `-e` pairs, both spellings, on purpose. The docker client adds the proxy
		// variables of ~/.docker/config.json to every container it runs, but only the exact
		// keys that no `-e` already names (docker/cli ParseProxyConfig). Neither an
		// --env-file entry nor the BLERG_RUNNER_* allowlist above would stop that, and the
		// injected NO_PROXY lacks the sandbox's service names, so the messaging CLI would
		// send http://blerg-runner:8080 to the corporate proxy.
		args = append(args, "-e", "NO_PROXY="+extras.noProxy, "-e", "no_proxy="+extras.noProxy)
	}
	if extras.cliPath != "" {
		// Read-only: the session runs the CLI, it has no business editing the
		// daemon's own copy of it.
		args = append(args, "--mount", cliMountArg(extras.cliPath))
	}
	if extras.pluginCache != "" {
		args = append(args, "--mount", pluginCacheMountArg(extras.pluginCache))
	}
	if extras.codexHome != "" && filepath.IsAbs(extras.codexHome) {
		args = append(args, "--mount", codexHomeMountArg(extras.codexHome))
	}

	if home != "" {
		// The engines' logins and configuration: a home of the session's own (sandboxhome.go),
		// never the person's ~/.claude, ~/.claude.json, ~/.codex or ~/.hermes. Their configuration
		// is mounted read-only; only the session's own copies are writable.
		codexAt := ""
		if extras.restrictedEngine == "" {
			codexAt = extras.codexSourceHome
		}
		codexSource := engineHome(home, "CODEX_HOME", ".codex")
		args = append(args, sandboxHomeMountArgs(extras.sandboxHome, home, codexSource, codexAt,
			sandboxEnginesFor(extras.engine, extras.restrictedEngine))...)
	}
	// Only BLERG_RUNNER_* and BLERG_BOARD_* are forwarded (see sandboxEnvArgs):
	// the host's HOME/PATH would override the image's own, and the master token
	// must never enter a session (Task 3 strips it from env already; this is the
	// second lock on that door).
	if extras.owner != "" {
		args = append(args, "--label", sandboxOwnerLabel+"="+extras.owner)
	}
	for _, l := range sandboxTestLabels {
		args = append(args, "--label", l)
	}
	for _, m := range sandboxTestMounts {
		args = append(args, "--mount", m)
	}
	args = append(args, sandboxEnvArgs(env)...)
	args = append(args, extraEnvArgs...)
	return append(args, sandboxImage(), "sleep", "infinity")
}

// createDockerSandbox starts a throwaway container for sessionID — only
// hostProjectPath bind-mounted in (at sandboxWorkdir), plus whichever
// engine's account state is present on the host — then creates a persistent
// tmux session inside it running inner. Mirrors createTmuxSession's tmux
// setup, just routed through `docker exec` instead of running directly on
// the host.
//
// The engines' logins come from the session's own sandbox home (sandboxhome.go), never from the
// person's engine directories: Claude Code's login check needs both its credentials file and the
// account state in ~/.claude.json (a credentials file alone runs the first-run OAuth wizard, which
// cannot complete in a container), so the home holds a copy of each; the person's configuration is
// mounted read-only over it. ~/.hermes keeps the image's own hermes-agent install: only its
// config.yaml and .env are mounted, read-only.
func createDockerSandbox(sessionID, hostProjectPath string, inner, env []string, opts SandboxOptions) error {
	// No engine credential to pass: a terminal sandbox runs the engine's CLI
	// interactively against the mounted login directories.
	if _, err := startSandboxContainer(sessionID, hostProjectPath, env, sandboxCredential{}, opts); err != nil {
		return err
	}

	// Same tmux setup as createTmuxSession (history limit, hidden status bar,
	// destroy-unattached off, mouse on) — see its comments for why each
	// matters — just run inside the container via runTmuxFor.
	_ = runTmuxFor(sessionID, "set-option", "-g", "history-limit", tmuxHistoryLimit).Run()

	name := tmuxSessionName(sessionID)
	newArgs := append([]string{"new-session", "-d", "-s", name, "-c", sandboxWorkdir, "--"}, inner...)
	if err := runTmuxFor(sessionID, newArgs...).Run(); err != nil {
		return err
	}
	_ = runTmuxFor(sessionID, "set-option", "-t", name, "status", "off").Run()
	_ = runTmuxFor(sessionID, "set-option", "-t", name, "destroy-unattached", "off").Run()
	_ = runTmuxFor(sessionID, "set-option", "-t", name, "mouse", "on").Run()
	return nil
}

// startSandboxContainer starts the hardened container for sessionID and
// registers it, without creating anything inside it. It is the half of
// createDockerSandbox that does not assume tmux: an agent-kind sandbox session
// has no pane, it runs its engine through `docker exec` (see sandboxExec), so
// it needs the container and nothing else. Returns the container name.
// cred is the engine credential the container needs, as `-e NAME` args plus
// whatever the docker client's own environment has to carry for them (see
// sandboxCredential) — the zero value means "nothing to pass".
//
// opts decides the network and the CLI mount. The network is checked here, at
// every start, not once at daemon start: the compose stack that creates it can
// come up (or be upgraded to declare it) after the daemon did.
func startSandboxContainer(sessionID, hostProjectPath string, env []string, cred sandboxCredential, opts SandboxOptions) (string, error) {
	// Only this session's own clone is ever mounted: never the person's checkout or its .git.
	if err := sandboxMountRefusal(hostProjectPath, sessionID); err != nil {
		return "", err
	}
	// The clone borrows the repository's objects: they are mounted read-only, from the daemon's
	// own record of the clone (never from the clone's session-writable alternates file).
	var cloneObjects string
	if isSandboxClone(hostProjectPath, sessionID) {
		var err error
		if cloneObjects, err = sandboxCloneMount(sessionID); err != nil {
			return "", err
		}
	}
	container := dockerContainerName(sessionID)
	// A container from an earlier life of this session (a daemon restart that
	// left one behind, or a resumed session) still owns the name, and `docker
	// run --name` would fail on the collision — with a message that says
	// nothing about the real cause. Clear it first: the old container belongs
	// to a session that is being recreated right now either way.
	forceRemoveContainer(container)
	home, _ := os.UserHomeDir()
	engines := sandboxEnginesFor(opts.Engine, opts.RestrictedEngine)
	sbHome := ""
	if home != "" {
		markSandboxHomeStarting(sessionID)
		defer clearSandboxHomeStarting(sessionID) // registered below, or failed
		var err error
		if sbHome, err = prepareSandboxHome(sessionID, home, engineHome(home, "CODEX_HOME", ".codex"), engines); err != nil {
			return "", err
		}
		ensureSandboxLoginRefresh()
	}
	extras := sandboxRunExtras{owner: sandboxOwner(), sandboxHome: sbHome, cloneObjects: cloneObjects, cliPath: messagingCLIPath(opts.RepoRoot), engine: opts.Engine, restrictedEngine: opts.RestrictedEngine, pluginCache: opts.PluginCache, codexHome: opts.CodexHome, codexSourceHome: opts.CodexSourceHome}
	if opts.Network != "" {
		exists, err := sandboxNetworkState(opts.Network)
		if err != nil {
			// Nothing registered or started yet: the caller surfaces this like
			// any other docker failure.
			return "", err
		}
		if exists {
			extras.network = opts.Network
			extras.noProxy = sandboxNoProxy(os.Getenv, home)
			env = sandboxNetworkEnv(env)
		}
	}
	// Registered BEFORE the run, not after: the orphan sweep works from
	// container names and treats an unregistered one as nobody's. Registering
	// afterwards leaves a window — however short — in which a concurrent
	// reconnect could `docker rm -f` the container this spawn just created.
	// Claiming the name first closes it; the failure path below gives it back.
	registerSandbox(sessionID, container)
	forgetSandboxDeath(container) // a new container under the name: its stop is not reported yet
	dockerContext := currentDockerContext()
	args := sandboxRunArgs(container, hostProjectPath, home, env, extras, cred.args...)
	// Put the label before the image, and run on exactly the context recorded in it.
	args = slices.Insert(args, 4, "--label", sandboxContextLabel+"="+dockerContext)
	pinSandboxContext(container, dockerContext)
	if err := dockerRunEnv(cred.clientEnv, pinnedDockerArgs(container, args...)...); err != nil {
		// A half-created container (run failed after the name was taken) would
		// otherwise linger and block the next attempt.
		forceRemoveContainer(container)
		unregisterSandbox(sessionID)
		forgetSandboxDeath(container)
		return "", err
	}
	// Every later look at it goes to the Docker it was started on.
	pinSandboxContext(container, dockerContext)
	// Before anything runs in it: a session given a fresh token earlier keeps it.
	copyTokenIntoSandbox(container, sessionID)
	return container, nil
}

// forceRemoveContainer removes a container by name, ignoring "no such
// container" — the only failure mode that matters here is "it still exists".
func forceRemoveContainer(container string) {
	_ = dockerRun("rm", "-f", container)
}

// removeSandboxContainer tears a session's container down and unregisters it —
// the agent-kind equivalent of the docker cleanup killTmuxSession does for a
// terminal sandbox.
//
// A registry miss is not "no container": the registry is in-memory and starts
// empty on every daemon restart, while the container from the previous life is
// still running under the same deterministic name. Falling back to that name
// is what keeps a stop/kill after a restart from leaving the container behind
// forever (nothing else would ever remove it — the session is gone, so it will
// never be recreated and never clear its own name).
//
// Accepted cost: after a restart the daemon cannot tell a sandboxed session
// from a host one, so a kill for a session that never had a container also
// spends one `docker rm -f` on a name that does not exist. That is a fork and
// an ignored "No such container", against the alternative of leaking a whole
// container — and only for sessions that outlived a daemon restart.
//
// The session's sandbox home goes with it, after a last sync of a login it refreshed.
func removeSandboxContainer(sessionID string) {
	defer releaseSandboxHome(sessionID)
	container, ok := sandboxContainer(sessionID)
	if !ok {
		container = dockerContainerName(sessionID)
		forceRemoveContainer(container)
		forgetSandboxDeath(container)
		return
	}
	forceRemoveContainer(container)
	forgetSandboxDeath(container)
	unregisterSandbox(sessionID)
}

// sweepOrphanSandboxContainers force-removes every sandbox container this
// daemon can prove nobody is using: not in the registry, not a session the
// daemon is managing, and with no tmux server inside it.
//
// It exists for the agent-kind sandbox. A terminal sandbox is rediscovered
// after a restart by listSandboxedTmuxSessions and reattached; an agent one
// deliberately is not (it holds no tmux, and its engine subprocess died with
// the old daemon), so without this its container would keep its CPU, memory
// and mounts until someone removed it by hand. The tmux probe is therefore the
// safety catch: a container that could still be reattached is never swept.
//
// isLive reports whether the daemon is managing a session id (nil = none are).
//
// Only a container this daemon can tie to itself is a candidate (listOwnSandboxContainers): never
// one that merely has the name prefix.
//
// tied adds the sessions the daemon has another record of (nil = none).
func sweepOrphanSandboxContainers(isLive, tied func(sessionID string) bool) {
	for _, name := range listOwnSandboxContainers(tied) {
		id := strings.TrimPrefix(name, sandboxContainerPrefix)
		if _, registered := sandboxContainer(id); registered {
			continue
		}
		if isLive != nil && isLive(id) {
			continue
		}
		// Last: it costs a `docker exec` per candidate, and the two cheap
		// checks above have already excused every live session.
		if has, known := tmuxProbe(name); has || !known {
			continue
		}
		log.Printf("sandbox: removing orphaned container %s (no live session)", name)
		// Its clone's leftover work is committed in it and brought back first.
		finishSandbox(id)
	}
}

// containerWorkspaceSource is the host folder a container has mounted at /workspace; ok is false
// when docker cannot say.
var containerWorkspaceSource = func(container string) (string, bool) {
	out, err := dockerOutput("inspect", "--format", `{{range .Mounts}}{{if eq .Destination "`+sandboxWorkdir+`"}}{{.Source}}{{end}}{{end}}`, container)
	if err != nil {
		return "", false
	}
	source := strings.TrimSpace(string(out))
	return source, source != "" && filepath.IsAbs(source)
}

// listSandboxContainers returns the names of the running containers this
// daemon's sandboxes use. An error (no docker, daemon down) is an empty list:
// every caller's job is cleanup or rediscovery, neither of which can proceed.
func listSandboxContainers() []string {
	out, err := dockerOutput("ps", "--filter", "name=^/"+sandboxContainerPrefix,
		"--format", "{{.Names}}")
	if err != nil {
		return nil
	}
	names := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, sandboxContainerPrefix) {
			names = append(names, name)
		}
	}
	return names
}

// listOwnSandboxContainers returns the running sandbox containers this daemon can tie to itself:
// those carrying its owner label, and, for a container from before the label (no owner label at
// all), those of a session it has a record of: registered, a sandbox clone record or a sandbox home
// in its state directory, or tied (nil = no other records). A container another daemon labeled, or
// an unlabeled one this daemon knows nothing of, is left alone, whatever its name: it is someone
// else's session (or a test's) that happens to share the prefix. An error is an empty list.
func listOwnSandboxContainers(tied func(sessionID string) bool) []string {
	out, err := dockerOutput("ps", "--filter", "name=^/"+sandboxContainerPrefix,
		"--format", "{{.Names}}\t{{.Label \""+sandboxOwnerLabel+"\"}}\t{{.Label \""+sandboxContextLabel+"\"}}")
	if err != nil {
		return nil
	}
	own := sandboxOwner()
	names := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, labels, _ := strings.Cut(strings.TrimSpace(line), "\t")
		owner, dockerContext, _ := strings.Cut(labels, "\t")
		name, owner = strings.TrimSpace(name), strings.TrimSpace(owner)
		if !strings.HasPrefix(name, sandboxContainerPrefix) {
			continue
		}
		if sandboxContainerIsOurs(strings.TrimPrefix(name, sandboxContainerPrefix), owner, own, tied) {
			restoreSandboxContext(name, strings.TrimSpace(dockerContext))
			names = append(names, name)
			continue
		}
		if _, seen := sandboxForeignLogged.LoadOrStore(name, struct{}{}); !seen {
			log.Printf("sandbox: container %s is not this daemon's (owner %q); left alone", name, owner)
		}
	}
	return names
}

// sandboxForeignLogged holds the containers already logged as not this daemon's: each is said once
// per daemon run, not at every reconnect.
var sandboxForeignLogged sync.Map

// sandboxContainerIsOurs is listOwnSandboxContainers' test for one container.
func sandboxContainerIsOurs(sessionID, owner, own string, tied func(string) bool) bool {
	if owner != "" {
		return own != "" && owner == own
	}
	if _, registered := sandboxContainer(sessionID); registered {
		return true
	}
	if !sandboxCloneIDPattern.MatchString(sessionID) {
		return false
	}
	if _, record, ok := sandboxClonePaths(sessionID); ok {
		if _, err := os.Lstat(record); err == nil {
			return true
		}
	}
	if dir, ok := sandboxHomePath(sessionID); ok {
		if _, err := os.Lstat(dir); err == nil {
			return true
		}
	}
	return tied != nil && tied(sessionID)
}

// sandboxExec is the argv prefix that runs a command inside a session's
// sandbox container instead of on the host. An empty value means "run on the
// host" — the unsandboxed default, so a driver holding one needs no branching
// beyond calling command().
type sandboxExec []string

// sandboxExecPrefix builds the prefix for container: `docker exec -i -w
// /workspace <container>`. -i because the engines read nothing from stdin but a
// closed stdin trips some CLIs; -w so the engine's cwd is the in-container
// mount point, never the host's real path. No `-e` here: the container's
// environment was set once at `docker run` (sandboxRunArgs), and every process
// started inside it inherits that — repeating the allowlist per exec would be
// two places to keep in sync for no gain.
func sandboxExecPrefix(container string) sandboxExec {
	return sandboxExec{"docker", "exec", "-i", "-w", sandboxWorkdir, container}
}

func (p sandboxExec) enabled() bool { return len(p) > 0 }

// container is the container name the prefix targets (its last element).
func (p sandboxExec) container() string {
	if !p.enabled() {
		return ""
	}
	return p[len(p)-1]
}

// sandboxTurnPIDFile is where an in-container turn records its own PID so
// Interrupt can signal it. One turn runs at a time per session (each driver
// serialises its queue) and each session has its own container, so a single
// fixed path is unambiguous. A var, not a const, so tests can point it at a
// scratch file instead of a real one — the path names a target for `kill`,
// which no test may aim at a live host process.
var sandboxTurnPIDFile = "/tmp/blerg-turn.pid"

// command builds the exec.Cmd for an engine invocation: bare on the host with
// workDir as its cwd, or wrapped in `docker exec` (whose -w already fixes the
// cwd inside the container, so Dir stays unset — the host path means nothing
// there).
//
// The in-container form runs the engine under a one-line `sh -c` that records
// the PID and then `exec`s the engine into that same process. Killing the host
// side of a `docker exec` does NOT signal the process inside the container, so
// without this a user's Interrupt would leave the engine running to completion;
// interrupt() signals the recorded PID instead.
func (p sandboxExec) command(ctx context.Context, workDir, bin string, args ...string) *exec.Cmd {
	return p.commandPID(ctx, workDir, bin, sandboxTurnPIDFile, args...)
}

// commandPID is command with the in-container PID file named by the caller: a long-lived engine process
// has its own file, so a later process's start or end can never remove or signal another's.
func (p sandboxExec) commandPID(ctx context.Context, workDir, bin, pidFile string, args ...string) *exec.Cmd {
	if !p.enabled() {
		cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // bin is a resolved engine binary and args the engine's own argv; no shell
		cmd.Dir = workDir
		return cmd
	}
	inner := append([]string{
		"sh", "-c", "echo $$ > " + pidFile + `; exec "$@"`, "sh", bin,
	}, args...)
	full := append(append([]string{}, p[1:]...), inner...)
	return exec.CommandContext(ctx, p[0], full...) //nolint:gosec // p[0] is the daemon-built docker prefix; engine argv follows as separate elements, run via exec "$@" so no interpolation
}

// interrupt signals the in-container turn (SIGTERM to the PID command()
// recorded), and reports whether that succeeded. A failure — no turn running,
// container gone, docker unavailable — leaves the caller to fall back to
// killing the host-side `docker exec` client.
func (p sandboxExec) interrupt() error { return p.interruptPID(sandboxTurnPIDFile) }

// interruptPID is interrupt for the PID file of one process.
func (p sandboxExec) interruptPID(pidFile string) error {
	if !p.enabled() {
		return errors.New("not sandboxed")
	}
	return dockerRun("exec", p.container(), "sh", "-c",
		"kill -TERM $(cat "+pidFile+")")
}

// killPID force-kills (SIGKILL) the in-container process whose PID file is pidFile. For a process that must
// go away even if it ignores SIGTERM (a wedged engine); interruptPID's SIGTERM is for a polite interrupt.
func (p sandboxExec) killPID(pidFile string) error {
	if !p.enabled() {
		return errors.New("not sandboxed")
	}
	return dockerRun("exec", p.container(), "sh", "-c", "kill -KILL $(cat "+pidFile+")")
}

// clearTurnPID deletes the PID file a finished turn left behind, so a later
// Interrupt cannot signal a PID the container has since recycled. Best-effort:
// a turn that never started wrote none.
func (p sandboxExec) clearTurnPID() { p.clearPID(sandboxTurnPIDFile) }

// clearPID deletes one process's PID file.
func (p sandboxExec) clearPID(pidFile string) {
	if !p.enabled() {
		return
	}
	_ = dockerRun("exec", p.container(), "rm", "-f", pidFile)
}

// interruptTurn is every driver's Interrupt: signal the process inside the
// container when the turn runs there, and kill the host-side child otherwise
// (or when the in-container signal could not be delivered — the client is
// still worth killing so the driver's Wait returns).
func interruptTurn(p sandboxExec, cmd *exec.Cmd) { interruptTurnPID(p, cmd, sandboxTurnPIDFile) }

// interruptTurnPID is interruptTurn for the process whose in-container PID file is pidFile.
func interruptTurnPID(p sandboxExec, cmd *exec.Cmd, pidFile string) {
	if p.enabled() && p.interruptPID(pidFile) == nil {
		return
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// engineInSandboxImage reports whether an engine binary exists inside the
// sandbox image — what preflight checks for a sandboxed agent-kind spawn,
// since the host PATH says nothing about what the container can run.
func engineInSandboxImage(bin string) error {
	args := []string{"run", "--rm", "--pull=never"}
	for _, l := range sandboxTestLabels {
		args = append(args, "--label", l)
	}
	return dockerRun(append(args, sandboxImage(), "which", bin)...)
}

// sandboxImageAvailable reports whether the sandbox image is present on this host,
// so the daemon can tell the server (which relays it to the launch UI)
// whether "Local sandbox" will actually work here right now — reported at
// connect and refreshed on every heartbeat, so a manually-removed image is
// reflected without waiting for a reconnect.
func sandboxImageAvailable() bool {
	return dockerRun("image", "inspect", sandboxImage()) == nil
}

// listSandboxedTmuxSessions returns the session IDs of all currently-running
// sandbox containers, re-registering each in sandboxRegistry as a side
// effect — the registry is in-memory only and starts empty on every daemon
// restart, so this is how a live sandboxed session gets rediscovered (the
// bare-host equivalent, listBlergRunnerTmuxSessions, only sees host tmux).
//
// Only containers that actually hold a tmux server count: an agent-kind
// sandbox container has none (its engine runs as a plain `docker exec`
// subprocess), and reporting one here would have the reconnect pass try to
// reattach a PTY to a tmux session that does not exist — the session would be
// "recovered" into something unusable instead of being respawned by the
// server.
//
// Only this daemon's own containers are listed (listOwnSandboxContainers; tied adds the sessions
// it has a record of): the reattach pass registers, reattaches or stops what it lists.
func listSandboxedTmuxSessions(tied func(sessionID string) bool) []string {
	ids := []string{}
	for _, name := range listOwnSandboxContainers(tied) {
		if has, known := tmuxProbe(name); !has || !known {
			continue
		}
		id := strings.TrimPrefix(name, sandboxContainerPrefix)
		registerSandbox(id, name)
		ids = append(ids, id)
	}
	return ids
}

// tmuxProbe reports whether a sandbox container runs a tmux server (it hosts a terminal session,
// not an agent one). known is false when the probe did not answer within sandboxProbeTimeout: the
// tmux in a container is the session's own, and one that never answers must not hold the
// reattach pass, the orphan sweep or a session's end. Callers leave such a container alone.
func tmuxProbe(container string) (has, known bool) {
	timedOut, err := dockerProbe(sandboxProbeTimeout, "exec", container, "tmux", "ls")
	if timedOut {
		log.Printf("sandbox: container %s did not answer the tmux probe within %s; left alone this pass", container, sandboxProbeTimeout)
		return false, false
	}
	return err == nil, true
}

// dockerProbe runs `docker <args>` bounded by timeout and says whether it timed out. A var so
// tests can stub it.
var dockerProbe = func(timeout time.Duration, args ...string) (timedOut bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // literal docker binary, daemon-built argv
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	return ctx.Err() != nil, err
}
