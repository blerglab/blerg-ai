package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/scratch"
)

// Sender is the interface used by Manager to send messages. *WSClient satisfies it.
type Sender interface {
	Send(msg any) error
}

// ManagerConfig holds configuration for the session manager.
type ManagerConfig struct {
	// ReposRoot is the initial repos root. ReposRootSetting, when set, is the
	// live one (changeable from the app, persisted); without it the Manager
	// wraps ReposRoot in a setting that cannot be saved, so a change is
	// refused (tests, and any caller with nowhere to persist it).
	ReposRoot        string
	ReposRootSetting *ReposRootSetting
	DaemonName       string
	PreviewURL       string
	DaemonToken      string
	ServerHTTP       string
	GithubOrg        string
	// AllowHostCredentialClone lets a named clone use the launching person's
	// own git token on the bare host (a This machine session). Off by
	// default: on the host, any other unsandboxed session on this machine
	// could read the token from git's process environment while it clones.
	// Only for a daemon nobody else uses (BLERG_RUNNER_ALLOW_HOST_CREDENTIAL_CLONE).
	AllowHostCredentialClone bool

	// SandboxNetwork is the docker network sandbox containers join when it
	// exists (BLERG_RUNNER_SANDBOX_NETWORK; "" = never join one). RepoRoot is
	// the daemon's checkout, from which the messaging CLI is mounted into
	// sandboxes. Both feed SandboxOptions — see sandbox.go.
	SandboxNetwork string
	RepoRoot       string

	// Command overrides the default claude command. Used in tests.
	Command []string
}

// withReposRootSetting fills in ReposRootSetting from ReposRoot when the
// caller supplied none.
func (c ManagerConfig) withReposRootSetting() ManagerConfig {
	if c.ReposRootSetting == nil {
		c.ReposRootSetting = NewReposRootSetting(c.ReposRoot, "")
	}
	return c
}

// reposRoot is the repos root in effect right now. Read it once per
// operation: the owner can change it between two reads.
func (m *Manager) reposRoot() string {
	return m.config.ReposRootSetting.Get()
}

// writeRecord and deleteRecord touch a session record under the root in
// effect, holding off a concurrent repos-root change (and its record move)
// until they are done — see ReposRootSetting.WithRoot.
func (m *Manager) writeRecord(rec SessionRecord) (err error) {
	m.config.ReposRootSetting.WithRoot(func(root string) { err = writeSessionRecord(root, rec) })
	return err
}

func (m *Manager) deleteRecord(id string) {
	m.config.ReposRootSetting.WithRoot(func(root string) { deleteSessionRecord(root, id) })
}

// sandboxOptions is the sandbox setup every container start of this daemon
// applies, terminal and agent kind alike.
func (c ManagerConfig) sandboxOptions() SandboxOptions {
	return SandboxOptions{Network: c.SandboxNetwork, RepoRoot: c.RepoRoot}
}

// Manager tracks active PTY sessions and handles server-driven lifecycle messages.
type Manager struct {
	mu          sync.Mutex
	sessions    map[string]*Session
	trackers    map[string]*StateTracker
	metaParsers map[string]*MetaParser
	client      Sender
	config      ManagerConfig

	// agents hosts agent-kind sessions (the LLM harness). Nil when the
	// daemon has no API key configured.
	agents *AgentHost

	// spawns carries spawn_session off the websocket read pump. A spawn's
	// first action is prepareWorkspace, whose EnsureCloned can run for
	// minutes on a cold board-driven start; handled inline, that froze every
	// other message — kill_session, send_input and the websocket control
	// frames ReadMessage processes — long enough for the server's heartbeat
	// to declare the daemon dead. One worker drains the queue, so spawns keep
	// the arrival order (and the one-at-a-time semantics) they had when they
	// ran on the read pump; only the pump itself is freed.
	spawns chan []byte

	// pendingSpawns tracks spawns that are queued or in flight, mapped to
	// whether a kill_session has since arrived for them (guarded by mu).
	// Detaching the spawn means a kill can now overtake it, and a spawn that
	// finished creating a session the user already killed would be an orphan.
	pendingSpawns map[string]bool
	// spawnCancels cancels a spawn's workspace step — above all a clone,
	// which can run for many minutes — the moment a kill_session arrives for
	// it (guarded by mu). Without it the kill only took effect once the clone
	// finished, and every spawn queued behind it waited that long too.
	spawnCancels map[string]spawnCtl

	// ensureClonedFunc clones a repo that is not checked out yet. A seam so
	// tests can exercise a slow/blocking clone without a network.
	ensureClonedFunc func(ctx context.Context, reposRoot, repo, provider, githubOrg string) error

	// reattachMu serializes ReattachSessions so two overlapping (re)connections
	// can't both reattach/recover the same session — which would double-attach a
	// PTY and leak a goroutine. mu guards only the maps and must not be held
	// across the blocking PTY/tmux work inside a reattach, so this is separate.
	reattachMu sync.Mutex

	// refreshTimers holds a pending debounced tmux repaint per session (guarded by
	// mu). A resize changes the PTY size immediately, but tmux's incremental
	// redraw can leave stale cells in the browser's xterm at the old geometry;
	// forcing a full refresh-client once the resize burst settles clears that
	// residue. refreshFunc performs the repaint (overridable in tests);
	// refreshDebounce is how long after the last resize we wait before repainting.
	refreshTimers   map[string]*time.Timer
	refreshFunc     func(sessionID string)
	refreshDebounce time.Duration

	// scrollbackFunc captures the session's tmux scrollback (the lines above the
	// visible screen) as ANSI text, bounded to the last maxLines lines when
	// maxLines > 0. Overridable in tests; nil-guarded so unit tests don't exec
	// tmux unless they inject their own.
	scrollbackFunc func(sessionID string, maxLines int) (string, error)

	// modeSyncFunc queries a session's current pane modes and returns the
	// escape sequences that reflect them (see modeSyncSequences), for the
	// mode-sync preamble on SessionScrollback.ModePrefix. Overridable in tests;
	// nil-guarded so unit tests don't exec tmux unless they inject their own —
	// nil means "no mode-sync preamble" rather than "run real tmux".
	modeSyncFunc func(sessionID string) string

	// noteReplies is a per-session single-slot pending injection (guarded by mu):
	// either a note reply or a freshly spawned session's initial prompt. Both are
	// "text to submit once the session is idle" and share the same queue/flush
	// machinery. Latest-wins: a second entry before the session goes idle
	// overwrites the first. The slot is daemon-memory only; it is cleared on
	// consume or lost on restart.
	noteReplies map[string]string

	// capturePaneFunc captures the current screen of a session for state
	// classification before injecting a note reply. Overridable in tests so no
	// real tmux is needed; nil means "can't classify → queue the reply".
	capturePaneFunc func(sessionID string) (string, error)

	// sandboxImagePresent reports whether the sandbox image is available, used
	// by preflightEngine before a sandboxed spawn. Defaults to
	// sandboxImageAvailable; tests override to force the missing-image path
	// without needing a real Docker daemon.
	sandboxImagePresent func() bool

	// sandboxEnginePresent reports whether an engine binary exists inside the
	// sandbox image, used by preflightEngine before a sandboxed agent-kind
	// spawn. Defaults to engineInSandboxImage; tests override it so no real
	// Docker daemon (or image) is needed.
	sandboxEnginePresent func(bin string) error

	// beforeRecordMigration, when set (tests), runs inside a repos-root
	// change after the new root is live and before records move — the
	// window a concurrent kill must not be able to slip into.
	beforeRecordMigration func()
}

// defaultRefreshDebounce coalesces a burst of resizes (e.g. a window drag, or the
// frontend's history_done double-resize) into a single repaint, and is long
// enough that tmux has applied the final size before we refresh.
const defaultRefreshDebounce = 120 * time.Millisecond

// statePollInterval is how often the daemon snapshots each session's screen to
// classify its state. Short enough that clicking a session or submitting a prompt
// reflects within ~1s; capture-pane is a cheap local exec.
const statePollInterval = time.Second

// minResizeCols/minResizeRows reject degenerate resizes — the frontend's gridCount
// floor (20x5), sent when a terminal element is measured before it's laid out (e.g.
// while navigating away on mobile, when clientWidth/clientHeight collapse to 0).
// Honoring such a size shrinks the pane until Claude's status/working lines are
// squeezed off-screen, so the state poller can't see them and a busy session is
// misclassified as idle. Below these we ignore the resize and keep the last good
// size. Real clients are always well above (the smallest observed is ~46x20).
const (
	minResizeCols = 24
	minResizeRows = 8
)

