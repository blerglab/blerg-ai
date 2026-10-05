// Package runner implements blerg-runner: PID 1 of a per-session dev
// container in the k8s cluster runtime. It hosts exactly one agent session,
// speaking the daemon WS protocol to the server. Workspaces are ephemeral —
// committed work is continuously safety-pushed to wip/<session-id>.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Config is assembled from the pod environment (set by the server's Job
// manifest).
type Config struct {
	ServerWS      string // BLERG_RUNNER_SERVER_WS   e.g. ws://blerg-runner-server:8080/ws/daemon
	ServerHTTP    string // BLERG_RUNNER_SERVER_HTTP e.g. http://blerg-runner-server:8080
	DaemonToken   string // BLERG_RUNNER_DAEMON_TOKEN
	SessionID     string // BLERG_RUNNER_SESSION_ID
	Repo          string // BLERG_RUNNER_REPO
	Title         string // BLERG_RUNNER_TITLE
	Model         string // BLERG_RUNNER_MODEL
	Effort        string // BLERG_RUNNER_EFFORT ("" = the model default)
	Engine        string // BLERG_RUNNER_ENGINE ("" (claude) | "codex")
	InitialPrompt string // BLERG_RUNNER_INITIAL_PROMPT
	GitURL        string // BLERG_RUNNER_GIT_URL   clone URL for the repo
	GitToken      string // BLERG_RUNNER_GIT_TOKEN optional; injected for https URLs
	APIKey        string // ANTHROPIC_API_KEY
	Resume        bool   // BLERG_RUNNER_RESUME=1  → rebuild transcript, expect wip branch
	Home          string // workspace parent + $HOME; default /workspace
	Version       string

	// NoRepo (BLERG_RUNNER_NO_REPO=1) is a "No repository" session: an empty
	// workspace directory, no clone and no git at all. Repo and GitURL are
	// empty then.
	NoRepo bool
	// NewRepo (BLERG_RUNNER_NEW_REPO=1): a "New repository" session. The
	// clone is retried, and when it keeps failing an empty repository is
	// initialised with origin = GitURL instead of failing the start.
	NewRepo bool

	// IdleTimeout (BLERG_RUNNER_IDLE_TIMEOUT_SECONDS) ends the pod after this
	// long with no user message or finished turn; 0 means never.
	IdleTimeout time.Duration

	// Plugins is the always-on plugin list (BLERG_RUNNER_PLUGINS), Claude sessions only.
	// PluginsErr is set when the variable was present but unusable.
	Plugins    []pluginspec.Entry
	PluginsErr error

	// MCPGateway is the session's MCP gateway grant, from ONE environment
	// variable, daemon.MCPGatewayEnvVar (BLERG_RUNNER_MCP_CONFIG): the JSON of a
	// protocol.MCPGatewayConfig ({"base_url": ..., "servers": [{"name": ...,
	// "token": ...}]}), delivered from the per-session Kubernetes Secret
	// through a secretKeyRef (never written into the Job spec). ConfigFromEnv
	// reads it and UNSETS it at once, before any child process (plugin install,
	// claude, a tool's shell) can inherit it; the daemon package's environment
	// builders drop it as well. The agent host writes the 0600 config file
	// outside the workdir from the spawn (podSpawn) and removes it at the end.
	// MCPGatewayErr is set when the variable was present but unusable: the pod
	// then fails its start rather than run without the grant's tool list.
	MCPGateway    *protocol.MCPGatewayConfig
	MCPGatewayErr error

	// RestrictTools (daemon.RestrictToolsEnvVar, "1"): the session is a cron's
	// or holds a grant, so Claude runs with the tool allow-list, no ambient MCP
	// server and none of the user's settings, plugins, hooks or skills, grant
	// or no grant. Plain, not secret; read and unset at once like the grant.
	RestrictTools bool
}

// takeRestrictToolsEnv consumes daemon.RestrictToolsEnvVar: "1" or "true" (any case) restricts.
func takeRestrictToolsEnv() bool {
	raw, present := os.LookupEnv(daemon.RestrictToolsEnvVar)
	if !present {
		return false
	}
	_ = os.Unsetenv(daemon.RestrictToolsEnvVar)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true":
		return true
	}
	return false
}

