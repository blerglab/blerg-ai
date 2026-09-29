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
	"strings"
	"sync"
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

	// Plugins is the always-on plugin list (BLERG_RUNNER_PLUGINS), Claude sessions only.
	// PluginsErr is set when the variable was present but unusable.
	Plugins    []pluginspec.Entry
	PluginsErr error
}

func ConfigFromEnv() Config {
	plugins, pluginsErr := ParsePluginsEnv(os.Getenv("BLERG_RUNNER_PLUGINS"))
	return Config{
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
		Home:          envDefault("BLERG_RUNNER_HOME", "/workspace"),
	}
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

// PrepareWorkspace clones the repo under home and, on resume, checks out the
// session's wip branch if the remote has one. Returns the workDir and the
// workspace state ("fresh-clone" | "resumed-from-wip"). A no-repo session gets
// an empty ScratchDir instead, and state WorkspaceEmpty.
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
	if out, err := gitRun(cfg.Home, "clone", "--", cloneURL, workDir); err != nil {
		// git anonymises URLs in its own messages; this is the belt to that
		// braces — the text travels to the browser as a start-stage detail.
		return "", "", &cloneError{out: scrubToken(fmt.Sprintf("%v: %s", err, out), cfg.GitToken)}
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
	}
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

	if err := daemon.DownloadConfigBundle(ctx, cfg.ServerHTTP, cfg.DaemonToken, cfg.Home); err != nil {
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
	reportWorkspaceReady(ctx, cfg, stages, installPlugins)

	spawn := protocol.SpawnSession{
		Type: "spawn_session", SessionID: cfg.SessionID, Repo: cfg.Repo,
		Title: cfg.Title, Model: cfg.Model, Effort: cfg.Effort, Engine: cfg.Engine, Kind: "agent",
	}
	if cfg.NoRepo {
		// No Repo to resolve a path from: hand the host the directory itself.
		spawn.ProjectPath = workDir
	}
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

	select {
	case <-ctx.Done():
		log.Printf("runner: terminating — final wip push")
		PushWip(workDir, cfg.SessionID)
		return nil
	case <-killed:
		PushWip(workDir, cfg.SessionID)
		return nil
	}
}