// NewManager creates a Manager and registers its message handlers on client.
func NewManager(client *WSClient, cfg ManagerConfig) *Manager {
	cfg = cfg.withReposRootSetting()
	m := &Manager{
		sessions:             make(map[string]*Session),
		trackers:             make(map[string]*StateTracker),
		metaParsers:          make(map[string]*MetaParser),
		client:               client,
		config:               cfg,
		refreshTimers:        make(map[string]*time.Timer),
		refreshFunc:          refreshTmuxClient,
		refreshDebounce:      defaultRefreshDebounce,
		scrollbackFunc:       captureScrollback,
		modeSyncFunc:         modeSyncSequences,
		noteReplies:          make(map[string]string),
		capturePaneFunc:      capturePane,
		sandboxImagePresent:  sandboxImageAvailable,
		sandboxEnginePresent: engineInSandboxImage,
	}
	m.startSpawnWorker()
	// AgentHost is always constructed, even with no ANTHROPIC_API_KEY: the
	// claude-code and codex engines (see agentsession.go's driver switch)
	// shell out to their own CLI and bill to the operator's own subscription
	// login, never touching this key at all. Only the native tool-calling
	// loop (the default when neither engine is selected) actually needs it,
	// and that path fails with a clear error at spawn time instead, rather
	// than silently making every agent-kind session unavailable up front.
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	budget := 25.0
	if v := os.Getenv("BLERG_RUNNER_AGENT_BUDGET_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			budget = f
		}
	}
	m.agents = NewAgentHost(client, AgentHostConfig{
		// A desktop Claude agent session runs on the user's `claude` login
		// when it is installed, like a terminal session does.
		PreferClaudeCLI: true,
		ReposRoot:       cfg.ReposRoot,
		ReposRootFn:     cfg.ReposRootSetting.Get,
		ServerHTTP:      cfg.ServerHTTP,
		DaemonToken:     cfg.DaemonToken,
		APIKey:          apiKey,
		BudgetUSD:       budget,
		Pricing:         DefaultPricing(),
		Sandbox:         cfg.sandboxOptions(),
	})
	if apiKey != "" {
		log.Printf("manager: agent sessions enabled (Claude: the claude CLI when installed, else the native loop, budget $%.0f/session)", budget)
	} else {
		log.Printf("manager: agent sessions enabled (Claude needs the claude CLI on PATH — no ANTHROPIC_API_KEY for the native loop; %s run their CLIs)", strings.Join(engineOrder, "/"))
	}
	logEngineConfig()
	client.OnMessage("spawn_session", m.enqueueSpawn)
	client.OnMessage("send_input", m.handleSendInput)
	client.OnMessage("kill_session", m.handleKillSession)
	client.OnMessage("resize_session", m.handleResizeSession)
	client.OnMessage("request_scrollback", m.handleRequestScrollback)
	client.OnMessage("inject_note_reply", m.handleInjectNoteReply)
	client.OnMessage("agent_event_ack", m.handleAgentEventAck)
	client.OnMessage("agent_user_message", m.handleAgentUserMessage)
	client.OnMessage("set_session_model", m.handleSetSessionModel)
	client.OnMessage("interrupt_session", m.handleInterruptSession)
	client.OnMessage("set_repos_root", m.handleSetReposRoot)
	client.SetReposRoot(cfg.ReposRootSetting.Get)
	return m
}

// ─── Agent session routing ───────────────────────────────────────────────────

func (m *Manager) handleAgentEventAck(raw []byte) {
	var msg protocol.AgentEventAck
	if err := json.Unmarshal(raw, &msg); err != nil || m.agents == nil {
		return
	}
	m.agents.HandleAck(msg.SessionID, msg.ClientEventID)
}

func (m *Manager) handleAgentUserMessage(raw []byte) {
	var msg protocol.AgentUserMessage
	if err := json.Unmarshal(raw, &msg); err != nil || m.agents == nil {
		return
	}
	m.agents.UserMessage(msg.SessionID, msg.Text, msg.Source)
}

func (m *Manager) handleSetSessionModel(raw []byte) {
	var msg protocol.SetSessionModel
	if err := json.Unmarshal(raw, &msg); err != nil || m.agents == nil {
		return
	}
	m.agents.SetModel(msg.SessionID, msg.Model, msg.Effort)
}

func (m *Manager) handleInterruptSession(raw []byte) {
	var msg protocol.InterruptSession
	if err := json.Unmarshal(raw, &msg); err != nil || m.agents == nil {
		return
	}
	m.agents.Interrupt(msg.SessionID)
}

// handleSetReposRoot answers set_repos_root off the read pump: validating a
// new root touches the filesystem (mkdir, a write probe, reading each repo's
// git remote), which a slow or network mount can stretch out.
func (m *Manager) handleSetReposRoot(raw []byte) {
	var msg protocol.SetReposRoot
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("manager: bad set_repos_root: %v", err)
		return
	}
	go func() {
		res := m.setReposRoot(msg)
		_ = m.client.Send(res)
		// The rest of what describes the new root (each folder's remote)
		// rides the usual heartbeat: send one now rather than in up to 30s.
		if k, ok := m.client.(interface{ RequestHeartbeat() }); ok && res.OK {
			k.RequestHeartbeat()
		}
	}()
}

// setReposRoot moves the repos root for NEW work: spawns, the repo listing
// and recovery records. Sessions already running keep the absolute paths
// they resolved at spawn. Their recovery records move to the new root, so a
// later kill, natural exit or hard-restart recovery still finds them. On a
// refusal nothing changes and the result says why.
func (m *Manager) setReposRoot(msg protocol.SetReposRoot) protocol.ReposRootResult {
	res := protocol.ReposRootResult{Type: "repos_root_result", RequestID: msg.RequestID}
	// The records move inside Set's critical section: a kill or exit that
	// deleted a record between the new root going live and the move would
	// otherwise find nothing to delete there, and the move would then write
	// the killed session's record back for recovery to revive.
	root, err := m.config.ReposRootSetting.Set(msg.ReposRoot, func(old, next string) {
		if m.beforeRecordMigration != nil {
			m.beforeRecordMigration()
		}
		migrateSessionRecords(old, next)
		log.Printf("manager: repos root changed from %s to %s", old, next)
	})
	res.ReposRoot = root
	if err != nil {
		log.Printf("manager: repos root change refused: %v", err)
		res.Error = err.Error()
		return res
	}
	res.OK = true
	res.CheckedOutRepos = listReposOnDisk(root)
	return res
}

// newManagerWithSender is used in tests to inject a custom Sender.
func newManagerWithSender(sender Sender, cfg ManagerConfig) *Manager {
	cfg = cfg.withReposRootSetting()
	m := &Manager{
		sessions:    make(map[string]*Session),
		trackers:    make(map[string]*StateTracker),
		metaParsers: make(map[string]*MetaParser),
		client:      sender,
		config:      cfg,
		// refreshFunc is left nil so unit tests don't exec tmux; tests that need
		// the repaint inject their own. refreshDebounce gets a sane default.
		refreshTimers:   make(map[string]*time.Timer),
		refreshDebounce: defaultRefreshDebounce,
		// noteReplies must be initialised so handlers can write to it without a nil-map panic.
		// capturePaneFunc is left nil; tests that need classification inject their own.
		noteReplies:          make(map[string]string),
		sandboxImagePresent:  sandboxImageAvailable,
		sandboxEnginePresent: engineInSandboxImage,
	}
	m.startSpawnWorker()
	return m
}

// claudeCommand builds the inner claude invocation. When resume is true it
// continues an existing conversation (--resume); otherwise it pins a new session
// to id (--session-id) so the conversation can be resumed after a hard restart.
func claudeCommand(id, model, effort string, skipPerms, resume, sandboxed bool) []string {
	cmd := []string{resolveEngineBinary(claudeEngineSpec.Binary, sandboxed)}
	cmd = append(cmd, modelEffortArgs(claudeEngineSpec, model, effort)...)
	if skipPerms {
		cmd = append(cmd, claudeEngineSpec.SkipPermsFlag)
	}
	if resume {
		cmd = append(cmd, "--resume", id)
	} else {
		cmd = append(cmd, "--session-id", id)
	}
	return cmd
}

// spawnModelProblem reports why a spawn's model/effort must be refused, or ""
// when both are usable — the daemon's own check against the engine's rules
// (internal/models), not trusting the server's.
func spawnModelProblem(msg protocol.SpawnSession) string {
	if !models.ValidModelFor(msg.Engine, msg.Model) {
		return fmt.Sprintf("refusing spawn: invalid model %q", msg.Model)
	}
	if !models.ValidEffortFor(msg.Engine, msg.Effort) {
		return fmt.Sprintf("refusing spawn: invalid effort %q", msg.Effort)
	}
	return ""
}