// takeMCPGatewayEnv consumes daemon.MCPGatewayEnvVar: parsed, validated and
// removed from the process environment. The error never contains the value.
func takeMCPGatewayEnv() (*protocol.MCPGatewayConfig, error) {
	raw, present := os.LookupEnv(daemon.MCPGatewayEnvVar)
	if !present {
		return nil, nil
	}
	_ = os.Unsetenv(daemon.MCPGatewayEnvVar)
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var cfg protocol.MCPGatewayConfig
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&cfg); err != nil || dec.More() {
		return nil, fmt.Errorf("%s is not a valid MCP gateway configuration", daemon.MCPGatewayEnvVar)
	}
	if err := daemon.ValidateMCPGateway(&cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", daemon.MCPGatewayEnvVar, err)
	}
	return &cfg, nil
}

// skipsUserConfig: a session with a grant runs unattended on untrusted text,
// so it does not load the user's downloaded ~/.claude bundle (settings, hooks,
// plugin cache, agent definitions), which is code and configuration the
// session's tool list does not cover.
func (c Config) skipsUserConfig() bool { return c.MCPGateway != nil || c.RestrictTools }

// podSpawn is the pod's synthesised spawn_session for its one agent session.
func podSpawn(cfg Config, workDir string) protocol.SpawnSession {
	spawn := protocol.SpawnSession{
		Type: "spawn_session", SessionID: cfg.SessionID, Repo: cfg.Repo,
		Title: cfg.Title, Model: cfg.Model, Effort: cfg.Effort, Engine: cfg.Engine, Kind: "agent",
		MCPGateway: cfg.MCPGateway,
		// A grant implies the restriction.
		RestrictTools: cfg.RestrictTools || cfg.MCPGateway != nil,
	}
	if cfg.NoRepo {
		// No Repo to resolve a path from: hand the host the directory itself.
		spawn.ProjectPath = workDir
	}
	return spawn
}

func ConfigFromEnv() Config {
	plugins, pluginsErr := ParsePluginsEnv(os.Getenv("BLERG_RUNNER_PLUGINS"))
	gateway, gatewayErr := takeMCPGatewayEnv()
	return Config{
		RestrictTools: takeRestrictToolsEnv(),
		MCPGateway:    gateway,
		MCPGatewayErr: gatewayErr,
		Plugins:       plugins,
		PluginsErr:    pluginsErr,
		ServerWS:      os.Getenv("BLERG_RUNNER_SERVER_WS"),
		ServerHTTP:    os.Getenv("BLERG_RUNNER_SERVER_HTTP"),
		DaemonToken:   os.Getenv("BLERG_RUNNER_DAEMON_TOKEN"),
		SessionID:     os.Getenv("BLERG_RUNNER_SESSION_ID"),
		Repo:          os.Getenv("BLERG_RUNNER_REPO"),
		Title:         os.Getenv("BLERG_RUNNER_TITLE"),
		Model:         os.Getenv("BLERG_RUNNER_MODEL"),
		Effort:        os.Getenv("BLERG_RUNNER_EFFORT"),
		Engine:        os.Getenv("BLERG_RUNNER_ENGINE"),
		InitialPrompt: os.Getenv("BLERG_RUNNER_INITIAL_PROMPT"),
		GitURL:        os.Getenv("BLERG_RUNNER_GIT_URL"),
		GitToken:      os.Getenv("BLERG_RUNNER_GIT_TOKEN"),
		APIKey:        os.Getenv("ANTHROPIC_API_KEY"),
		Resume:        os.Getenv("BLERG_RUNNER_RESUME") == "1",
		NoRepo:        os.Getenv("BLERG_RUNNER_NO_REPO") == "1",
		NewRepo:       os.Getenv("BLERG_RUNNER_NEW_REPO") == "1",
		IdleTimeout:   idleTimeoutFromEnv(os.Getenv("BLERG_RUNNER_IDLE_TIMEOUT_SECONDS")),
		Home:          envDefault("BLERG_RUNNER_HOME", "/workspace"),
	}
}

