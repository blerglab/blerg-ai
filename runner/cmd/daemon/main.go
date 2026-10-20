package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
)

var version = "dev"

func main() {
	// A subcommand (the installer's question before a reinstall) runs and exits before anything
	// a running daemon does: no provisioning, no records lock, no connection.
	if len(os.Args) > 1 {
		os.Exit(runCommand(os.Args[1:], os.Stdout, os.Stderr))
	}

	// Systemd user services start with a minimal PATH that omits ~/.local/bin,
	// where `claude` is typically installed. Without it, exec.LookPath("claude")
	// fails and every spawned/recovered session execs a non-existent binary,
	// dying instantly. Prepend it so child tmux/claude processes inherit it.
	ensureLocalBinOnPath()

	// Systemd user services also start with no TERM. The session PTY runs
	// `tmux attach`, and a tmux client with an empty TERM aborts with
	// "open terminal failed: terminal does not support clear". The browser
	// renders with xterm.js, so default to xterm-256color.
	if os.Getenv("TERM") == "" {
		if err := os.Setenv("TERM", "xterm-256color"); err != nil {
			log.Printf("set TERM: %v", err)
		}
	}

	// Idempotently provision the blerg-runner CLI symlink and global CLAUDE.md block.
	// Best-effort: errors are logged inside Provision; the daemon always starts.
	// The repo root also feeds the sandbox's messaging-CLI mount ("" = none).
	repoRoot, err := daemon.RepoRootFromExecutable()
	if err != nil {
		log.Printf("blerg-runner-daemon: provision: cannot determine repo root: %v", err)
	} else if homeDir, err := os.UserHomeDir(); err != nil {
		log.Printf("blerg-runner-daemon: provision: cannot determine home dir: %v", err)
	} else {
		daemon.Provision(repoRoot, homeDir, os.Getenv("BLERG_RUNNER_PROVISION_CLAUDE_MD") == "true")
	}

	// BLERG_RUNNER_REPOS_ROOT stays required: it is the first-run default.
	// A root the owner later chose in the app is saved in the daemon's state
	// directory and wins from then on (see daemon.LoadReposRootSetting).
	envReposRoot := requireEnv("BLERG_RUNNER_REPOS_ROOT")
	settingsPath := daemon.DaemonSettingsPath(daemon.DaemonStateDir())
	reposRoot, reposRootSource := daemon.LoadReposRootSetting(settingsPath, envReposRoot)

	cfg := daemon.Config{
		ServerURL:   requireEnv("BLERG_RUNNER_SERVER_URL"),
		DaemonToken: requireEnv("BLERG_RUNNER_DAEMON_TOKEN"),
		DaemonName:  envOr("BLERG_RUNNER_DAEMON_NAME", hostname()),
		DaemonMode:  envOr("BLERG_RUNNER_DAEMON_MODE", "local"),
		ReposRoot:   reposRoot.Get(),
		Version:     version,
		// Only on a daemon nobody else uses: see DAEMON.md "Cloning a
		// repository you name".
		AllowHostCredentialClone: os.Getenv("BLERG_RUNNER_ALLOW_HOST_CREDENTIAL_CLONE") == "true",
	}

	// Recovery records of agent sessions belong to one daemon process at a time: a second
	// daemon run by the same user (a build started by hand) keeps none and recovers nothing.
	agentRecordsDir := daemon.AgentRecordsDir(daemon.DaemonStateDir())
	if !daemon.LockAgentRecords(agentRecordsDir) {
		agentRecordsDir = ""
	}
	// Why the previous run ended (the installer's marker, or its own note of a clean stop), read
	// once by the daemon that holds the records: the sessions it cut off are told.
	stateDir := ""
	if agentRecordsDir != "" {
		stateDir = daemon.DaemonStateDir()
	}
	// The link's state for the installer and `install.sh status` (connection.json), kept by the
	// daemon that holds the state directory: a second one started by hand does not overwrite it.
	cfg.ConnectionFile = daemon.ConnectionFilePath(stateDir)

	client := daemon.NewWSClient(cfg)
	restartCause := daemon.ReadRestartCause(stateDir)

	serverHTTP, fromPreviewURL := serverHTTPSetting(os.Getenv("BLERG_RUNNER_SERVER_HTTP"), os.Getenv("BLERG_RUNNER_PREVIEW_URL"))
	if fromPreviewURL {
		log.Printf("blerg-runner-daemon: BLERG_RUNNER_PREVIEW_URL is deprecated; set BLERG_RUNNER_SERVER_HTTP=%s instead (derived from it for now)", serverHTTP)
	}

	// The providers' usage limits (Codex from its app-server, Claude from its usage endpoint with
	// the CLI's sign-in, and when it refuses a call for its limit), reported per provider: only
	// percentages and reset times leave the machine.
	claudeUsage := claudeUsageSetting(os.Getenv("BLERG_RUNNER_CLAUDE_USAGE"))
	if claudeUsage {
		log.Printf("claude usage meter: on (reads the CLI's usage endpoint with its sign-in; BLERG_RUNNER_CLAUDE_USAGE=0 turns it off)")
	} else {
		log.Printf("claude usage meter: off (BLERG_RUNNER_CLAUDE_USAGE); Claude shows only a cap when it refuses a call")
	}
	limits := daemon.NewUsageLimitsTracker(client.Send, daemon.UsageLimitsOptions{ClaudeUsage: claudeUsage, Version: version})

	mgr := daemon.NewManager(client, daemon.ManagerConfig{
		ReposRoot:        cfg.ReposRoot,
		ReposRootSetting: reposRoot,
		DaemonName:       cfg.DaemonName,
		DaemonToken:      cfg.DaemonToken,
		ServerHTTP:       serverHTTP,
		ServerURL:        cfg.ServerURL,

		SandboxNetwork: sandboxNetworkSetting(),
		RepoRoot:       repoRoot,

		AllowHostCredentialClone: cfg.AllowHostCredentialClone,

		AgentRecordsDir: agentRecordsDir,
		RestartCause:    restartCause,

		UsageLimits: limits,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// First thing on a stop signal, before anything is torn down: no more recovery-record
	// writes. The engines die with the daemon and report their turns over as they go; that must
	// not erase what the next daemon needs to know about the turns that were cut off.
	// Then a note that this run was told to stop rather than crashing (ReadRestartCause): the next
	// run tells the sessions it cut off that nothing they ran stopped it. ctx ends only on the
	// signal (stop runs as main returns).
	go func() {
		<-ctx.Done()
		mgr.FreezeAgentRecords()
		daemon.NoteCleanStop(stateDir)
	}()

	log.Printf("blerg-runner-daemon: starting (name=%s mode=%s repos_root=%s from %s)",
		cfg.DaemonName, cfg.DaemonMode, cfg.ReposRoot, reposRootSource)

	// Probe the engines that can list their models (codex, hermes) now, on
	// every (re)connect and every ~15 min, in the background; the lists ride
	// the hello and — when they change — a heartbeat.
	prober := daemon.NewModelProber()
	go prober.Run(ctx)
	client.SetEngineModels(prober)
	go limits.Run(ctx)

	// Reattach to any persistent tmux sessions left by a prior run on every
	// (re)connect, so Claude sessions survive a daemon restart.
	client.SetOnConnect(func() {
		prober.Kick()
		// The server keeps limits per connection: say them again, and read them now (the first
		// read waits for this, so it never races the hello).
		limits.Resend()
		limits.Kick()
		// The hello says which sessions are alive, not what they are doing: a heartbeat now
		// moves a session the server marked lost straight to its real state.
		client.RequestHeartbeat()
		mgr.ReattachSessions()
	})

	// Report per-session detector state in each heartbeat so the server can
	// self-heal a dropped session_state_changed event.
	client.SetSessionStates(mgr.SessionStates)
	// ...and the spawns still in its queue, so a slow start is not read as a lost session.
	client.SetQueuedSessions(mgr.QueuedSpawnIDs)

	// Claim the agent sessions the previous run of the daemon left behind, before connecting:
	// the hello then lists them and the server keeps their rows while they are hosted again.
	if n := mgr.AdoptAgentSessions(client.RequestHeartbeat); n > 0 {
		log.Printf("blerg-runner-daemon: recovering %d agent session(s) from before the restart; restart %s", n, restartCause)
	}

	client.Run(ctx, mgr.ActiveSessionIDs)

	if ctx.Err() != nil {
		// Again, and before returning: the goroutine above may not have got to it yet.
		daemon.NoteCleanStop(stateDir)
	}
	log.Println("blerg-runner-daemon: stopped")
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

// claudeUsageSetting reads BLERG_RUNNER_CLAUDE_USAGE: the Claude usage meter is on unless it says
// 0, false, off or no.
func claudeUsageSetting(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// sandboxNetworkSetting reads BLERG_RUNNER_SANDBOX_NETWORK: the docker network
// sandbox containers join when it exists. Unset means the desktop compose's
// "blerg-sandbox"; set to the empty string, it disables joining any network
// (so envOr, which treats empty as unset, does not fit).
//
// Docker's special network modes are refused and treated as disabled: `host`
// would give the sandbox the host's network stack (every loopback service),
// `container:<id>` another container's, and `none`/`bridge` are not the
// dedicated network this setting exists to join. A value starting with '-'
// would be read by docker as a flag.
func sandboxNetworkSetting() string {
	v, ok := os.LookupEnv("BLERG_RUNNER_SANDBOX_NETWORK")
	if !ok {
		return "blerg-sandbox"
	}
	lower := strings.ToLower(v)
	switch {
	case lower == "host", lower == "none", lower == "bridge",
		strings.HasPrefix(lower, "container:"), strings.HasPrefix(v, "-"):
		log.Printf("blerg-runner-daemon: BLERG_RUNNER_SANDBOX_NETWORK=%q is not a dedicated network; sandboxes will not join one", v)
		return ""
	}
	return v
}

// serverHTTPSetting is the server's HTTP base the daemon works with:
// BLERG_RUNNER_SERVER_HTTP when it is set. When it is not, an older install may
// still have only the deprecated BLERG_RUNNER_PREVIEW_URL (…/api/preview) in
// its env file; the base is then that URL without the suffix, and derived is
// true so the caller can say so. The same rule as the blerg-runner CLI's own
// fallback, so a session reaches the server it always did.
func serverHTTPSetting(serverHTTP, previewURL string) (base string, derived bool) {
	if serverHTTP != "" {
		return serverHTTP, false
	}
	preview := strings.TrimRight(strings.TrimSpace(previewURL), "/")
	if rest, ok := strings.CutSuffix(preview, "/api/preview"); ok {
		if base = strings.TrimRight(rest, "/"); base != "" {
			return base, true
		}
	}
	return "", false
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ensureLocalBinOnPath prepends $HOME/.local/bin to PATH (if not already there)
// so tools installed there — notably `claude` — resolve when the daemon runs
// under a systemd user service with a minimal inherited PATH.
func ensureLocalBinOnPath() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.Setenv("PATH", prependPath(os.Getenv("PATH"), localBin)); err != nil {
		log.Printf("set PATH: %v", err)
	}
}

// prependPath returns list with dir moved/added to the front, unless dir is
// empty or already present as an exact element. Pure for testability.
func prependPath(list, dir string) string {
	if dir == "" {
		return list
	}
	for _, p := range filepath.SplitList(list) {
		if p == dir {
			return list
		}
	}
	if list == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + list
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

const usage = `Usage:
  blerg-runner-daemon                      run the daemon (its settings come from the environment)
  blerg-runner-daemon status --open-work   list the agent sessions a restart would cut off:
                                           a turn running, background tasks, subagents
                                           (nothing printed: none; run inside an agent
                                           session, that session's own turn is left out)
`

// runCommand runs a subcommand and returns the exit code. An argument it does not know is an
// error rather than ignored: the daemon itself takes none, and must never start by mistake.
func runCommand(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help"):
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	case len(args) == 2 && args[0] == "status" && args[1] == "--open-work":
		// Run from inside an agent session (an agent updating the daemon), that session's own
		// turn is the one asking.
		self := os.Getenv("BLERG_RUNNER_SESSION_ID")
		_, _ = fmt.Fprint(stdout, daemon.OpenWorkReport(daemon.AgentRecordsDir(daemon.DaemonStateDir()), self))
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "blerg-runner-daemon: unknown arguments: %s\n%s", strings.Join(args, " "), usage)
	return 2
}