// resolveEngineBinary returns the command name for a CLI engine (claude,
// codex, ...). sandboxed skips resolving to an absolute host path via
// LookPath: that path (e.g. /home/<host-user>/.local/bin/claude) doesn't
// exist inside a Docker sandbox, and embedding it in the pane's command fails
// instantly, silently killing the tmux session (and, since it was the
// container's only session, its whole private tmux server) the moment it's
// created. The bare name resolves fine there against the image's own PATH.
func resolveEngineBinary(name string, sandboxed bool) string {
	if sandboxed {
		return name
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

// ActiveSessionIDs returns the IDs this daemon should be considered to own:
// sessions it is actively managing, plus any persistent tmux sessions left on
// disk that a restart has not yet reattached. Reporting the union keeps the
// server from reaping persisted sessions during the reconnect window.
func (m *Manager) ActiveSessionIDs() []string {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	if m.agents != nil {
		ids = append(ids, m.agents.ActiveIDs()...)
	}
	// A custom command (tests) is not tmux-backed, so skip the tmux probe.
	if len(m.config.Command) > 0 {
		return ids
	}
	var recorded []string
	m.config.ReposRootSetting.WithRoot(func(root string) { recorded = recordedSessionIDs(root) })
	return mergeSessionIDs(ids, listBlergRunnerTmuxSessions(), recorded)
}

// SessionStates returns each managed session's current tracked state, keyed by
// session ID. Sessions whose state the tracker cannot yet characterise (no
// snapshot classified yet) are omitted. Used by the heartbeat to let the server
// reconcile any state change that was dropped in transit.
func (m *Manager) SessionStates() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.trackers))
	for id, tr := range m.trackers {
		if st := tr.State(); st != "" {
			out[id] = st
		}
	}
	if m.agents != nil {
		for id, st := range m.agents.States() {
			out[id] = st
		}
	}
	return out
}

// sanitizedEnviron is os.Environ() with the daemon master token removed and
// extra KEY=VALUE entries appended. It is the ONLY env source any session
// process may start from: the master token is full runner admin, and a
// prompt-injected session must never be able to read it (desktop-safety C1).
// Everything else — including the engine's own credential
// (ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN), without which the engine
// cannot run — is inherited as-is.
//
// What this is NOT: a security boundary against a process running as the
// same uid. A bare-host session can still read /proc/<daemon-pid>/environ,
// the daemon's config file, or ~/.blerg-runner, and recover the master token
// that way. Env hygiene removes the trivial `echo $BLERG_RUNNER_DAEMON_TOKEN`
// leak and the accidental inheritance paths; the Docker sandbox runtime
// (separate pid namespace and filesystem, only the repo mounted) is the
// actual boundary for an untrusted session.
func sanitizedEnviron(extra ...string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+len(extra))
	for _, e := range base {
		// The master token, and a pod's gateway grant (MCPGatewayEnvVar; the pod
		// entrypoint unsets it as well): neither may reach any child process.
		if !strings.HasPrefix(e, "BLERG_RUNNER_DAEMON_TOKEN=") && !strings.HasPrefix(e, MCPGatewayEnvVar+"=") &&
			!strings.HasPrefix(e, RestrictToolsEnvVar+"=") {
			out = append(out, e)
		}
	}
	return append(out, extra...)
}

// reservedExtraEnvKeys are engine credentials a caller's ExtraEnv may not
// set or override: the daemon inherits them from its own environment.
var reservedExtraEnvKeys = map[string]bool{
	"ANTHROPIC_API_KEY":       true,
	"CLAUDE_CODE_OAUTH_TOKEN": true,
	"OPENAI_API_KEY":          true,
}

// validExtraEnvKey reports whether a caller-supplied ExtraEnv key may reach
// a session: non-empty, well-formed (no '=', NUL or newline — each would
// forge a second entry), not BLERG_RUNNER_* (the daemon owns that namespace:
// it is how the master token or a forged session token would be smuggled
// back in) and not an engine credential.
func validExtraEnvKey(k string) bool {
	if k == "" || strings.ContainsAny(k, "=\x00\n") {
		return false
	}
	if strings.HasPrefix(k, "BLERG_RUNNER_") {
		return false
	}
	return !reservedExtraEnvKeys[k]
}

// extraEnvPairs renders the per-session messaging token and caller-supplied
// extra env as KEY=VALUE entries (token first, extra keys sorted so the
// result is deterministic). An empty token yields no entry rather than an
// empty one, so `blerg-runner` falls through to "not configured". Keys that
// fail validExtraEnvKey are dropped silently — ExtraEnv is untrusted input
// from the spawn caller, never a channel for daemon-owned vars.
func extraEnvPairs(sessionToken string, extra map[string]string) []string {
	var pairs []string
	if sessionToken != "" {
		pairs = append(pairs, "BLERG_RUNNER_SESSION_TOKEN="+sessionToken)
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		if validExtraEnvKey(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		pairs = append(pairs, k+"="+extra[k])
	}
	return pairs
}

// sessionEnv returns sanitizedEnviron() augmented with the Blerg Runner vars
// the session needs. BLERG_RUNNER_SESSION_ID is always injected (unique per
// session); BLERG_RUNNER_PREVIEW_URL and BLERG_RUNNER_SERVER_HTTP only when
// the corresponding config field is non-empty. sessionToken (the server-minted
// per-session messaging credential) becomes BLERG_RUNNER_SESSION_TOKEN and
// extra is appended verbatim.
//
// BLERG_RUNNER_DAEMON_TOKEN is never present — not from config, not inherited
// — for ANY session kind: bare host, sandbox, assist, or agent. It used to be
// injected for non-assist sessions; the per-session token replaced it.
//
// ~/.local/bin is prepended to PATH so that the blerg-runner CLI (symlinked
// there by Provision) resolves inside spawned sessions even when the daemon
// was started under a systemd user service with a minimal inherited PATH.
//
// When assist is true the session is an AI-driven ticket agent and
// BLERG_RUNNER_BOARD_ID / BLERG_RUNNER_BOARD_TOKEN are appended too.
func (m *Manager) sessionEnv(sessionID string, assist bool, boardID, boardToken, sessionToken string, extra map[string]string) []string {
	return buildSessionEnv(sessionEnvOpts{
		SessionID:    sessionID,
		PreviewURL:   m.config.PreviewURL,
		ServerHTTP:   m.config.ServerHTTP,
		Assist:       assist,
		BoardID:      boardID,
		BoardToken:   boardToken,
		SessionToken: sessionToken,
		Extra:        extra,
	})
}

// sessionEnvOpts is everything the session environment depends on. It exists
// so Manager (terminal kinds) and AgentHost (agent kind) build the SAME
// environment from one place: agent-kind used to roll its own
// sanitizedEnviron(extraEnvPairs(...)), which is exactly how a board-driven
// session ended up holding a per-session messaging token with no
// BLERG_RUNNER_SESSION_ID, no BLERG_RUNNER_SERVER_HTTP and no PATH prepend to
// spend it with — the blerg-runner CLI was inert in the sessions the board
// drives.
type sessionEnvOpts struct {
	SessionID    string
	PreviewURL   string
	ServerHTTP   string
	Assist       bool
	BoardID      string
	BoardToken   string
	SessionToken string
	Extra        map[string]string
}

func buildSessionEnv(o sessionEnvOpts) []string {
	env := sanitizedEnviron()

	// Prepend ~/.local/bin so the blerg-runner CLI resolves in session subprocesses.
	if home, err := os.UserHomeDir(); err == nil {
		env = prependPathInEnv(env, filepath.Join(home, ".local", "bin"))
	}

	if o.PreviewURL != "" {
		env = append(env, "BLERG_RUNNER_PREVIEW_URL="+o.PreviewURL)
	}
	env = append(env, "BLERG_RUNNER_SESSION_ID="+o.SessionID)
	if o.ServerHTTP != "" {
		env = append(env, "BLERG_RUNNER_SERVER_HTTP="+o.ServerHTTP)
	}
	if o.Assist {
		if o.BoardID != "" {
			env = append(env, "BLERG_RUNNER_BOARD_ID="+o.BoardID)
		}
		if o.BoardToken != "" {
			env = append(env, "BLERG_RUNNER_BOARD_TOKEN="+o.BoardToken)
		}
	}
	return append(env, extraEnvPairs(o.SessionToken, o.Extra)...)
}

func (m *Manager) sendError(sessionID, msg string) {
	errMsg := msg
	_ = m.client.Send(protocol.SessionStateChanged{
		Type:      "session_state_changed",
		SessionID: sessionID,
		Status:    "error",
		Message:   &errMsg,
	})
}

// resolveProjectPath joins repo onto reposRoot and refuses any result that
// escapes reposRoot. Applied to EVERY spawn, not only new_repo: the server
// validates too, but the daemon is the process that actually mounts/chdirs,
// so it must not trust the server here (defence in depth).
func resolveProjectPath(reposRoot, repo string) (string, error) {
	if repo == "" || filepath.IsAbs(repo) {
		return "", fmt.Errorf("invalid folder path %q", repo)
	}
	root := filepath.Clean(reposRoot)
	p := filepath.Clean(filepath.Join(root, repo))
	if p == root || !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid folder path %q", repo)
	}

	// The Clean-based check above only catches ".." segments; a symlink inside
	// reposRoot can point anywhere on disk and still pass it. Re-checking only
	// when the FULL path exists is not enough: since "org/name" became legal,
	// a spawn for a not-yet-created repo skipped the check entirely whenever
	// root/org was a symlink pointing elsewhere — and prepareWorkspace's
	// MkdirAll/clone then landed outside the root. resolveExistingAncestor
	// resolves the deepest EXISTING ancestor and re-joins the rest, so a
	// not-yet-created leaf inherits its parent's containment.
	resolvedRoot, err := resolveExistingAncestor(root)
	if err != nil {
		return "", fmt.Errorf("invalid folder path %q", repo)
	}
	resolvedP, err := resolveExistingAncestor(p)
	if err != nil {
		return "", fmt.Errorf("invalid folder path %q", repo)
	}
	if resolvedP != resolvedRoot && !strings.HasPrefix(resolvedP, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid folder path %q", repo)
	}

	return p, nil
}