// idleTimeoutFromEnv parses the idle timeout in seconds; anything unusable
// means no idle limit (the pod's hard lifetime cap still applies).
func idleTimeoutFromEnv(v string) time.Duration {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// idleTracker records when a session last did something the user would call
// activity: a message sent, a turn finished. Viewing the session is not.
type idleTracker struct{ last atomic.Int64 }

func newIdleTracker() *idleTracker {
	t := &idleTracker{}
	t.touch()
	return t
}

func (t *idleTracker) touch() { t.last.Store(time.Now().UnixNano()) }

func (t *idleTracker) idleFor() time.Duration {
	return time.Since(time.Unix(0, t.last.Load()))
}

// idleCheckEvery is how often the idle limit is checked: promptly for short
// limits, once a minute for long ones.
func idleCheckEvery(limit time.Duration) time.Duration {
	return min(time.Minute, max(limit/10, time.Second))
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// writeCodexAuthFromEnv materializes CODEX_AUTH_JSON (the raw contents of a
// logged-in ~/.codex/auth.json, delivered via k8s Secret the same way
// CLAUDE_CODE_OAUTH_TOKEN is) to $home/.codex/auth.json so a bare `codex
// exec` in this ephemeral pod is already authenticated. No-op when the env
// var is unset — codex simply isn't usable, same as claude with no token.
func writeCodexAuthFromEnv(home string) error {
	raw := os.Getenv("CODEX_AUTH_JSON")
	if raw == "" {
		return nil
	}
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "auth.json"), []byte(raw), 0o600) //nolint:gosec // home is the pod's BLERG_RUNNER_HOME (trusted operator config), file name fixed
}

// writeHermesEnvFromEnv materializes HERMES_ENV_CONTENTS (the raw contents
// of a configured ~/.hermes/.env — provider API keys and everything else
// hermes reads from dotenv) to $home/.hermes/.env, mirroring
// writeCodexAuthFromEnv. Hermes has no single bare-token env var like
// CLAUDE_CODE_OAUTH_TOKEN — its providers are configured via .env — so the
// whole file rides through one secret-backed env var instead. No-op when
// unset, same as the codex case.
func writeHermesEnvFromEnv(home string) error {
	raw := os.Getenv("HERMES_ENV_CONTENTS")
	if raw == "" {
		return nil
	}
	dir := filepath.Join(home, ".hermes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".env"), []byte(raw), 0o600) //nolint:gosec // home is the pod's BLERG_RUNNER_HOME (trusted operator config), file name fixed
}

func wipBranch(sessionID string) string { return "wip/" + sessionID }

// authURL injects the token into an https clone URL, with the username the
// host's git provider expects alongside a token (GitHub "x-access-token",
// GitLab "oauth2"; an unregistered host keeps the historical GitHub one).
// Which token — if any — reaches which host is the server's decision
// (JobManager.gitCredentialFor). The result is a secret: never log it.
func authURL(rawURL, token string) string {
	if token == "" || !strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.User = url.UserPassword(gitprovider.Default.TokenUserForHost(u.Hostname()), token)
	return u.String()
}

// scrubToken removes every occurrence of token (raw and URL-escaped, as it
// sits in a clone URL's userinfo) from s.
func scrubToken(s, token string) string {
	if strings.TrimSpace(token) == "" {
		return s
	}
	s = strings.ReplaceAll(s, token, "REDACTED")
	if esc := url.UserPassword("u", token).String(); esc != "u:"+token {
		s = strings.ReplaceAll(s, strings.TrimPrefix(esc, "u:"), "REDACTED")
	}
	return s
}

func gitRun(dir string, args ...string) (string, error) {
	// The agent owns the working tree and can write .git/config: keep git from EXECUTING anything the
	// repository's own settings name (a hook, an fsmonitor command, a credential helper). The command line
	// beats the repository config for these keys.
	args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "protocol.ext.allow=never"}, args...)
	cmd := exec.Command("git", args...) //nolint:gosec,noctx // literal git binary; callers pass fixed subcommands, and clone puts the URL after --; noctx: short git steps of pod startup, which has no context to bound them; the pod itself is killed on timeout
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ScratchDir is the workspace directory, under Home, of a "No repository"
// session. Not Home itself: Home is also $HOME, holding the downloaded
// ~/.claude bundle and engine credentials, which are no business of the
// agent's working tree.
const ScratchDir = "scratch"

// WorkspaceEmpty is the workspace state of a "No repository" session: a new,
// empty directory — also on resume, since the pod's filesystem did not
// survive and there is no wip branch to bring anything back from.
const WorkspaceEmpty = "empty"

