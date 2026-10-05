package daemon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// dockerRun runs `docker <args>` and reports only whether it succeeded — the
// shape every sandbox lifecycle call (run/rm, and the in-image engine check)
// needs. A package var so tests can record invocations, or stub them, without
// a Docker daemon.
var dockerRun = func(args ...string) error { return exec.Command("docker", args...).Run() } //nolint:gosec // literal docker binary, args assembled by the daemon (no shell)

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
	cmd := exec.Command("docker", args...) //nolint:gosec // literal docker binary, argv assembled by the daemon (no shell); container name is generated, remote and image come from validated config
	cmd.Env = append(os.Environ(), env...)
	return cmd.Run()
}

// dockerOutput is dockerRun's sibling for the calls whose stdout matters
// (`docker ps`). Same reason for being a var: tests stub it.
var dockerOutput = func(args ...string) ([]byte, error) {
	return exec.Command("docker", args...).Output() //nolint:gosec // literal docker binary, args assembled by the daemon (no shell)
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
		return exec.Command("docker", full...) //nolint:gosec,noctx // literal docker binary, `exec <container> tmux ...` argv assembled by the daemon (no shell); noctx: short-lived tmux helper invoked from synchronous daemon code without a context
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
	// ClaudeOnly is set per session (never from configuration) for a restricted session: only the
	// claude login is mounted, not codex's or hermes's.
	ClaudeOnly bool
	// PluginCache is set per session when it loads always-on plugins: the
	// session's plugin snapshot (pluginworkshop.go), bind-mounted read-only at
	// sandboxPluginPath. "" = no mount.
	PluginCache string
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
func dockerErrorText(err error, command string) string { //nolint:unparam // the command names the failing call in the message; one caller today, the helper is not specific to it
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
//   - When the daemon's own server URL (BLERG_RUNNER_SERVER_URL) is loopback —
//     the daemon talks to the local desktop stack — it is dropped (a host-side
//     websocket URL, meaningless inside) and BLERG_RUNNER_SERVER_HTTP is set to
//     the runner's service URL, which the messaging CLI prefers over every
//     other source. A remote server URL is forwarded as-is and SERVER_HTTP is
//     left alone: the CLI derives the right https URL from it.
//   - BLERG_RUNNER_PREVIEW_URL pointing at loopback is dropped: unreachable,
//     and the CLI never gets to it once SERVER_HTTP is set.
func sandboxNetworkEnv(env []string) []string {
	localServer := false
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "BLERG_RUNNER_SERVER_URL="); ok {
			_, localServer = loopbackURL(v)
		}
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		switch {
		case localServer && (strings.HasPrefix(kv, "BLERG_RUNNER_SERVER_URL=") ||
			strings.HasPrefix(kv, "BLERG_RUNNER_SERVER_HTTP=")):
			continue
		case strings.HasPrefix(kv, "BLERG_RUNNER_PREVIEW_URL="):
			if _, loop := loopbackURL(strings.TrimPrefix(kv, "BLERG_RUNNER_PREVIEW_URL=")); loop {
				continue
			}
		case strings.HasPrefix(kv, "BLERG_BOARD_URL="):
			if u, loop := loopbackURL(strings.TrimPrefix(kv, "BLERG_BOARD_URL=")); loop {
				kv = "BLERG_BOARD_URL=" + sandboxBoardURL + u.EscapedPath()
				if u.RawQuery != "" {
					kv += "?" + u.RawQuery
				}
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

// sandboxRunExtras are the per-start additions to `docker run` that depend on
// the host's state at spawn time rather than on the session.
type sandboxRunExtras struct {
	network string // --network <name>; "" = default bridge
	cliPath string // host path of the messaging CLI to mount; "" = none
	// claudeOnly mounts only the claude login (a restricted session runs no other engine).
	claudeOnly bool
	// pluginCache is the daemon's plugin cache to mount read-only; "" = none.
	pluginCache string
}

// sandboxRunArgs is the `docker run` argument list for a session sandbox:
// pure, so the flags are testable without Docker. home is the host home dir
// whose engine config dirs are mounted (only the ones that exist); env is
// the session env, of which only BLERG_RUNNER_* entries are forwarded
// (sandboxEnvArgs — never the master token, which Task 3 already strips;
// asserted again here as defence in depth).
//
// extraEnvArgs are additional `-e KEY=VAL` pairs the caller vouches for,
// outside that allowlist. The one user today is a sandboxed agent-kind
// session's Claude credential (see sandboxClaudeCredentialEnv): the engine
// runs *inside* the container, so a credential the host-side driver would
// have read from its own environment has to travel with it. That is the same
// exposure as the ~/.claude mount right beside it, and it is never the daemon
// master token — no caller may pass one (assertSandboxRun pins that).
//
// extras adds the sandbox network (--network) and the read-only messaging CLI
// mount; the env rewrite that goes with the network is the caller's
// (startSandboxContainer), since env arrives here already final.
func sandboxRunArgs(container, hostProjectPath, home string, env []string, extras sandboxRunExtras, extraEnvArgs ...string) []string {
	args := []string{
		"run", "-d", "--name", container,
		// Hardening (desktop-security I7): the container is a containment
		// boundary for the engine's shell, so it gets no capabilities, no
		// setuid escalation, and a process/memory ceiling. No --restart: a
		// sandbox that outlives its session record is recoverSession's job to
		// bring back, not Docker's.
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "512",
		"--memory", "4g",
		"-v", hostProjectPath + ":" + sandboxWorkdir,
		"-w", sandboxWorkdir,
	}
	if extras.network != "" {
		args = append(args, "--network", extras.network)
	}
	if extras.cliPath != "" {
		// Read-only: the session runs the CLI, it has no business editing the
		// daemon's own copy of it.
		args = append(args, "--mount", cliMountArg(extras.cliPath))
	}
	if extras.pluginCache != "" {
		args = append(args, "--mount", pluginCacheMountArg(extras.pluginCache))
	}
	if home != "" {
		// /home/agent matches the sandbox image's ENV HOME (see
		// runner/sandbox/Dockerfile) — not the host's own $HOME. These are
		// read-write on purpose (the engine needs its login) — which is why the
		// launch sheet and DAEMON.md say the sandbox does NOT protect them.
		logins := []string{".claude", ".claude.json", ".codex", ".hermes/config.yaml", ".hermes/.env"}
		if extras.claudeOnly {
			// A restricted session runs claude only: no other engine's login goes in.
			logins = logins[:2]
		}
		for _, p := range logins {
			if _, err := os.Stat(filepath.Join(home, p)); err == nil {
				args = append(args, "-v", filepath.Join(home, p)+":/home/agent/"+p)
			}
		}
	}
	// Only BLERG_RUNNER_* and BLERG_BOARD_* are forwarded (see sandboxEnvArgs):
	// the host's HOME/PATH would override the image's own, and the master token
	// must never enter a session (Task 3 strips it from env already; this is the
	// second lock on that door).
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
// Both ~/.claude AND ~/.claude.json (a separate top-level dotfile) are
// mounted, read-write. This was empirically found, not assumed: mounting only
// ~/.claude/.credentials.json (even read-only) does NOT satisfy claude's own
// login check — it still runs the full first-run OAuth wizard, which can't
// complete headlessly in a container (it tries to open a browser). Account
// state (which ~/.claude.json holds) turned out to be what actually gates
// that, alongside the credentials file. Read-write because claude writes into
// ~/.claude (session transcripts, settings) as it runs; this is the same
// directory the bare-host daemon already trusts fully, just now also reachable
// from inside a container — no new exposure of anything not already true for
// desktop-daemon sessions. ~/.codex gets the identical treatment for the same
// reason (untested against codex's actual login check specifically, but
// there is no reason to expect the credentials-file-alone shortcut to work
// there either, given the mix of config + operational state under the same
// directory).
//
// ~/.hermes is different: alongside config/credentials it also holds the
// actual hermes-agent code checkout the installer manages ("Code:
// ~/.hermes/hermes-agent" per its own install output), which the sandbox
// image installs its own copy of at build time. Mounting the whole directory
// would silently replace that with whatever's on the host, version mismatch
// and all — so only the two files that actually hold config/credentials
// (config.yaml, .env) are mounted, individually, leaving the image's own
// install untouched.
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
	container := dockerContainerName(sessionID)
	// A container from an earlier life of this session (a daemon restart that
	// left one behind, or a resumed session) still owns the name, and `docker
	// run --name` would fail on the collision — with a message that says
	// nothing about the real cause. Clear it first: the old container belongs
	// to a session that is being recreated right now either way.
	forceRemoveContainer(container)
	home, _ := os.UserHomeDir()
	extras := sandboxRunExtras{cliPath: messagingCLIPath(opts.RepoRoot), claudeOnly: opts.ClaudeOnly, pluginCache: opts.PluginCache}
	if opts.Network != "" {
		exists, err := sandboxNetworkState(opts.Network)
		if err != nil {
			// Nothing registered or started yet: the caller surfaces this like
			// any other docker failure.
			return "", err
		}
		if exists {
			extras.network = opts.Network
			env = sandboxNetworkEnv(env)
		}
	}
	// Registered BEFORE the run, not after: the orphan sweep works from
	// container names and treats an unregistered one as nobody's. Registering
	// afterwards leaves a window — however short — in which a concurrent
	// reconnect could `docker rm -f` the container this spawn just created.
	// Claiming the name first closes it; the failure path below gives it back.
	registerSandbox(sessionID, container)
	if err := dockerRunEnv(cred.clientEnv, sandboxRunArgs(container, hostProjectPath, home, env, extras, cred.args...)...); err != nil {
		// A half-created container (run failed after the name was taken) would
		// otherwise linger and block the next attempt.
		forceRemoveContainer(container)
		unregisterSandbox(sessionID)
		return "", err
	}
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
func removeSandboxContainer(sessionID string) {
	container, ok := sandboxContainer(sessionID)
	if !ok {
		forceRemoveContainer(dockerContainerName(sessionID))
		return
	}
	forceRemoveContainer(container)
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
func sweepOrphanSandboxContainers(isLive func(sessionID string) bool) {
	for _, name := range listSandboxContainers() {
		id := strings.TrimPrefix(name, sandboxContainerPrefix)
		if _, registered := sandboxContainer(id); registered {
			continue
		}
		if isLive != nil && isLive(id) {
			continue
		}
		// Last: it costs a `docker exec` per candidate, and the two cheap
		// checks above have already excused every live session.
		if containerHasTmuxServer(name) {
			continue
		}
		log.Printf("sandbox: removing orphaned container %s (no live session)", name)
		forceRemoveContainer(name)
	}
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
	return dockerRun("run", "--rm", sandboxImage(), "which", bin)
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
func listSandboxedTmuxSessions() []string {
	ids := []string{}
	for _, name := range listSandboxContainers() {
		if !containerHasTmuxServer(name) {
			continue
		}
		id := strings.TrimPrefix(name, sandboxContainerPrefix)
		registerSandbox(id, name)
		ids = append(ids, id)
	}
	return ids
}

// containerHasTmuxServer reports whether a sandbox container runs a tmux
// server (i.e. it hosts a terminal session, not an agent one).
func containerHasTmuxServer(container string) bool {
	return dockerRun("exec", container, "tmux", "ls") == nil
}