// resolveExistingAncestor is filepath.EvalSymlinks for a path that may not
// exist yet: it resolves the deepest existing ancestor and re-joins the
// remaining (not-yet-created) segments to the result. A path with no existing
// ancestor at all resolves to itself — nothing on it can be a symlink. A
// dangling symlink on the way errors, and every caller treats that as a
// rejection.
func resolveExistingAncestor(p string) (string, error) {
	probe := filepath.Clean(p)
	rest := ""
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return filepath.Clean(p), nil
		}
		rest = filepath.Join(filepath.Base(probe), rest)
		probe = parent
	}
	resolved, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, rest), nil
}

// preflightEngine refuses a spawn whose runtime can't start, with a reason a
// human can act on — instead of a tmux/docker failure that surfaces as a bare
// "error" (trial blocker 2). Reports only the binary/image name and the PATH
// value, never a full resolved host path or any secret.
//
// Applies to agent-kind too, the kind every board-driven session uses: on the
// host it runs its driver in-process (no tmux), so only a CLI-shelling driver
// has a binary to check — the native loop has none, and its own
// missing-credential error is the actionable one there. Sandboxed, the engine
// runs inside the container instead, so the image must exist and the binary is
// looked for IN THE IMAGE: the host PATH says nothing about what the container
// can run (and an engine installed only in the image would otherwise be
// refused for no reason).
func (m *Manager) preflightEngine(msg protocol.SpawnSession) error {
	if len(m.config.Command) > 0 { // test override
		return nil
	}
	// OpenClaw is host-only, so a sandboxed spawn is refused whatever the image
	// and binary checks would say — and refused with the reason that is true
	// ("OpenClaw runs only on the host") rather than an image or PATH error the
	// user would then go and fix for nothing. The same string AgentHost.Spawn
	// refuses with, so both gates read identically.
	if msg.Sandbox && msg.Engine == "openclaw" {
		return errors.New(sandboxOpenclawRefusal)
	}
	if msg.Kind == "agent" {
		if m.agents == nil {
			return nil
		}
		name := m.agents.engineBinary(msg)
		if msg.Sandbox {
			if !m.sandboxImagePresent() {
				return missingSandboxImageError()
			}
			if name == "" {
				return nil // native loop: refused at spawn with the spec's reason
			}
			if err := m.sandboxEnginePresent(name); err != nil {
				return fmt.Errorf("%s not found in the sandbox image %s — rebuild the image with the engine installed, or choose This machine", name, sandboxImage())
			}
			return nil
		}
		if name == "" {
			return nil
		}
		return engineOnPath(resolveEngineBinary(name, false))
	}
	if msg.Sandbox {
		if !m.sandboxImagePresent() {
			return missingSandboxImageError()
		}
		return nil
	}
	bin := resolveEngineBinary("claude", false)
	if spec, ok := engineRegistry[msg.Engine]; ok {
		bin = resolveEngineBinary(spec.Binary, false)
	}
	return engineOnPath(bin)
}

// missingSandboxImageError is the one home for the "no sandbox image" reason,
// shared by every kind that needs the container (terminal and, now, agent).
func missingSandboxImageError() error {
	return fmt.Errorf("sandbox image %s missing — run install/desktop/daemon/install.sh install to build it, or pick the Desktop daemon runtime", sandboxImage())
}

// engineOnPath is the one home for the "engine binary missing" reason: the
// text becomes the session's error_reason and travels to the board card.
func engineOnPath(bin string) error {
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("%s not found on PATH of the daemon service (PATH=%s) — install it, or add its directory to the service's PATH and restart the daemon", bin, os.Getenv("PATH"))
	}
	return nil
}

// prepareWorkspace resolves msg.Repo under the daemon's repos root and makes
// sure the directory a session is about to run in actually exists: created for
// a new_repo spawn, cloned when the daemon knows a GitHub org, otherwise
// required to be there already.
//
// The returned error's text is what the session's error reason becomes (Task
// 6 persists it), so it must say something the user can act on — "your repo
// isn't checked out here" is a fixable answer; a session that dies silently
// half a second after a 202 is not.
//
// Shared by every spawn kind. This used to live inline in the terminal branch
// only, which is exactly how agent-kind ended up without it.
func (m *Manager) prepareWorkspace(msg protocol.SpawnSession) (string, error) {
	path, _, err := m.prepareWorkspaceTracked(msg)
	return path, err
}

// createdDirs names the folders one spawn brought into existence: leaf is the
// workspace, top the first path segment that did not exist before (the leaf
// itself, or an org/ parent above it). The zero value means the spawn created
// nothing, and nothing is ever removed for it.
type createdDirs struct{ leaf, top string }

// topMissingAncestor returns the outermost part of p (inside root) that does
// not exist yet, or "" when p exists already.
func topMissingAncestor(root, p string) string {
	root = filepath.Clean(root)
	top := ""
	for cur := filepath.Clean(p); cur != root && strings.HasPrefix(cur, root+string(filepath.Separator)); cur = filepath.Dir(cur) {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		top = cur
	}
	return top
}

// removeCreatedWorkspace undoes a spawn's own folder creation after the spawn
// failed before its session started, so a failed launch does not leave an empty
// folder to show up in the repo picker. It only ever removes directories this
// spawn created (c), from the leaf up to c.top, and only while each is a real
// (non-symlink) directory strictly inside root with nothing in it. A folder
// that existed before, has any content, or lies outside root is left alone.
func removeCreatedWorkspace(root string, c createdDirs) {
	if c.top == "" || c.leaf == "" {
		return
	}
	root = filepath.Clean(root)
	inside := func(p string) bool {
		return p != root && strings.HasPrefix(p, root+string(filepath.Separator))
	}
	leaf, top := filepath.Clean(c.leaf), filepath.Clean(c.top)
	if !inside(leaf) || !inside(top) || (leaf != top && !strings.HasPrefix(leaf, top+string(filepath.Separator))) {
		return
	}
	for p := leaf; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() { // gone, a symlink or a file: not ours to touch
			return
		}
		entries, err := os.ReadDir(p)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(p); err != nil {
			return
		}
		log.Printf("manager: removed the empty folder %s left by a spawn that failed before its session started", p)
		if p == top {
			return
		}
	}
}