// WorkspaceInitialised is the workspace state of a "New repository" session
// whose repository could not be cloned: a fresh `git init` whose origin points
// at where the repository would be, with no commits.
const WorkspaceInitialised = daemon.WorkspaceInitialised

// newRepoCloneAttempts and newRepoCloneDelay: a repository created seconds ago
// may not be clonable yet (GitHub's auto_init commit lands asynchronously), so
// a new-repository clone is retried before the pod decides there is nothing
// there. The delay is a variable so tests need not wait.
var (
	newRepoCloneAttempts = 5
	newRepoCloneDelay    = 2 * time.Second
)

// PrepareWorkspace clones the repo under home and, on resume, checks out the
// session's wip branch if the remote has one. Returns the workDir and the
// workspace state ("fresh-clone" | "resumed-from-wip"). A no-repo session gets
// an empty ScratchDir instead, and state WorkspaceEmpty; a new-repo session
// whose clone fails gets an initialised empty repository, WorkspaceInitialised.
func PrepareWorkspace(cfg Config) (string, string, error) {
	if cfg.NoRepo {
		workDir := filepath.Join(cfg.Home, ScratchDir)
		if err := os.MkdirAll(workDir, 0o750); err != nil {
			return "", "", fmt.Errorf("create empty workspace: %w", err)
		}
		return workDir, WorkspaceEmpty, nil
	}
	workDir := filepath.Join(cfg.Home, cfg.Repo)
	if _, err := os.Stat(filepath.Join(workDir, ".git")); err == nil {
		return workDir, "existing", nil // pre-mounted workspace (tests)
	}
	cloneURL := authURL(cfg.GitURL, cfg.GitToken)
	// A blobless partial clone: full history and every branch, but file
	// contents are fetched only for the checkout (and lazily afterwards). Pod
	// startup is bounded by the download, and history blobs are most of it. A
	// server that cannot filter makes git warn and clone in full.
	attempts := 1
	if cfg.NewRepo {
		attempts = newRepoCloneAttempts
	}
	var cloneErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(newRepoCloneDelay)
		}
		out, err := gitRun(cfg.Home, "clone", "--filter=blob:none", "--", cloneURL, workDir)
		if err == nil {
			cloneErr = nil
			break
		}
		// git anonymises URLs in its own messages; this is the belt to that
		// braces — the text travels to the browser as a start-stage detail.
		cloneErr = &cloneError{out: scrubToken(fmt.Sprintf("%v: %s", err, out), cfg.GitToken)}
		_ = os.RemoveAll(workDir) // a failed clone may leave the directory behind
	}
	if cloneErr != nil {
		if !cfg.NewRepo {
			return "", "", cloneErr
		}
		// On a resume the wip branch may already hold earlier work on the remote, and PushWip would force over
		// it from an empty repository. Only an answer that says the repository is not there justifies starting
		// empty; a flaky network or a provider outage fails the start instead.
		if cfg.Resume && !repoMissing(cloneErr.Error()) {
			return "", "", cloneErr
		}
		// The repository is not there (not created, or not visible to this
		// token): start in an empty one that pushes to where it should be.
		log.Printf("runner: new repository: clone failed after %d attempt(s); initialising an empty repository", attempts)
		if err := initNewRepo(cfg, workDir, cloneURL); err != nil {
			return "", "", err
		}
		return workDir, WorkspaceInitialised, nil
	}
	state := "fresh-clone"
	wip := wipBranch(cfg.SessionID)
	if out, err := gitRun(workDir, "ls-remote", "--heads", "origin", wip); err == nil && strings.Contains(out, wip) {
		if out, err := gitRun(workDir, "checkout", "-b", wip, "origin/"+wip); err != nil {
			log.Printf("runner: wip checkout failed (%v: %s); staying on default branch", err, out)
		} else {
			state = "resumed-from-wip"
		}
	} else if !cfg.Resume {
		// Fresh session: work on the wip branch from the start so every
		// commit is safety-pushable.
		_, _ = gitRun(workDir, "checkout", "-b", wip)
	}
	return workDir, state, nil
}