// prepareWorkspaceTracked is prepareWorkspace plus what it created, returned on
// failure too, so the caller can clean up when the spawn fails later.
func (m *Manager) prepareWorkspaceTracked(msg protocol.SpawnSession) (string, createdDirs, error) {
	ctx := m.spawnContext(msg.SessionID)
	// Defence in depth: this is the process that actually chdirs into the
	// repo, so it does not trust the server's own validation.
	// One read for the whole spawn, so a concurrent repos-root change can't
	// resolve the path under one root and clone under another.
	root := m.reposRoot()
	projectPath, err := resolveProjectPath(root, msg.Repo)
	if err != nil {
		log.Printf("manager: path traversal rejected for repo %q", msg.Repo)
		return "", createdDirs{}, errors.New("invalid folder path")
	}
	var created createdDirs
	if !msg.NoRepo {
		// Anything at projectPath after this point was made by this spawn.
		if top := topMissingAncestor(root, projectPath); top != "" {
			created = createdDirs{leaf: projectPath, top: top}
		}
	}
	switch {
	case msg.NoRepo:
		// "No repository": an empty scratch folder, created exactly like a
		// new_repo folder and never cloned (first, so nothing below — a
		// CloneFrom, the org clone — can apply to it). Refused unless it is
		// a scratch name, so this flag cannot become a way to mkdir
		// arbitrary folders that then show up in the repo picker.
		if !scratch.Valid(msg.Repo) {
			return "", created, fmt.Errorf("invalid scratch folder name %q", msg.Repo)
		}
		// Always a NEW folder: an existing one (an earlier session's, files
		// and all) is never reused. The path it returns may name a different
		// folder than asked for; handleSpawnSession takes the name from it.
		p, err := createScratchFolder(root, msg.Repo)
		if err != nil {
			return "", created, err
		}
		return p, createdDirs{leaf: p, top: p}, nil
	case msg.CloneFrom != "":
		// A repository the caller named: cloned into exactly msg.Repo, or
		// that folder used when it already is this repository — never an
		// unrelated folder of the same name (EnsureClonedTarget).
		if msg.NewRepo {
			return "", created, errors.New("a new empty folder cannot also be a clone")
		}
		if err := EnsureClonedTarget(ctx, root, CloneTarget{
			Provider: msg.Provider, FullName: msg.CloneFrom, Folder: msg.Repo, Token: msg.GitToken,
			// A Local sandbox session's token clone runs in the sandbox
			// image; on the bare host only when this daemon's owner allowed it.
			Sandbox: msg.Sandbox, HostTokenAllowed: m.config.AllowHostCredentialClone,
		}); err != nil {
			// The error names repositories and folders only; the token is
			// never part of it (EnsureClonedTarget scrubs git's output).
			log.Printf("manager: clone of %s into %q failed: %v", msg.CloneFrom, msg.Repo, err)
			return "", created, err
		}
	case msg.NewRepo:
		if err := os.MkdirAll(projectPath, 0o755); err != nil { //nolint:gosec // new project directory the user asked for inside the repos root (contained by resolveProjectPath); user-visible working tree, not secret state
			log.Printf("manager: mkdir %s: %v", projectPath, err)
			return "", created, err
		}
	case m.config.GithubOrg != "" || strings.Contains(msg.Repo, "/"):
		// EnsureCloned is a no-op when the directory is already there.
		clone := m.ensureClonedFunc
		if clone == nil {
			clone = EnsureCloned
		}
		if err := clone(ctx, root, msg.Repo, msg.Provider, m.config.GithubOrg); err != nil {
			log.Printf("manager: EnsureCloned failed: %v", err)
			return "", created, err
		}
	}
	if _, err := os.Stat(projectPath); err != nil {
		return "", created, fmt.Errorf("repo %q is not checked out under this daemon's repos root (%s) — clone it there, or configure the daemon's GitHub org so it can clone on demand",
			msg.Repo, root)
	}
	return projectPath, created, nil
}

// scratchAttempts bounds how many names createScratchFolder tries: the one
// asked for, then fresh ones derived from it.
const scratchAttempts = 5

// scratchRetry derives the next name to try after a collision; a variable only
// so a test can make every attempt collide.
var scratchRetry = scratch.Retry

// createScratchFolder creates a new, empty scratch folder directly under root
// and returns its path. os.Mkdir, not MkdirAll: the folder is one path segment
// under the root, so a missing root is an error too, and — the point — an
// existing folder of that name is an error rather than silently reused. On a
// collision it tries a fresh name derived from the one asked for
// (scratch.Retry), and gives up with a clear error after scratchAttempts.
func createScratchFolder(root, name string) (string, error) {
	for range scratchAttempts {
		p, err := resolveProjectPath(root, name)
		if err != nil {
			return "", errors.New("invalid folder path")
		}
		err = os.Mkdir(p, 0o755) //nolint:gosec // scratch project directory inside the repos root (contained by resolveProjectPath); user-visible working tree, not secret state
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			log.Printf("manager: mkdir %s: %v", p, err)
			return "", fmt.Errorf("could not create the scratch folder %s: %w", p, err)
		}
		log.Printf("manager: scratch folder %s already exists — trying another name", p)
		name = scratchRetry(name)
	}
	return "", fmt.Errorf("could not create a new scratch folder under %s: %d names were all taken", root, scratchAttempts)
}

// spawnQueueDepth bounds the queue. Spawns are user-initiated and rare; the
// depth exists so a wedged worker reports "retry" instead of growing without
// bound.
const spawnQueueDepth = 32

// startSpawnWorker creates the spawn queue and the single goroutine that
// drains it. One worker, not one goroutine per spawn: spawns keep the arrival
// order and the one-at-a-time semantics they had on the read pump, so
// nothing downstream (the session maps, the recovery records, the
// session_started / session_state_changed a spawn emits) sees concurrency it
// did not see before.
func (m *Manager) startSpawnWorker() {
	m.spawns = make(chan []byte, spawnQueueDepth)
	m.pendingSpawns = make(map[string]bool)
	m.ensureClonedFunc = EnsureCloned
	// A daemon killed mid-clone never ran that clone's cleanup.
	sweepCloneLeftovers(m.reposRoot(), time.Now())
	go func() {
		for raw := range m.spawns {
			m.handleSpawnSession(raw)
		}
	}()
}

// enqueueSpawn is what the websocket read pump dispatches to. It must return
// promptly: everything slow about a spawn happens on the worker.
func (m *Manager) enqueueSpawn(raw []byte) {
	var msg protocol.SpawnSession
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("manager: bad spawn_session: %v", err)
		return
	}
	m.mu.Lock()
	m.pendingSpawns[msg.SessionID] = false
	m.mu.Unlock()
	select {
	case m.spawns <- raw:
	default:
		m.mu.Lock()
		delete(m.pendingSpawns, msg.SessionID)
		m.mu.Unlock()
		m.sendError(msg.SessionID, "daemon is already preparing its queue limit of sessions — retry in a moment")
	}
}

// spawnKilled reports whether a kill_session arrived while this spawn was
// queued or preparing its workspace.
func (m *Manager) spawnKilled(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pendingSpawns[sessionID]
}

func (m *Manager) clearPendingSpawn(sessionID string) {
	m.mu.Lock()
	delete(m.pendingSpawns, sessionID)
	ctl, ok := m.spawnCancels[sessionID]
	delete(m.spawnCancels, sessionID)
	m.mu.Unlock()
	if ok {
		ctl.cancel()
	}
}

// spawnCtl is one in-flight spawn's cancellable context.
type spawnCtl struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// beginSpawn gives sessionID's spawn a context a kill_session cancels —
// already cancelled when the kill overtook the spawn in the queue.
func (m *Manager) beginSpawn(sessionID string) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if m.spawnCancels == nil {
		m.spawnCancels = make(map[string]spawnCtl)
	}
	m.spawnCancels[sessionID] = spawnCtl{ctx: ctx, cancel: cancel}
	killed := m.pendingSpawns[sessionID]
	m.mu.Unlock()
	if killed {
		cancel()
	}
}

// spawnContext is sessionID's spawn context, or a background one for a
// workspace prepared outside a spawn (tests).
func (m *Manager) spawnContext(sessionID string) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctl, ok := m.spawnCancels[sessionID]; ok {
		return ctl.ctx
	}
	return context.Background()
}

func (m *Manager) handleSpawnSession(raw []byte) {
	var msg protocol.SpawnSession
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("manager: bad spawn_session: %v", err)
		return
	}
	// Resolve the project path and make sure the workspace exists. Applied to
	// EVERY spawn, agent-kind included: agent-kind used to branch off above
	// this, so a board-driven session for a repo that was not cloned yet got a
	// 202 and then died on AgentHost's os.Stat — the spawn black hole this
	// plan set out to kill.
	m.beginSpawn(msg.SessionID)
	defer m.clearPendingSpawn(msg.SessionID)
	// model/effort become engine CLI arguments: refuse a bad one before any
	// workspace work, rather than trusting the server to have checked.
	if problem := spawnModelProblem(msg); problem != "" {
		log.Printf("manager: %s: %s", msg.SessionID, problem)
		m.sendError(msg.SessionID, problem)
		return
	}
	// An MCP gateway grant is honoured only by a sandboxed agent-kind Claude
	// session. The server checks the same, but this daemon is the process that
	// would run it: a terminal session has no place to apply the tool
	// allow-list, and a bare-host session is a full shell as the developer.
	if problem := mcpGatewaySpawnProblem(msg); problem != "" {
		log.Printf("manager: %s: %s", msg.SessionID, problem)
		m.sendError(msg.SessionID, problem)
		return
	}
	projectPath, created, err := m.prepareWorkspaceTracked(msg)
	// A spawn that fails before its session exists must not leave a folder
	// it created behind (it would show up, empty, in the repo picker).
	// Cleared once the session is handed over.
	cleanupCreated := created.top != ""
	defer func() {
		if cleanupCreated {
			removeCreatedWorkspace(m.reposRoot(), created)
		}
	}()
	// The git token served the clone and nothing else: drop it before msg
	// reaches anything that keeps or forwards it (the session record, the
	// agent host, a recovery), so it cannot outlive this spawn.
	msg.GitToken = ""
	if err != nil {
		if m.spawnKilled(msg.SessionID) {
			// Killed mid-clone: the clone was stopped for that kill, and the
			// session is gone — nothing to report as a failure.
			log.Printf("manager: spawn %s abandoned — killed while its workspace was being prepared", msg.SessionID)
			return
		}
		m.sendError(msg.SessionID, err.Error())
		return
	}
	// The workspace step can run for minutes, and the read pump is free now,
	// so a kill_session can arrive mid-clone. Stop here rather than creating
	// a session the user already killed.
	if m.spawnKilled(msg.SessionID) {
		log.Printf("manager: spawn %s abandoned — killed while its workspace was being prepared", msg.SessionID)
		return
	}
	// Tell the agent host the resolved path rather than making it re-derive
	// one; its own resolveProjectPath fallback stays for callers that pass
	// none (recovery).
	msg.ProjectPath = projectPath
	if msg.NoRepo {
		// The scratch folder may have been created under another name than
		// asked for (a collision): the session, its recovery record and its
		// session_started all name the folder that actually exists.
		msg.Repo = filepath.Base(projectPath)
	}

	// Preflight the runtime before creating anything (Task 1's
	// resolveProjectPath has already run above; this comes after it, per the
	// pre-flight ruling) — a missing engine binary or sandbox image should
	// fail with a reason a human can act on, not a bare tmux/docker/driver
	// error later. Agent-kind is included: it used to branch off below this,
	// which left the headline board-driven flow with no preflight at all.
	if err := m.preflightEngine(msg); err != nil {
		log.Printf("manager: %v", err)
		m.sendError(msg.SessionID, err.Error())
		return
	}

	if msg.Kind == "agent" {
		if m.agents == nil {
			m.sendError(msg.SessionID, "agent sessions unavailable: no ANTHROPIC_API_KEY on this daemon")
			return
		}
		cleanupCreated = false // AgentHost owns the workspace from here
		m.agents.Spawn(msg)
		return
	}

	// Build environment.
	env := m.sessionEnv(msg.SessionID, msg.Assist, msg.BoardID, msg.BoardToken, msg.SessionToken, msg.ExtraEnv)

	cols, rows := msg.Cols, msg.Rows
	if cols == 0 {
		cols = 220
	}
	if rows == 0 {
		rows = 50
	}

	command := m.config.Command
	if len(command) == 0 {
		var inner []string
		if spec, ok := engineRegistry[msg.Engine]; ok {
			inner = buildTerminalCommand(spec, msg.Model, msg.Effort, msg.DangerouslySkipPermissions, msg.Sandbox)
		} else {
			inner = claudeCommand(msg.SessionID, msg.Model, msg.Effort, msg.DangerouslySkipPermissions, false, msg.Sandbox)
		}
		// Run the engine inside a persistent, detached tmux session so the process
		// survives a daemon restart; we then attach to it under the PTY. The
		// daemon reattaches to the same session on reconnect. Sandbox runs that
		// tmux session inside a throwaway container instead of on the host —
		// see sandbox.go; everything downstream (attach, capture, kill) is
		// registry-transparent so it needs no branching here beyond create.
		var createErr error
		if msg.Sandbox {
			createErr = createDockerSandbox(msg.SessionID, projectPath, inner, env, m.config.sandboxOptions())
		} else {
			createErr = createTmuxSession(msg.SessionID, projectPath, inner, env)
		}
		if createErr != nil {
			log.Printf("manager: create session %s failed: %v", msg.SessionID, createErr)
			m.sendError(msg.SessionID, createErr.Error())
			return
		}
		// Persist a recovery record so the daemon can recreate this session after
		// a hard restart (tmux server death). Best-effort: a write failure must
		// not abort the spawn.
		now := time.Now().UTC().Format(time.RFC3339)
		if err := m.writeRecord(SessionRecord{
			Version:                    recordSchemaVersion,
			SessionID:                  msg.SessionID,
			Repo:                       msg.Repo,
			ProjectPath:                projectPath,
			Model:                      msg.Model,
			Effort:                     msg.Effort,
			DangerouslySkipPermissions: msg.DangerouslySkipPermissions,
			Sandbox:                    msg.Sandbox,
			Engine:                     msg.Engine,
			Title:                      msg.Title,
			InitialPrompt:              msg.InitialPrompt,
			Assist:                     msg.Assist,
			BoardID:                    msg.BoardID,
			TicketID:                   msg.TicketID,
			BoardToken:                 msg.BoardToken,
			SessionToken:               msg.SessionToken,
			ExtraEnv:                   msg.ExtraEnv,
			CreatedAt:                  now,
			UpdatedAt:                  now,
		}); err != nil {
			log.Printf("manager: write session record %s: %v", msg.SessionID, err)
		}
		command = tmuxAttachCommand(msg.SessionID)
	}

	sess, err := NewSession(msg.SessionID, projectPath, cols, rows, env, command)
	if err != nil {
		log.Printf("manager: spawn_session %s failed: %v", msg.SessionID, err)
		m.sendError(msg.SessionID, err.Error())
		return
	}

	cleanupCreated = false // the session started: its folder is real now
	tracker := NewStateTracker(msg.SessionID, m.client)
	metaParser := NewMetaParser(msg.SessionID, m.client)
	m.mu.Lock()
	m.sessions[msg.SessionID] = sess
	m.trackers[msg.SessionID] = tracker
	m.metaParsers[msg.SessionID] = metaParser
	m.mu.Unlock()

	// Notify server the session started.
	_ = m.client.Send(protocol.SessionStarted{
		Type:        "session_started",
		SessionID:   msg.SessionID,
		ProjectPath: projectPath,
		Repo:        msg.Repo,
		Title:       msg.Title,
		Model:       msg.Model,
		Cols:        cols,
		Rows:        rows,
	})

	m.streamSession(msg.SessionID, sess)
	m.startStatePoller(msg.SessionID, tracker)

	m.queueInitialPrompt(msg.SessionID, msg.InitialPrompt)
}

// queueInitialPrompt queues a freshly spawned or recovered session's initial
// prompt rather than writing it after a blind delay: Claude's startup time
// (plugin/hook loading) is unbounded, and a prompt written before its raw-mode
// input is ready can land as inert text with no Enter — the human then has to
// hit it manually. startStatePoller flushes this slot the first time it
// observes the session idle (ready for input), however long that takes.
// No-op for an empty prompt.
func (m *Manager) queueInitialPrompt(sessionID, prompt string) {
	if prompt == "" {
		return
	}
	m.mu.Lock()
	m.noteReplies[sessionID] = prompt
	m.mu.Unlock()
}

// streamSession forwards a session's PTY output to the server until the process
// exits, then removes the session and emits session_ended. Running in one
// goroutine guarantees session_ended is sent after all session_output.
func (m *Manager) streamSession(id string, sess *Session) {
	m.mu.Lock()
	metaParser := m.metaParsers[id]
	m.mu.Unlock()

	go func() {
		for chunk := range sess.OutputCh {
			if metaParser != nil {
				metaParser.Process(chunk)
			}
			encoded := base64.StdEncoding.EncodeToString(chunk)
			_ = m.client.Send(protocol.SessionOutput{
				Type:      "session_output",
				SessionID: id,
				Data:      encoded,
				Seq:       sess.NextSeq(),
			})
		}
		// OutputCh is closed only after the process exits, so exit code is set.
		// Removing the session here also signals the state poller to stop.
		m.mu.Lock()
		delete(m.sessions, id)
		delete(m.trackers, id)
		delete(m.metaParsers, id)
		if t := m.refreshTimers[id]; t != nil {
			t.Stop()
			delete(m.refreshTimers, id)
		}
		m.mu.Unlock()
		// Remove the recovery record now that the session has ended naturally.
		// Best-effort: safe to call when no record exists (e.g. test path).
		m.deleteRecord(id)
		_ = m.client.Send(protocol.SessionEnded{
			Type:      "session_ended",
			SessionID: id,
			ExitCode:  sess.exitCode,
			Signal:    sess.signal,
		})
	}()
}

// startStatePoller periodically snapshots the session's screen and feeds the
// classification to its tracker, which emits session_state_changed on
// transitions. It exits once the session is removed (streamSession's cleanup),
// so its lifetime tracks the session. No-op when a custom Command is configured
// (tests, not tmux-backed).
func (m *Manager) startStatePoller(id string, tracker *StateTracker) {
	if len(m.config.Command) > 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(statePollInterval)
		defer ticker.Stop()
		for range ticker.C {
			m.mu.Lock()
			_, alive := m.sessions[id]
			capFn := m.capturePaneFunc
			m.mu.Unlock()
			if !alive {
				return
			}
			if capFn == nil {
				capFn = capturePane
			}
			screen, err := capFn(id)
			if err != nil {
				continue // session may be momentarily unavailable; keep last state
			}
			state := classifyScreen(screen)
			tracker.Update(state)
			if state == "idle" {
				m.flushPendingNoteReply(id)
			}
		}
	}()
}