// repoMissing reports whether a failed clone's output says the remote repository does not exist (or is not
// visible to the token), as opposed to a network or server failure.
func repoMissing(out string) bool {
	out = strings.ToLower(out)
	for _, s := range []string{"not found", "does not exist", "does not appear to be a git repository"} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

// initNewRepo makes workDir an empty repository on the session's wip branch
// whose origin is cloneURL (token included when one was given — the same
// exposure as a normal clone's .git/config — so a push works as soon as the
// remote exists).
func initNewRepo(cfg Config, workDir, cloneURL string) error {
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"symbolic-ref", "HEAD", "refs/heads/" + wipBranch(cfg.SessionID)},
		{"remote", "add", "origin", cloneURL},
	} {
		if out, err := gitRun(workDir, args...); err != nil {
			return fmt.Errorf("init new repository: git %s: %v: %s", args[0], err, scrubToken(out, cfg.GitToken))
		}
	}
	return nil
}

// newRepoNote is prepended to the first prompt of a session whose repository
// could not be created: the agent would otherwise push, see an error and tell
// nobody. url is the redacted origin.
func newRepoNote(repo, url string) string {
	return "[system] This is a new repository that could not be created on its git host: `origin` points at " + url +
		" but that repository does not exist yet, so `git push` will fail until it does. Before pushing, create it (for example with " +
		"`gh repo create " + repo + "` or the host's API, if you have a credential that may) or ask the person to create it; " +
		"blerg-runner cannot do this for you. Work and commit as usual in the meantime.\n\n"
}

// pushProblem is what PushWip tells the session about a failed push, once.
// nil = log only (the default).
var pushProblem func(workDir, sessionID, detail string)

// PushWip force-pushes the session's wip branch if there are new commits.
// Idempotent and cheap; called after every turn and on SIGTERM.
func PushWip(workDir, sessionID string) {
	head, err := gitRun(workDir, "rev-parse", "HEAD")
	if err != nil {
		return
	}
	remote, _ := gitRun(workDir, "rev-parse", "origin/"+wipBranch(sessionID))
	if head == remote {
		return
	}
	if out, err := gitRun(workDir, "push", "--force", "origin", "HEAD:"+wipBranch(sessionID)); err != nil {
		log.Printf("runner: wip push: %v: %s", err, out)
		if pushProblem != nil {
			pushProblem(workDir, sessionID, classifyPushFailure(out))
		}
	}
}

// classifyPushFailure is the fixed, caller-safe text for a failed push: git's
// own output can carry the clone URL, and with it the token.
func classifyPushFailure(out string) string {
	o := strings.ToLower(out)
	switch {
	case strings.Contains(o, "repository not found") || strings.Contains(o, "does not appear to be a git repository") || strings.Contains(o, "could not read from remote"):
		return "the remote repository does not exist (or this session's token cannot see it)"
	case strings.Contains(o, "authentication failed") || strings.Contains(o, "403") || strings.Contains(o, "permission"):
		return "the remote refused this session's credential"
	}
	return "git push failed; see the pod log"
}

// FetchTranscript pulls the persisted transcript for resume.
func FetchTranscript(ctx context.Context, cfg Config) ([]agent.RestoredEvent, error) {
	var all []agent.RestoredEvent
	after := int64(0)
	client := &http.Client{Timeout: 30 * time.Second}
	for {
		u := fmt.Sprintf("%s/api/sessions/%s/agent-events?after_seq=%d&limit=500", cfg.ServerHTTP, cfg.SessionID, after)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.DaemonToken)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("transcript fetch: %d: %s", resp.StatusCode, raw)
		}
		var page []protocol.AgentEvent
		decErr := json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if decErr != nil {
			return nil, decErr
		}
		for _, ev := range page {
			all = append(all, agent.RestoredEvent{Kind: ev.Kind, Payload: ev.Payload})
			if ev.Seq > after {
				after = ev.Seq
			}
		}
		if len(page) < 500 {
			return all, nil
		}
	}
}