// ReattachSessions reattaches to persistent tmux sessions left by a prior daemon
// run, resuming output streaming for each. It is idempotent — already-managed
// sessions are skipped — so it is safe to call on every (re)connection. After
// reattaching live tmux sessions it performs a recovery pass: for each record
// whose session was lost in a hard restart (tmux server death), it recreates the
// tmux session and resumes Claude via --resume. No-op when a custom Command is
// configured (tests, not tmux-backed).
func (m *Manager) ReattachSessions() {
	// A tmux server that outlived an older daemon (or was started from the
	// user's shell) may hold the master token or a stale session token in
	// its global env, which every later pane would inherit. Unset them
	// before any session is created or recovered on it. Best-effort.
	if len(m.config.Command) == 0 {
		scrubTmuxGlobalEnv()
	}
	// Agent sessions survive in-process across reconnects — just flush any
	// events the server hasn't acked yet.
	if m.agents != nil {
		m.agents.ResendPending()
	}
	// Ship the user's agent config to the server so runner pods (which have
	// no ~/.claude) can download it. Best-effort, off the reattach path.
	if m.config.ServerHTTP != "" && m.config.DaemonToken != "" && len(m.config.Command) == 0 {
		go func() {
			home, err := os.UserHomeDir()
			if err != nil {
				return
			}
			if err := UploadConfigBundle(context.Background(), m.config.ServerHTTP, m.config.DaemonToken, home); err != nil {
				log.Printf("manager: agent-config upload: %v", err)
			}
		}()
	}
	if len(m.config.Command) > 0 {
		return
	}
	// Serialize whole passes: onConnect fires this without awaiting it, so a
	// rapid disconnect/reconnect could otherwise run two passes concurrently.
	m.reattachMu.Lock()
	defer m.reattachMu.Unlock()

	liveIDs := mergeSessionIDs(listBlergRunnerTmuxSessions(), listSandboxedTmuxSessions())
	for _, id := range liveIDs {
		m.mu.Lock()
		_, exists := m.sessions[id]
		m.mu.Unlock()
		if exists {
			continue
		}
		m.reattachSession(id)
	}

	// Recovery pass: recreate tmux sessions for records abandoned by a hard restart.
	m.mu.Lock()
	managedIDs := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		managedIDs = append(managedIDs, id)
	}
	m.mu.Unlock()

	var recs []SessionRecord
	m.config.ReposRootSetting.WithRoot(func(root string) { recs = listSessionRecords(root) })
	for _, rec := range recoverableRecords(recs, liveIDs, managedIDs) {
		m.recoverSession(rec)
	}

	// Last, once everything reattachable has been reattached and everything
	// recoverable recovered: a sandboxed agent session's container survives the
	// daemon that made it, and nothing above will ever adopt one (it holds no
	// tmux, and the engine subprocess inside it died with the old daemon). Left
	// alone it keeps its memory ceiling, its mounts and its name forever, so
	// the next session with that id cannot even start without a collision.
	sweepOrphanSandboxContainers(m.ownsSession)
}

// ownsSession reports whether this daemon is currently managing sessionID, as
// either a tmux-backed session or an in-process agent session. It is the
// "leave this alone" test for cleanup passes that work from container names.
func (m *Manager) ownsSession(sessionID string) bool {
	m.mu.Lock()
	_, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if ok {
		return true
	}
	if m.agents != nil {
		for _, id := range m.agents.ActiveIDs() {
			if id == sessionID {
				return true
			}
		}
	}
	return false
}

// reattachSession opens a fresh PTY attached to an existing tmux session and
// resumes streaming its output under the original session ID. It does not send
// session_started — the server already has the row from the original spawn.
func (m *Manager) reattachSession(id string) {
	// env is used only by tmuxAttachCommand (the attach-client subprocess); it does
	// not reach the already-running Claude process. We still use sessionEnv for
	// consistency so the attach client's env matches what Claude was spawned with.
	// Assist=false and no session token here: board vars and the messaging
	// token are not needed by the attach client (the engine inside tmux keeps
	// the env it was created with).
	env := m.sessionEnv(id, false, "", "", "", nil)

	// Size is provisional; the browser sends a resize on subscribe.
	sess, err := NewSession(id, m.reposRoot(), 220, 50, env, tmuxAttachCommand(id))
	if err != nil {
		log.Printf("manager: reattach session %s failed: %v", id, err)
		return
	}

	tracker := NewStateTracker(id, m.client)
	metaParser := NewMetaParser(id, m.client)
	m.mu.Lock()
	m.sessions[id] = sess
	m.trackers[id] = tracker
	m.metaParsers[id] = metaParser
	m.mu.Unlock()

	log.Printf("manager: reattached to persistent session %s", id)
	m.streamSession(id, sess)
	m.startStatePoller(id, tracker)
}

// recoverSession recreates a tmux session for an abandoned record and resumes
// the original session ID. If the transcript is missing (a hard restart
// caught the session before the engine's first write) — or the record is a
// codex, hermes, or openclaw session, none of which has an equivalent way to
// resume the interactive tmux case — it spawns fresh and injects the stored
// initial prompt instead.
func (m *Manager) recoverSession(rec SessionRecord) {
	var inner []string
	resume := false
	if spec, ok := engineRegistry[rec.Engine]; ok {
		inner = buildTerminalCommand(spec, rec.Model, rec.Effort, rec.DangerouslySkipPermissions, rec.Sandbox)
	} else {
		resume = transcriptExists(rec.SessionID)
		inner = claudeCommand(rec.SessionID, rec.Model, rec.Effort, rec.DangerouslySkipPermissions, resume, rec.Sandbox)
	}

	// Records written before SessionToken existed recover with no token: the
	// session runs but cannot message (it never could without the master
	// token, which is gone for good).
	env := m.sessionEnv(rec.SessionID, rec.Assist, rec.BoardID, rec.BoardToken, rec.SessionToken, rec.ExtraEnv)

	var createErr error
	if rec.Sandbox {
		createErr = createDockerSandbox(rec.SessionID, rec.ProjectPath, inner, env, m.config.sandboxOptions())
	} else {
		createErr = createTmuxSession(rec.SessionID, rec.ProjectPath, inner, env)
	}
	if createErr != nil {
		log.Printf("manager: recover %s: create session failed: %v", rec.SessionID, createErr)
		return
	}
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := m.writeRecord(rec); err != nil {
		log.Printf("manager: recover %s: update record: %v", rec.SessionID, err)
	}

	m.reattachSession(rec.SessionID) // attaches PTY, registers tracker/parser, streams, polls

	// reattachSession has no return value; it leaves m.sessions[id] unset if the
	// PTY failed to open. A nil session here means recovery did not actually take
	// hold — the record stays on disk so the next reconnect retries.
	m.mu.Lock()
	sess := m.sessions[rec.SessionID]
	m.mu.Unlock()
	if sess == nil {
		log.Printf("manager: recover %s: reattach failed; will retry on next reconnect", rec.SessionID)
		return
	}

	log.Printf("manager: recovered session %s (resume=%v)", rec.SessionID, resume)

	if !resume {
		m.queueInitialPrompt(rec.SessionID, rec.InitialPrompt)
	}
}

func (m *Manager) handleSendInput(raw []byte) {
	var msg protocol.SendInput
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("manager: bad send_input: %v", err)
		return
	}
	m.mu.Lock()
	sess, ok := m.sessions[msg.SessionID]
	m.mu.Unlock()
	if !ok {
		log.Printf("manager: send_input for unknown session %s", msg.SessionID)
		return
	}

	// Data is base64 encoded by the sender.
	data, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		log.Printf("manager: send_input bad base64: %v", err)
		return
	}

	// claude runs directly as the tmux pane's process (no shell in between), so
	// there's no job-control layer to "fg" it back from a SIGTSTP. Ctrl+Z (0x1A)
	// has wedged the Ink TUI's raw-mode input handling in practice, so drop it
	// rather than forward it.
	data = bytes.ReplaceAll(data, []byte{0x1a}, nil)
	if len(data) == 0 {
		return
	}

	// State is derived from screen snapshots, so we don't nudge it here: the next
	// poll reflects whatever the input produced (a turn starting → running, or
	// just keystrokes echoing at an idle prompt → still idle).
	if err := sess.Write(data); err != nil {
		log.Printf("manager: send_input write error: %v", err)
	}
}

// injectBytes strips CR/LF from text and appends exactly one \r (the Enter byte)
// so the resulting bytes, when written to the PTY, submit the text as a new user
// turn in Claude. Stripping is critical: an embedded newline would submit early,
// splitting the message across two separate turns.
func injectBytes(text string) []byte {
	stripped := strings.NewReplacer("\r", "", "\n", "").Replace(text)
	return []byte(stripped + "\r")
}

// handleInjectNoteReply handles the server→daemon inject_note_reply message.
// If the session's current screen classifies as "idle", the reply is injected
// immediately; otherwise it is stored in the single-slot pending map (latest-wins)
// and flushed by startStatePoller on the next idle observation.
func (m *Manager) handleInjectNoteReply(raw []byte) {
	var msg protocol.InjectNoteReply
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("manager: bad inject_note_reply: %v", err)
		return
	}

	// Agent sessions take note replies as ordinary user messages — no PTY
	// injection or idle-classification gymnastics needed.
	if m.agents != nil && m.agents.Has(msg.SessionID) {
		m.agents.UserMessage(msg.SessionID, msg.Text, "note_reply")
		return
	}

	m.mu.Lock()
	sess, ok := m.sessions[msg.SessionID]
	capFn := m.capturePaneFunc
	m.mu.Unlock()

	if !ok {
		log.Printf("manager: inject_note_reply for unknown session %s", msg.SessionID)
		return
	}

	// If we can't capture the screen (no capturePaneFunc in tests, or capture error),
	// queue the reply conservatively so it is not lost.
	if capFn == nil {
		m.mu.Lock()
		m.noteReplies[msg.SessionID] = msg.Text
		m.mu.Unlock()
		return
	}
	screen, err := capFn(msg.SessionID)
	if err != nil {
		log.Printf("manager: inject_note_reply capturePane %s: %v; queuing", msg.SessionID, err)
		m.mu.Lock()
		m.noteReplies[msg.SessionID] = msg.Text
		m.mu.Unlock()
		return
	}

	if classifyScreen(screen) == "idle" {
		// Evict any stale queued reply before injecting so the state poller
		// can't also flush it on the next idle tick (double-inject guard).
		// Release the lock before sess.Write to avoid holding it across I/O.
		m.mu.Lock()
		delete(m.noteReplies, msg.SessionID)
		m.mu.Unlock()
		if err := sess.Write(injectBytes(msg.Text)); err != nil {
			log.Printf("manager: inject_note_reply write %s: %v", msg.SessionID, err)
		}
		return
	}

	// Not idle (running or waiting) — queue latest-wins.
	m.mu.Lock()
	m.noteReplies[msg.SessionID] = msg.Text
	m.mu.Unlock()
}

// flushPendingNoteReply pops and writes any pending note reply for id. It is
// called by startStatePoller on each raw "idle" observation (not the debounced
// StateTracker transition) so the reply lands promptly once the session is free.
// Clear-on-consume: the slot is deleted before writing to prevent re-injection
// on subsequent idle ticks even if Write fails.
func (m *Manager) flushPendingNoteReply(id string) {
	m.mu.Lock()
	text, ok := m.noteReplies[id]
	if ok {
		delete(m.noteReplies, id)
	}
	sess := m.sessions[id]
	m.mu.Unlock()

	if !ok || sess == nil {
		return
	}
	if err := sess.Write(injectBytes(text)); err != nil {
		log.Printf("manager: flushPendingNoteReply write %s: %v", id, err)
	}
}

func (m *Manager) handleKillSession(raw []byte) {
	var msg protocol.KillSession
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("manager: bad kill_session: %v", err)
		return
	}
	// A spawn that is still queued or preparing its workspace has nothing to
	// kill yet; mark it so the worker abandons it instead of creating a
	// session the user already killed.
	m.mu.Lock()
	if _, pending := m.pendingSpawns[msg.SessionID]; pending {
		m.pendingSpawns[msg.SessionID] = true
	}
	// ...and stop a clone it is running now, rather than after it finishes.
	if ctl, ok := m.spawnCancels[msg.SessionID]; ok {
		if m.pendingSpawns == nil {
			m.pendingSpawns = make(map[string]bool)
		}
		m.pendingSpawns[msg.SessionID] = true
		ctl.cancel()
	}
	m.mu.Unlock()

	if m.agents != nil && m.agents.Has(msg.SessionID) {
		m.agents.Kill(msg.SessionID)
		return
	}
	// Tear down the recovery record and the persistent tmux session
	// unconditionally — even if we are not currently managing this session. A kill
	// can arrive during the post-restart reconnect window, before ReattachSessions
	// has re-registered the session; if we skipped this cleanup, the surviving
	// record (or live tmux session) would resurrect a session the user explicitly
	// killed on the next reconnect. Both calls are best-effort no-ops when the
	// target is absent. tmux teardown is skipped when not tmux-backed (tests).
	m.deleteRecord(msg.SessionID)
	if len(m.config.Command) == 0 {
		// Killing the persistent tmux session terminates the Claude process; the
		// resulting EOF on any attached client PTY ends the session via streamSession.
		killTmuxSession(msg.SessionID)
	}

	m.mu.Lock()
	sess, ok := m.sessions[msg.SessionID]
	m.mu.Unlock()
	if !ok {
		log.Printf("manager: kill_session for unknown session %s (record and tmux cleared)", msg.SessionID)
		return
	}
	sess.Kill()
}

func (m *Manager) handleResizeSession(raw []byte) {
	var msg protocol.ResizeSession
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("manager: bad resize_session: %v", err)
		return
	}
	// Ignore degenerate sizes (an unlaid-out terminal element's gridCount floor) so
	// they can't shrink the pane and break state classification. Keep the last size.
	if msg.Cols < minResizeCols || msg.Rows < minResizeRows {
		log.Printf("manager: ignoring degenerate resize %dx%d for session %s", msg.Cols, msg.Rows, msg.SessionID)
		return
	}
	m.mu.Lock()
	sess, ok := m.sessions[msg.SessionID]
	m.mu.Unlock()
	if !ok {
		log.Printf("manager: resize_session for unknown session %s", msg.SessionID)
		return
	}
	if err := sess.Resize(msg.Cols, msg.Rows); err != nil {
		log.Printf("manager: resize error: %v", err)
	}
	// Repaint after the resize settles to clear xterm residue (see scheduleRefresh).
	m.scheduleRefresh(msg.SessionID)
}

// handleRequestScrollback captures the session's tmux scrollback and sends it to
// the server (which fans it out to the requesting browser). This is the
// authoritative source of scroll-back history — it works for full-screen /
// alt-screen TUIs that the raw PTY-stream replay cannot reconstruct.
func (m *Manager) handleRequestScrollback(raw []byte) {
	var msg protocol.RequestScrollback
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.Printf("manager: bad request_scrollback: %v", err)
		return
	}
	m.mu.Lock()
	_, ok := m.sessions[msg.SessionID]
	fn := m.scrollbackFunc
	modeFn := m.modeSyncFunc
	m.mu.Unlock()
	if !ok || fn == nil {
		return
	}
	text, err := fn(msg.SessionID, msg.MaxLines)
	if err != nil {
		log.Printf("manager: scrollback capture %s: %v", msg.SessionID, err)
		return
	}
	// Mode-sync preamble rides only on ModePrefix here — never as session_output.
	// session_output is persisted to the event log and Seq-numbered by the PTY
	// stream goroutine; session_scrollback is fan-out-only, exactly where a
	// point-in-time snapshot like this belongs.
	var modePrefix string
	if modeFn != nil {
		if seq := modeFn(msg.SessionID); seq != "" {
			modePrefix = base64.StdEncoding.EncodeToString([]byte(seq))
		}
	}
	_ = m.client.Send(protocol.SessionScrollback{
		Type:       "session_scrollback",
		SessionID:  msg.SessionID,
		Data:       base64.StdEncoding.EncodeToString([]byte(text)),
		ModePrefix: modePrefix,
	})
}

// scheduleRefresh (re)arms a per-session debounce timer that, when it fires,
// forces a full tmux repaint of the session's client. Each resize resets the
// timer, so a burst coalesces into one repaint after the resizes stop. Safe to
// call for non-tmux sessions: the default refreshFunc no-ops when no tmux client
// exists, and it is nil in unit tests unless injected.
func (m *Manager) scheduleRefresh(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.refreshTimers[id]; t != nil {
		t.Stop()
	}
	m.refreshTimers[id] = time.AfterFunc(m.refreshDebounce, func() {
		m.mu.Lock()
		delete(m.refreshTimers, id)
		fn := m.refreshFunc
		m.mu.Unlock()
		if fn != nil {
			fn(id)
		}
	})
}