// Run is the runner main loop: prepare workspace + config, host the single
// agent session, and block until ctx is cancelled (SIGTERM) or the session is
// killed server-side.
func Run(ctx context.Context, cfg Config) error {
	if cfg.SessionID == "" || cfg.ServerWS == "" {
		return fmt.Errorf("BLERG_RUNNER_SESSION_ID and BLERG_RUNNER_SERVER_WS are required")
	}
	if err := os.MkdirAll(cfg.Home, 0o750); err != nil {
		return err
	}
	// The pod's HOME hosts the downloaded ~/.claude bundle.
	if err := os.Setenv("HOME", cfg.Home); err != nil {
		return err
	}

	// Connect first, prepare the workspace second: the clone is the slow,
	// failure-prone part of a start, and reporting its progress (and its
	// failure, with a reason) needs the server connection. The session has
	// no host-side state yet, so nothing is reported active until spawn.
	client := daemon.NewWSClient(daemon.Config{
		ServerURL:   cfg.ServerWS,
		DaemonToken: cfg.DaemonToken,
		DaemonName:  "runner-" + cfg.SessionID,
		DaemonMode:  "runner",
		ReposRoot:   cfg.Home,
		Version:     cfg.Version,
	})

	// workDir is set once the workspace is ready; the turn-done hook below
	// only fires after spawn, which is after that.
	var workDir string
	killed := make(chan struct{})
	activity := newIdleTracker()
	host := daemon.NewAgentHost(client, daemon.AgentHostConfig{
		ReposRoot:   cfg.Home,
		ServerHTTP:  cfg.ServerHTTP,
		DaemonToken: cfg.DaemonToken,
		APIKey:      cfg.APIKey,
		// Subscription mode: bill sessions to the operator's Claude plan via
		// headless Claude Code when its OAuth token is provided and no
		// metered API key is.
		ClaudeCode: os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" && cfg.APIKey == "",
		HomeDir:    cfg.Home,
		Pricing:    daemon.DefaultPricing(),
		OnTurnDone: func(sessionID string) {
			activity.touch()
			go PushWip(workDir, sessionID)
		},
	})

	client.OnMessage("agent_event_ack", func(raw []byte) {
		var msg protocol.AgentEventAck
		if json.Unmarshal(raw, &msg) == nil {
			host.HandleAck(msg.SessionID, msg.ClientEventID)
		}
	})
	client.OnMessage("agent_user_message", func(raw []byte) {
		var msg protocol.AgentUserMessage
		if json.Unmarshal(raw, &msg) == nil {
			activity.touch()
			host.UserMessage(msg.SessionID, msg.Text, msg.Source)
		}
	})
	client.OnMessage("set_session_model", func(raw []byte) {
		var msg protocol.SetSessionModel
		if json.Unmarshal(raw, &msg) == nil {
			host.SetModel(msg.SessionID, msg.Model, msg.Effort)
		}
	})
	client.OnMessage("interrupt_session", func(raw []byte) {
		var msg protocol.InterruptSession
		if json.Unmarshal(raw, &msg) == nil {
			host.Interrupt(msg.SessionID)
		}
	})
	client.OnMessage("kill_session", func(raw []byte) {
		var msg protocol.KillSession
		if json.Unmarshal(raw, &msg) == nil && msg.SessionID == cfg.SessionID {
			host.Kill(msg.SessionID)
			close(killed)
		}
	})
	// Start stage reports queue until the connection allows them; each
	// (re)connect flushes what is waiting.
	stages := &stageReporter{s: client, sessionID: cfg.SessionID}
	connected := make(chan struct{})
	var connectOnce sync.Once
	client.SetOnConnect(func() {
		connectOnce.Do(func() { close(connected) })
		host.ResendPending()
		stages.flush()
	})
	client.SetSessionStates(host.States)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go client.Run(runCtx, host.ActiveIDs)

	// Wait for the first successful connection before spawning: the
	// session_started message is fire-and-forget, and losing it would leave
	// status stuck until the first heartbeat reconcile. Cap the wait — the
	// server-side session row already exists, so a late connect still works.
	select {
	case <-connected:
	case <-time.After(60 * time.Second):
		log.Printf("runner: no server connection after 60s; proceeding (events buffer until connect)")
	case <-ctx.Done():
		return nil
	}

	if detail, hint := missingEngineCredential(cfg.Engine, os.Getenv); detail != "" {
		stages.report(done(protocol.StageConnect))
		stages.fail(protocol.StageEngine, detail, hint, cfg.Resume)
		return fmt.Errorf("%s", detail)
	}
	cloneDetail := "Cloning " + cfg.Repo
	if cfg.NoRepo {
		cloneDetail = "Preparing an empty workspace (no repository)"
	}
	stages.report(done(protocol.StageConnect), active(protocol.StageClone, cloneDetail))

	if cfg.MCPGatewayErr != nil {
		// Fail closed: the server meant this session to have a grant and its
		// tool allow-list; starting without them would be the wrong session.
		detail := "the MCP gateway configuration is unusable"
		log.Printf("runner: %v", cfg.MCPGatewayErr)
		stages.report(done(protocol.StageConnect))
		stages.fail(protocol.StageEngine, detail, "", cfg.Resume)
		return fmt.Errorf("%s", detail)
	}
	if cfg.skipsUserConfig() {
		log.Printf("runner: MCP gateway grant: not loading the user's Claude config bundle")
	} else if err := daemon.DownloadConfigBundle(ctx, cfg.ServerHTTP, cfg.DaemonToken, cfg.Home); err != nil {
		log.Printf("runner: config bundle: %v (continuing without user config)", err)
	}
	if err := writeCodexAuthFromEnv(cfg.Home); err != nil {
		log.Printf("runner: codex auth: %v (continuing without codex login)", err)
	}
	if err := writeHermesEnvFromEnv(cfg.Home); err != nil {
		log.Printf("runner: hermes env: %v (continuing without hermes credentials)", err)
	}

	wd, wsState, err := PrepareWorkspace(cfg)
	if err != nil {
		detail, hint := classifyWorkspaceError(err)
		stages.fail(protocol.StageClone, detail, hint, cfg.Resume)
		return err
	}
	workDir = wd
	log.Printf("runner: workspace %s (%s)", workDir, wsState)
	if wsState == WorkspaceInitialised {
		// The clone did not happen: a warning (which later progress never
		// folds to a green check) says what the workspace is instead, and the
		// agent's first prompt says what that means for pushing.
		stages.report(protocol.StartStage{ID: protocol.StageClone, State: protocol.StageStateWarning,
			Detail: "Initialised an empty repository — no remote yet"})
		cfg.InitialPrompt = newRepoNote(cfg.Repo, gitprovider.RedactURL(cfg.GitURL)) + cfg.InitialPrompt
		var once sync.Once
		pushProblem = func(_, sessionID, detail string) {
			once.Do(func() {
				payload, _ := json.Marshal(agent.ErrorPayload{Message: "Pushing the wip branch failed: " + detail, Retryable: true})
				_ = client.Send(protocol.AgentEvent{
					Type: "agent_event", SessionID: sessionID, ClientEventID: newEventID(),
					Ts: time.Now().UTC().Format(time.RFC3339Nano), Kind: "error", Payload: payload,
				})
			})
		}
	}
	reportWorkspaceReady(ctx, cfg, stages, installPlugins)

	spawn := podSpawn(cfg, workDir)
	if cfg.Resume {
		events, err := FetchTranscript(ctx, cfg)
		if err != nil {
			log.Printf("runner: transcript fetch: %v (starting fresh)", err)
			spawn.InitialPrompt = cfg.InitialPrompt
			host.Spawn(spawn) //nolint:contextcheck // the session outlives this startup context; the prompt-context fetch inside has its own short timeout
		} else {
			// The resume prompt rides in the spawn, so the session goes
			// straight to its turn: delivered separately after spawn, the host
			// would first report the session idle (nothing queued) and the
			// user would get a "finished its turn" for a turn that never ran.
			spawn.InitialPrompt = cfg.InitialPrompt
			host.SpawnResumed(spawn, events, wsState) //nolint:contextcheck // the session outlives this startup context; the prompt-context fetch inside has its own short timeout
		}
	} else {
		spawn.InitialPrompt = cfg.InitialPrompt
		host.Spawn(spawn) //nolint:contextcheck // the session outlives this startup context; the prompt-context fetch inside has its own short timeout
	}

	var idleTick <-chan time.Time
	if cfg.IdleTimeout > 0 {
		t := time.NewTicker(idleCheckEvery(cfg.IdleTimeout))
		defer t.Stop()
		idleTick = t.C
	}
	for {
		select {
		case <-idleTick:
			if activity.idleFor() < cfg.IdleTimeout {
				continue
			}
			log.Printf("runner: idle for %s — ending the pod; the session can be resumed", cfg.IdleTimeout)
			PushWip(workDir, cfg.SessionID)
			return nil
		case <-ctx.Done():
			log.Printf("runner: terminating — final wip push")
			PushWip(workDir, cfg.SessionID)
			return nil
		case <-killed:
			PushWip(workDir, cfg.SessionID)
			return nil
		}
	}
}
