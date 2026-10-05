package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/agent/anthropic"
	"github.com/blerglab/blerg-ai/runner/internal/agent/skills"
	"github.com/blerglab/blerg-ai/runner/internal/agent/tools"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// AgentHostConfig configures agent-kind session hosting inside the daemon.
type AgentHostConfig struct {
	// SpawnKilled, when set, reports whether a kill_session arrived for a spawn that is still being prepared.
	// The Manager sets it; the cluster pod has no such window. It is checked after the plugin install, which
	// can run for minutes after the Manager's own kill checks.
	SpawnKilled func(sessionID string) bool
	// ClaudeCode switches sessions to the subscription-billed Claude Code
	// driver (headless `claude -p`) instead of the native API loop. Set when
	// CLAUDE_CODE_OAUTH_TOKEN is present and no ANTHROPIC_API_KEY is.
	ClaudeCode bool
	// PreferClaudeCLI (desktop daemon): a Claude agent session runs the
	// user's own `claude` CLI — their Claude Code login — whenever it is on
	// the daemon's PATH, falling back to the native loop only when it is not
	// and an API key is set. Like every agent session it runs without
	// permission prompts (`--dangerously-skip-permissions`). See
	// chooseAgentDriver.
	PreferClaudeCLI bool
	ReposRoot       string
	// ReposRootFn, when set, is the live repos root (the desktop daemon's
	// changeable setting) and wins over ReposRoot.
	ReposRootFn func() string
	ServerHTTP  string // messages API base, e.g. http://server:8080
	DaemonToken string
	APIKey      string         // ANTHROPIC_API_KEY
	HomeDir     string         // "" → os.UserHomeDir()
	Provider    agent.Provider // override for tests; nil → anthropic.New(APIKey, "")
	BudgetUSD   float64
	Pricing     map[string]agent.Price
	// OnTurnDone fires after each completed turn (runner: wip safety push).
	OnTurnDone func(sessionID string)
	// Sandbox is the network/CLI-mount setup a sandboxed session's container
	// gets (sandbox.go). Zero value — the cluster pod's — joins no network.
	Sandbox SandboxOptions
	// Plugins is the daemon's plugin workshop (pluginworkshop.go), where a
	// session's always-on plugins (SpawnSession.Plugins) are installed. nil —
	// the cluster pod's, and a daemon without a state dir — loads none.
	Plugins *pluginWorkshop
}

// reposRoot is the repos root in effect now.
func (c AgentHostConfig) reposRoot() string {
	if c.ReposRootFn != nil {
		return c.ReposRootFn()
	}
	return c.ReposRoot
}

// AgentHost manages agent-kind sessions inside the daemon: one Loop per
// session, events shipped to the server over the existing WS with
// client-event-ID idempotency and resend-on-reconnect.
type AgentHost struct {
	sender Sender
	cfg    AgentHostConfig

	mu       sync.Mutex
	sessions map[string]*agentSession
}

type agentSession struct {
	loop    sessionDriver
	cancel  context.CancelFunc
	emitter *wsEmitter
	status  atomic.Value // last status string for heartbeats
	skills  []skills.Skill
	// sandboxed marks a session whose engine runs inside a container, so Kill
	// tears the container down the way killTmuxSession does for a terminal one.
	sandboxed bool
	plugins   bool // holds a plugin snapshot in the workshop (released on Kill)
	// mcp is the session's MCP gateway config file (nil: no grant), removed
	// when the session ends.
	mcp *mcpConfigFile
	// engine is the session's engine id ("" = Claude): the rules every
	// in-session model/effort change is checked against (changeModel).
	engine string
}

// The two reasons a sandboxed agent-kind spawn is refused: the Claude
// engine in the sandbox runs Claude Code inside the container, which needs a
// credential the container can see, and OpenClaw stays host-only.
const (
	sandboxNoClaudeCredentialRefusal = "no Claude login or API key is available to the sandbox — run `claude` login on this machine, set ANTHROPIC_API_KEY for the daemon, pick Codex/Hermes, or choose This machine"
	sandboxOpenclawRefusal           = "OpenClaw runs only on the host"
	// sandboxUnsupportedDriverRefusal covers the branch the two above are
	// meant to make unreachable: a driver that cannot be routed into the
	// container got past them. Refusing beats running the engine on the host
	// under a "sandboxed" label, and the text claims nothing more than it knows.
	sandboxUnsupportedDriverRefusal = "this engine cannot run in the sandbox"
	// mcpGatewayNeedsClaudeRefusal: a grant is delivered as Claude Code flags
	// (--mcp-config, --strict-mcp-config, --tools); any other driver would
	// ignore it and run with every tool and no MCP, so it is refused instead.
	mcpGatewayNeedsClaudeRefusal = "an MCP gateway grant needs the Claude Code engine"
	// restrictToolsNeedsClaudeRefusal: the same for the tool restriction of an
	// unattended session, which is Claude Code flags too.
	restrictToolsNeedsClaudeRefusal = "a restricted (unattended) session needs the Claude Code engine"
)

// sandboxCredential is how a sandboxed session's Claude credential reaches the
// container.
//
// args are `-e NAME` docker-run arguments — the variable's NAME only, never its
// value: docker then reads the value from its own environment. That keeps the
// secret out of the daemon's argv, which every other user of the machine can
// read out of `ps` for as long as the `docker run` takes.
//
// clientEnv is what that environment has to carry, for the one source that is
// not already in the daemon's own (an API key configured on AgentHostConfig).
// It is set on the docker client process, not exported anywhere lasting.
type sandboxCredential struct {
	args      []string
	clientEnv []string
}

// sandboxClaudeCredentialEnv reports how a sandboxed Claude session
// authenticates, and whether any credential exists at all.
//
// Three sources, in preference order:
//   - a `claude` subscription login on this host: ~/.claude(.json) is already
//     bind-mounted into the container, so nothing has to be passed — this is
//     the path the launch sheet's default assumes;
//   - CLAUDE_CODE_OAUTH_TOKEN, or
//   - ANTHROPIC_API_KEY on the daemon.
//
// The last two are the ONLY variables outside the BLERG_RUNNER_* allowlist
// that ever enter a sandbox, and only on this path: with the engine running
// inside the container, a credential the host-side driver would have read from
// its own environment has to travel with it, and that is the same exposure as
// the login directory mounted right beside it. The daemon master token is not
// among them and never will be.
func sandboxClaudeCredentialEnv(apiKey string) (sandboxCredential, bool) {
	if claudeAvailable() {
		return sandboxCredential{}, true
	}
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		// Already in the daemon's environment, so the docker client inherits it.
		return sandboxCredential{args: []string{"-e", "CLAUDE_CODE_OAUTH_TOKEN"}}, true
	}
	if apiKey != "" {
		return sandboxCredential{
			args:      []string{"-e", "ANTHROPIC_API_KEY"},
			clientEnv: []string{"ANTHROPIC_API_KEY=" + apiKey},
		}, true
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return sandboxCredential{args: []string{"-e", "ANTHROPIC_API_KEY"}}, true
	}
	return sandboxCredential{}, false
}

// sandboxClaudeCredentialReport is what the hello/heartbeat say about
// sandboxClaudeCredentialEnv: whether a sandboxed Claude spawn would find a
// credential right now. The daemon's API key is ANTHROPIC_API_KEY (see
// manager.go), so the environment is the whole answer.
func sandboxClaudeCredentialReport() *bool {
	_, ok := sandboxClaudeCredentialEnv(os.Getenv("ANTHROPIC_API_KEY"))
	return &ok
}

// NewAgentHost builds an AgentHost. Sender is the WS client (or a test fake).
func NewAgentHost(sender Sender, cfg AgentHostConfig) *AgentHost {
	if cfg.HomeDir == "" {
		if h, err := os.UserHomeDir(); err == nil {
			cfg.HomeDir = h
		}
	}
	return &AgentHost{sender: sender, cfg: cfg, sessions: make(map[string]*agentSession)}
}

// ─── Event emitter with pending buffer ───────────────────────────────────────

// pendingCap bounds the unacked-event buffer. At the cap the emitter blocks
// (spec: buffering) rather than dropping — dropping would break the
// transcript's rebuild guarantee. Transient deltas are never buffered.
const pendingCap = 10_000

type pendingEvent struct {
	clientEventID string
	raw           []byte
}

// wsEmitter implements agent.Emitter: marshals events onto the WS and keeps
// non-transient events pending until the server acks persistence.
type wsEmitter struct {
	sender    Sender
	sessionID string

	mu      sync.Mutex
	pending []pendingEvent // FIFO, insert order == emit order
	notFull *sync.Cond
	closed  bool // set by Kill: Emit becomes a drop, waiters are released

	// sendMu serializes append-to-pending + Send in Emit against resend's
	// full replay. Without it a resend racing a live emit can put a NEWER
	// event on the wire before an older unacked one — and the server assigns
	// seq per arrival, permanently corrupting transcript order.
	sendMu sync.Mutex

	// onEvent lets the host observe events (status tracking) without a
	// second emitter wrapper.
	onEvent func(agent.Event)
}

func newWSEmitter(sender Sender, sessionID string, onEvent func(agent.Event)) *wsEmitter {
	e := &wsEmitter{sender: sender, sessionID: sessionID, onEvent: onEvent}
	e.notFull = sync.NewCond(&e.mu)
	return e
}

func (e *wsEmitter) Emit(ev agent.Event) {
	if e.onEvent != nil {
		e.onEvent(ev)
	}
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		log.Printf("agent %s: marshal %s payload: %v", e.sessionID, ev.Kind, err)
		return
	}
	transient := false
	if ev.Kind == "assistant_text" {
		if p, ok := ev.Payload.(agent.AssistantTextPayload); ok && !p.Done {
			transient = true
		}
	}
	msg := protocol.AgentEvent{
		Type:          "agent_event",
		SessionID:     e.sessionID,
		ClientEventID: ev.ClientEventID,
		Ts:            ev.Ts.UTC().Format(time.RFC3339Nano),
		Kind:          ev.Kind,
		Payload:       payload,
		Transient:     transient,
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	if transient {
		_ = e.sender.Send(msg) // best-effort, never buffered or ordered
		return
	}
	e.mu.Lock()
	for len(e.pending) >= pendingCap && !e.closed {
		e.notFull.Wait() // spec: loop blocks at the bound (buffering)
	}
	if e.closed {
		e.mu.Unlock()
		return // killed session: drop instead of wedging the loop goroutine
	}
	e.mu.Unlock()

	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.pending = append(e.pending, pendingEvent{ev.ClientEventID, raw})
	e.mu.Unlock()
	_ = e.sender.Send(msg) // send failure is fine: resend covers it
}

// ack removes a persisted event from the pending buffer.
func (e *wsEmitter) ack(clientEventID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, p := range e.pending {
		if p.clientEventID == clientEventID {
			e.pending = append(e.pending[:i], e.pending[i+1:]...)
			e.notFull.Signal()
			return
		}
	}
}

// close releases any blocked Emit and turns future Emits into drops.
func (e *wsEmitter) close() {
	e.mu.Lock()
	e.closed = true
	e.notFull.Broadcast()
	e.mu.Unlock()
}

// resend re-sends all unacked events in order (after a reconnect, or on the
// periodic retry tick that covers lost acks / transient persist failures).
// Holds sendMu for the whole replay so live emits can't interleave.
func (e *wsEmitter) resend() {
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	e.mu.Lock()
	snapshot := make([]pendingEvent, len(e.pending))
	copy(snapshot, e.pending)
	e.mu.Unlock()
	for _, p := range snapshot {
		var msg protocol.AgentEvent
		if json.Unmarshal(p.raw, &msg) == nil {
			_ = e.sender.Send(msg)
		}
	}
}

func (e *wsEmitter) pendingCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pending)
}

// ─── Messenger over the existing messages HTTP API ───────────────────────────

// httpMessenger implements agent.Messenger against the server's messages API
// — the same endpoints the blerg-runner CLI uses, so the Chat tab, push
// notifications, and answer routing work unchanged.
type httpMessenger struct {
	base      string
	token     string
	sessionID string
	client    *http.Client
	ctx       context.Context     // session lifetime; bounds answer polling
	onAnswer  func(answer string) // called from the answer-poll goroutine
}

func (m *httpMessenger) post(kind, body string) (string, error) {
	payload, _ := json.Marshal(map[string]string{
		"session_id": m.sessionID, "kind": kind, "body": body,
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, m.base+"/api/messages", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.token)
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("messages api: %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (m *httpMessenger) SendUpdate(body string) error { _, err := m.post("update", body); return err }
func (m *httpMessenger) SendNote(body string) error   { _, err := m.post("note", body); return err }

func (m *httpMessenger) SendAsk(body string) (string, error) {
	id, err := m.post("ask", body)
	if err != nil {
		return "", err
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go m.pollAnswer(ctx, id)
	return id, nil
}

// pollAnswer long-polls the answer endpoint (the CLI's protocol) and delivers
// the reply into the loop. Terminates on session shutdown (ctx), on a
// non-retryable HTTP status (message expired/deleted), or on the answer.
func (m *httpMessenger) pollAnswer(ctx context.Context, id string) {
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+"/api/messages/"+id+"/answer", nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+m.token)
		resp, err := m.client.Do(req)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
				return // message expired/deleted — terminal
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		// Server shape: {"answered": bool, "answer": string|null} (long-poll
		// holds the request ≤25s while open).
		var out struct {
			Answered bool    `json:"answered"`
			Answer   *string `json:"answer"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&out)
		_ = resp.Body.Close()
		if decodeErr != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if out.Answered {
			answer := ""
			if out.Answer != nil {
				answer = *out.Answer
			}
			if answer == "" {
				answer = "[ask closed without an answer]"
			}
			if m.onAnswer != nil {
				m.onAnswer(answer)
			}
			return
		}
		// still open — the long-poll already waited server-side; loop again.
	}
}

// ─── Host lifecycle ──────────────────────────────────────────────────────────

// Spawn starts an agent-kind session for a spawn_session message.
func (h *AgentHost) Spawn(msg protocol.SpawnSession) { h.spawn(msg, nil, "") }

// SpawnResumed starts a session with provider context rebuilt from persisted
// transcript events (pod/daemon restart). wsState describes the workspace
// ("resumed-from-wip" | "fresh-clone"); a fresh clone means uncommitted work
// from before the restart is gone, and the model is told so.
// WorkspaceInitialised is the workspace state of a "New repository" session
// whose repository could not be cloned (runner.PrepareWorkspace).
const WorkspaceInitialised = "initialised"

func (h *AgentHost) SpawnResumed(msg protocol.SpawnSession, events []agent.RestoredEvent, wsState string) {
	note := ""
	if wsState == "empty" {
		// A "No repository" cluster session (runner.WorkspaceEmpty): nothing
		// to clone, and nothing survived the old pod.
		note = "[system] Session resumed after a restart. This session has no repository, so its workspace is a new, empty directory: nothing written before the restart survived. Re-create anything you still need."
	} else if wsState == WorkspaceInitialised {
		// A "New repository" cluster session whose repository still does not
		// exist (runner.WorkspaceInitialised): nothing could be cloned back.
		note = "[system] Session resumed after a restart. This session's repository could not be cloned (it was never created on its git host, or is not reachable), so the workspace is a new, empty repository again: nothing written before the restart survived. Re-create anything you still need, and create the remote before pushing."
	} else if wsState != "resumed-from-wip" {
		note = "[system] Session resumed after a restart. The workspace is a fresh clone: committed work on the wip branch survived, but any uncommitted changes from before the restart are gone. Re-verify workspace state before continuing."
	}
	h.spawn(msg, events, note)
}

func (h *AgentHost) spawn(msg protocol.SpawnSession, restore []agent.RestoredEvent, note string) {
	h.mu.Lock()
	if _, dup := h.sessions[msg.SessionID]; dup {
		h.mu.Unlock()
		log.Printf("agent session %s: duplicate spawn ignored", msg.SessionID)
		return
	}
	h.mu.Unlock()
	// Checked here as well as in Manager.handleSpawnSession: the cluster pod
	// (cmd/runner) calls Spawn directly, never through the Manager.
	if problem := spawnModelProblem(msg); problem != "" {
		h.sendError(msg.SessionID, problem)
		return
	}
	workDir := msg.ProjectPath
	if workDir == "" {
		// Applied here too, not just in Manager.handleSpawnSession: agent-kind
		// spawns branch off before that check runs, so this is the only guard
		// standing between an unvalidated Repo and a filesystem path outside
		// ReposRoot.
		wd, err := resolveProjectPath(h.cfg.reposRoot(), msg.Repo)
		if err != nil {
			h.sendError(msg.SessionID, "invalid folder path")
			return
		}
		workDir = wd
	}
	// Backstop: Manager.prepareWorkspace already creates/clones the workspace
	// before every spawn_session reaches here, so this only fires for callers
	// that bypass it (recovery with a stale path). Name the repo, not just the
	// path — this text becomes the session's error_reason.
	if _, err := os.Stat(workDir); err != nil {
		h.sendError(msg.SessionID, fmt.Sprintf("workspace not found for repo %q: %s is missing — clone it under this daemon's repos root", msg.Repo, workDir))
		return
	}

	// Which driver runs this session is one decision (chooseAgentDriver),
	// shared with the engine preflight and recovery.
	if msg.Sandbox && msg.Engine == "openclaw" {
		h.sendError(msg.SessionID, sandboxOpenclawRefusal)
		return
	}
	kind, refusal := h.driverFor(msg)
	if kind == driverNone {
		h.sendError(msg.SessionID, refusal)
		return
	}
	cliEngine := kind == driverCLI
	useClaudeCode := kind == driverClaudeCode

	// In the sandbox Claude Code runs inside the container, and the
	// credential travels with it — see sandboxClaudeCredentialEnv.
	var sandboxCred sandboxCredential
	if msg.Sandbox && useClaudeCode {
		cred, ok := sandboxClaudeCredentialEnv(h.cfg.APIKey)
		if !ok {
			h.sendError(msg.SessionID, sandboxNoClaudeCredentialRefusal)
			return
		}
		sandboxCred = cred
	}

	if msg.RestrictTools && !useClaudeCode {
		h.sendError(msg.SessionID, restrictToolsNeedsClaudeRefusal)
		return
	}
	if msg.MCPGateway != nil {
		if !useClaudeCode {
			h.sendError(msg.SessionID, mcpGatewayNeedsClaudeRefusal)
			return
		}
		// The error names the problem, never a token.
		if err := ValidateMCPGateway(msg.MCPGateway); err != nil {
			h.sendError(msg.SessionID, err.Error())
			return
		}
	}

	skillList, _ := skills.Discover(workDir, h.cfg.HomeDir)
	agentTypes := agent.DiscoverAgentTypes(workDir, h.cfg.HomeDir)

	provider := h.cfg.Provider
	if provider == nil {
		provider = anthropic.New(h.cfg.APIKey, "")
	}

	sess := &agentSession{skills: skillList, engine: msg.Engine}
	// A new session is idle until its first turn starts (the driver says
	// "running" then). Reporting "running" here made a session with no
	// initial prompt look busy — spinner, working indicator — while it was
	// really waiting for its first message.
	sess.status.Store("idle")
	emitter := newWSEmitter(h.sender, msg.SessionID, func(ev agent.Event) {
		if ev.Kind == "status_changed" {
			if p, ok := ev.Payload.(agent.StatusPayload); ok {
				sess.status.Store(p.Status)
			}
		}
		if ev.Kind == "turn_done" && h.cfg.OnTurnDone != nil {
			h.cfg.OnTurnDone(msg.SessionID)
		}
		// A driver failure is an agent *event*; only session_state_changed
		// reaches SetSessionError, which is the only thing that writes
		// error_reason and so the only way a board card shows a reason
		// instead of sitting at "running". Retryable errors (a dropped queue
		// message, an engine exit the driver will try again) stay
		// transcript-only: "error" is a terminal session status, so marking
		// one would end a session that is still alive.
		if ev.Kind == "error" {
			if p, ok := ev.Payload.(agent.ErrorPayload); ok && !p.Retryable && p.Message != "" {
				h.sendError(msg.SessionID, p.Message)
			}
		}
	})
	sess.emitter = emitter

	model := msg.Model
	// claude-sonnet-5 is only a sensible default for the native loop and the
	// claude-code driver — passing it to codex (an invalid model name there)
	// made every codex agent-kind session fail immediately with exit status
	// 1, caught live: real turn ran, but codex rejected the bogus --model.
	// Every other registered engine's model names are provider/model shaped
	// too, so they all get the same treatment: no guessed default, empty
	// means "use the engine's own configured default".
	if model == "" && !cliEngine {
		model = "claude-sonnet-5"
	}

	ctx, cancel := context.WithCancel(context.Background())

	// The environment every subprocess of this session starts from — engine
	// CLIs and the native loop's bash tool alike. It is built by the SAME
	// builder every other spawn kind uses, so an agent-kind session gets the
	// session id, the server URL and the ~/.local/bin PATH prepend that make
	// its per-session messaging token usable, not just the token itself.
	// The daemon master token is still absent (sanitizedEnviron) and ExtraEnv
	// is still filtered (extraEnvPairs); h.cfg.DaemonToken stays with the
	// host's own server calls below and never enters a child process.
	env := buildSessionEnv(sessionEnvOpts{
		SessionID:    msg.SessionID,
		ServerHTTP:   h.cfg.ServerHTTP,
		Assist:       msg.Assist,
		BoardID:      msg.BoardID,
		BoardToken:   msg.BoardToken,
		SessionToken: msg.SessionToken,
		Extra:        msg.ExtraEnv,
	})

	// A sandboxed agent-kind session gets the same hardened container a
	// terminal sandbox gets (sandbox.go) — no tmux inside it, just a place for
	// the engine subprocess to run: every turn below is wrapped in `docker
	// exec` by this prefix, so the engine sees only /workspace and the
	// credential mounts, never the rest of the host.
	// Always-on plugins (pluginworkshop.go), installed before the container
	// starts because a sandbox mounts the session's snapshot at `docker run`.
	// The list is dropped — whatever the server sent — for any session that
	// must not load code of its own: a restricted or granted one (--plugin-dir
	// survives the hardening flags), a non-Claude engine, a terminal kind; and
	// for a daemon with no workshop. A dropped non-empty list is said so in
	// the start panel, never shown as installed.
	var pluginDirs []string
	if len(msg.Plugins) > 0 {
		stages := daemonStageReporter{sender: h.sender, sessionID: msg.SessionID}
		reason := ""
		switch {
		case msg.RestrictTools || msg.MCPGateway != nil:
			reason = "not loaded: a restricted session runs without plugins"
		case !useClaudeCode || msg.Kind != "agent":
			reason = "not loaded: only Claude Code agent sessions load plugins"
		case h.cfg.Plugins == nil:
			reason = "not loaded: this daemon has no plugin workshop (no state directory)"
		}
		if reason != "" {
			stages.report(protocol.StartStage{ID: protocol.StagePlugins, Label: "Installing plugins", State: protocol.StageStateWarning, Detail: reason})
		} else {
			stages.report(protocol.StartStage{ID: protocol.StagePlugins, Label: "Installing plugins",
				State: protocol.StageStateActive, Detail: fmt.Sprintf("Installing %d plugin(s)", len(msg.Plugins))})
			dirs, res := h.cfg.Plugins.prepare(ctx, msg.SessionID, msg.Plugins)
			pluginDirs = dirs
			if h.cfg.SpawnKilled != nil && h.cfg.SpawnKilled(msg.SessionID) {
				// Stopped while the plugins installed: the session must not start behind the user's back.
				log.Printf("agent session %s: spawn abandoned — killed while its plugins were being prepared", msg.SessionID)
				cancel()
				if len(dirs) > 0 {
					h.cfg.Plugins.release(msg.SessionID)
				}
				return
			}
			log.Printf("agent session %s: plugins: %s", msg.SessionID, res.Detail())
			state := protocol.StageStateDone
			if len(res.Failed)+len(res.Skipped) > 0 {
				state = protocol.StageStateWarning
			}
			stages.report(protocol.StartStage{ID: protocol.StagePlugins, Label: "Installing plugins", State: state, Detail: res.Detail()})
		}
	}
	// releasePlugins drops the session's plugin snapshot when the spawn fails
	// after preparing it; a started session's goes on Kill.
	releasePlugins := func() {
		if len(pluginDirs) > 0 {
			h.cfg.Plugins.release(msg.SessionID)
		}
	}

	var prefix sandboxExec
	if msg.Sandbox {
		sbOpts := h.cfg.Sandbox
		sbOpts.ClaudeOnly = msg.RestrictTools || msg.MCPGateway != nil
		if len(pluginDirs) > 0 {
			sbOpts.PluginCache = h.cfg.Plugins.sessionDir(msg.SessionID)
			pluginDirs = translatePluginDirs(pluginDirs, sbOpts.PluginCache)
		}
		container, err := startSandboxContainer(msg.SessionID, workDir, env, sandboxCred, sbOpts)
		if err != nil {
			cancel()
			releasePlugins()
			h.sendError(msg.SessionID, "could not start the sandbox container: "+err.Error())
			return
		}
		prefix = sandboxExecPrefix(container)
		sess.sandboxed = true
	}

	// The MCP gateway config file, written before any turn (inside the
	// container for a sandbox) so a session that cannot get its grant fails
	// here rather than running without it.
	var mcpFile *mcpConfigFile
	var ccOpts []ccOption
	if msg.RestrictTools {
		// A grant session is restricted too: the driver adds the flags for it
		// as soon as it has a config source, RestrictTools or not.
		ccOpts = append(ccOpts, withRestrictTools())
	}
	if len(pluginDirs) > 0 {
		ccOpts = append(ccOpts, withPluginDirs(pluginDirs))
	}
	if msg.MCPGateway != nil {
		mcpFile = newMCPConfigFile(msg.MCPGateway, prefix)
		if _, err := mcpFile.Ensure(); err != nil {
			cancel()
			if msg.Sandbox {
				removeSandboxContainer(msg.SessionID)
			}
			releasePlugins()
			h.sendError(msg.SessionID, err.Error())
			return
		}
		ccOpts = append(ccOpts, withMCPConfigSource(mcpFile))
		sess.mcp = mcpFile
	}

	var loop *agent.Loop
	messenger := &httpMessenger{
		base: h.cfg.ServerHTTP, token: h.cfg.DaemonToken, sessionID: msg.SessionID,
		client: &http.Client{Timeout: 60 * time.Second},
		ctx:    ctx,
		onAnswer: func(answer string) {
			if loop != nil {
				loop.Enqueue(answer, "ask_answer")
			}
		},
	}

	registryTools := []agent.Tool{
		tools.ReadFile(workDir), tools.Glob(workDir), tools.Grep(workDir),
		tools.WriteFile(workDir), tools.EditFile(workDir), tools.BashEnv(workDir, env),
		agent.SkillTool(skillList),
	}
	dataPlaneCtx := ""
	if h.cfg.ServerHTTP != "" {
		pub := tools.PublishConfig{Base: h.cfg.ServerHTTP, Token: h.cfg.DaemonToken}
		dp := tools.DataPlaneConfig{Base: h.cfg.ServerHTTP, Token: h.cfg.DaemonToken, Project: msg.Repo}
		registryTools = append(registryTools,
			tools.PushMockup(workDir, pub), tools.PushScreenshot(workDir, pub),
			tools.MemoryWrite(dp), tools.MemoryDelete(dp),
			tools.RulePropose(dp), tools.KnowledgeSearch(dp))
		dataPlaneCtx = fetchDataPlaneContext(h.cfg.ServerHTTP, h.cfg.DaemonToken, msg.Repo)
	}
	registry := agent.NewRegistry(registryTools...)
	cfg := agent.Config{
		Provider:  provider,
		Emitter:   emitter,
		Registry:  registry,
		System:    BuildSystemPrompt(workDir, h.cfg.HomeDir, skillList, agentTypes) + dataPlaneCtx,
		Model:     model,
		Effort:    msg.Effort,
		Messenger: messenger,
		BudgetUSD: h.cfg.BudgetUSD,
		Pricing:   h.cfg.Pricing,
	}
	var driver sessionDriver
	switch {
	case cliEngine:
		spec := engineRegistry[msg.Engine]
		log.Printf("agent session %s: %s driver", msg.SessionID, spec.DisplayName)
		driver = spec.NewAgentDriver(AgentDriverOpts{
			Spec: spec, WorkDir: workDir, Model: model, Effort: msg.Effort,
			SessionID: msg.SessionID, Emitter: emitter, Env: env,
		})
	case useClaudeCode:
		log.Printf("agent session %s: claude-code driver (the engine's own login)", msg.SessionID)
		cc := newClaudeCodeDriver(workDir, model, msg.Effort, emitter, env, ccOpts...)
		cc.skills, cc.home = skillList, h.cfg.HomeDir
		driver = cc
	default:
		loop = agent.NewLoop(cfg)
		loop.AddTool(agent.AgentTool(loop, agentTypes, agent.DefaultSpawn))
		loop.InstallMessagingTools()
		driver = loop
	}
	if prefix.enabled() {
		// Belt and braces: the refusals above mean only a containable driver
		// can get here, so a driver that cannot take the prefix is a bug —
		// refuse rather than silently run the engine on the host.
		sb, ok := driver.(sandboxedDriver)
		if !ok {
			cancel()
			mcpFile.Remove()
			removeSandboxContainer(msg.SessionID)
			releasePlugins()
			h.sendError(msg.SessionID, sandboxUnsupportedDriverRefusal)
			return
		}
		sb.useSandbox(prefix)
	}
	if len(restore) > 0 {
		driver.RestoreContext(restore)
	}

	sess.loop = driver
	sess.cancel = cancel
	sess.plugins = len(pluginDirs) > 0

	h.mu.Lock()
	h.sessions[msg.SessionID] = sess
	h.mu.Unlock()

	// Periodic resend covers lost acks and transient server-side persist
	// failures (the server acks only after a successful DB append).
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if emitter.pendingCount() > 0 {
					emitter.resend()
				}
			}
		}
	}()

	_ = h.sender.Send(protocol.SessionStarted{
		Type: "session_started", SessionID: msg.SessionID,
		ProjectPath: workDir, Repo: msg.Repo, Title: msg.Title, Model: model,
		Kind: "agent",
	})
	// What the session has loaded, for the Skills & plugins panel. Claude
	// Code reports its own from each turn's init line (claudeCodeDriver); the
	// native loop knows its lists; other engines may have a best-effort
	// source on their EngineSpec. Nothing else is guessed.
	var caps capEmitter
	switch {
	case loop != nil:
		caps.emit(emitter, nativeCapabilities(model, skillList, agentTypes, loop.ToolDefs(), workDir, h.cfg.HomeDir))
	case cliEngine:
		if p, ok := engineCapabilities(engineRegistry[msg.Engine], model, CapabilityContext{
			WorkDir: workDir, HomeDir: h.cfg.HomeDir, Sandboxed: prefix.enabled(),
		}); ok {
			caps.emit(emitter, p)
		}
	}
	driver.Start()
	go driver.Run(ctx)
	if note != "" {
		driver.Enqueue(note, "chat")
	}
	if strings.TrimSpace(msg.InitialPrompt) != "" {
		h.UserMessage(msg.SessionID, msg.InitialPrompt, "chat")
	} else if note == "" {
		// Nothing queued: the session is ready and waiting for its first
		// message. Say so now — otherwise the row sits at "starting" until
		// the next heartbeat reconciles it, and the browser keeps showing
		// the start panel for a session that is already live. (With work
		// queued, the driver's own "running" is the transition.)
		_ = h.sender.Send(protocol.SessionStateChanged{
			Type: "session_state_changed", SessionID: msg.SessionID, Status: "idle",
		})
	}
}

func (h *AgentHost) get(sessionID string) *agentSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[sessionID]
}

// UserMessage delivers a user message: harness slash-commands are
// intercepted, skill slash-commands resolve to skill content, plain text is
// enqueued as-is.
func (h *AgentHost) UserMessage(sessionID, text, source string) {
	sess := h.get(sessionID)
	if sess == nil {
		return
	}
	if source == "" {
		source = "chat"
	}
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "/model ") {
		sess.changeModel(strings.TrimSpace(strings.TrimPrefix(trimmed, "/model ")), "", "command")
		return
	}
	if strings.HasPrefix(trimmed, "/effort ") {
		sess.changeModel("", strings.TrimSpace(strings.TrimPrefix(trimmed, "/effort ")), "command")
		return
	}
	if resolved, ok, err := skills.ResolveSlash(sess.skills, trimmed); err == nil && ok {
		sess.loop.Enqueue(resolved, source)
		return
	}
	sess.loop.Enqueue(text, source)
}

// SetModel applies a model/effort change from the UI.
func (h *AgentHost) SetModel(sessionID, model, effort string) {
	if sess := h.get(sessionID); sess != nil {
		sess.changeModel(model, effort, "ui")
	}
}

// changeModel is the ONE gate every in-session model/effort change passes —
// a /model or /effort chat command, or the UI's switch — before any driver
// sees it: both values are checked against the session engine's rules
// (internal/models), and a refused change never reaches the driver (whose
// SetModel checks again) or the session row (model_changed). The refusal is
// said in the transcript instead.
func (sess *agentSession) changeModel(model, effort, source string) {
	if model == "" && effort == "" {
		return
	}
	if !models.ValidModelFor(sess.engine, model) || !models.ValidEffortFor(sess.engine, effort) {
		msg := fmt.Sprintf("ignored invalid model %q / effort %q", model, effort)
		if levels := models.EffortsFor(sess.engine); effort != "" && len(levels) > 0 {
			msg += " — effort is one of " + strings.Join(levels, ", ")
		} else if effort != "" {
			msg += " — this engine takes no effort"
		}
		if sess.emitter != nil {
			sess.emitter.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "error",
				Payload: agent.ErrorPayload{Message: msg, Retryable: true}})
		}
		return
	}
	sess.loop.SetModel(model, effort, source)
}

// Has reports whether sessionID is a live agent session on this host.
func (h *AgentHost) Has(sessionID string) bool { return h.get(sessionID) != nil }

// Interrupt cancels the session's in-flight turn.
func (h *AgentHost) Interrupt(sessionID string) {
	if sess := h.get(sessionID); sess != nil {
		sess.loop.Interrupt()
	}
}

// Kill stops the session entirely.
func (h *AgentHost) Kill(sessionID string) {
	h.mu.Lock()
	sess := h.sessions[sessionID]
	delete(h.sessions, sessionID)
	h.mu.Unlock()
	if sess == nil {
		return
	}
	sess.emitter.close() // release any Emit blocked on a full buffer
	sess.loop.Interrupt()
	sess.cancel()
	sess.mcp.Remove() // before the container goes, so the file is deleted in it too
	if sess.sandboxed {
		// Same teardown a terminal sandbox gets on kill (killTmuxSession): the
		// container outlives nothing.
		removeSandboxContainer(sessionID)
	}
	if sess.plugins {
		h.cfg.Plugins.release(sessionID) // after the container: the snapshot was mounted in it
	}
	_ = h.sender.Send(protocol.SessionEnded{Type: "session_ended", SessionID: sessionID, ExitCode: 0})
}

// HandleAck removes a server-persisted event from the pending buffer.
func (h *AgentHost) HandleAck(sessionID, clientEventID string) {
	if sess := h.get(sessionID); sess != nil {
		sess.emitter.ack(clientEventID)
	}
}

// ResendPending re-sends unacked events for every session (on reconnect).
func (h *AgentHost) ResendPending() {
	h.mu.Lock()
	sessions := make([]*agentSession, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	for _, s := range sessions {
		s.emitter.resend()
	}
}

// ActiveIDs lists live agent session IDs.
func (h *AgentHost) ActiveIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	return ids
}

// States reports each agent session's protocol-derived status for heartbeats.
func (h *AgentHost) States() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]string, len(h.sessions))
	for id, s := range h.sessions {
		if v, ok := s.status.Load().(string); ok {
			out[id] = v
		}
	}
	return out
}

// engineBinary reports the CLI binary an agent-kind spawn would shell out to,
// or "" for the native in-process loop, which has none. It mirrors spawn's
// driver switch and is what Manager.preflightEngine checks, so agent-kind
// gets the same actionable "not on PATH" reason every other kind gets.
func (h *AgentHost) engineBinary(msg protocol.SpawnSession) string {
	if spec, ok := engineRegistry[msg.Engine]; ok {
		return spec.Binary
	}
	// The same decision spawn makes (chooseAgentDriver): a session that will
	// run Claude Code — sandboxed, the cluster pod, or a desktop daemon with
	// `claude` installed — has a binary to preflight; the native loop has none.
	if kind, _ := h.driverFor(msg); kind == driverClaudeCode {
		return "claude"
	}
	return ""
}

func (h *AgentHost) sendError(sessionID, msg string) {
	log.Printf("agent session %s: %s", sessionID, msg)
	_ = h.sender.Send(protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: sessionID, Status: "error", Message: &msg,
	})
}

// DefaultPricing is the per-model USD/MTok table used for the session budget
// cap. Prices per the Anthropic pricing page (cache writes ≈ 1.25× input,
// cache reads ≈ 0.1× input).
func DefaultPricing() map[string]agent.Price {
	price := func(in, out float64) agent.Price {
		return agent.Price{InputPerM: in, OutputPerM: out, CacheReadPerM: in * 0.1, CacheWritePerM: in * 1.25}
	}
	return map[string]agent.Price{
		"claude-fable-5":    price(10, 50),
		"claude-opus-5":     price(5, 25),
		"claude-opus-4-8":   price(5, 25),
		"claude-sonnet-5":   price(3, 15),
		"claude-sonnet-4-6": price(3, 15),
		"claude-haiku-4-5":  price(1, 5),
	}
}

// fetchDataPlaneContext pulls the project's enabled rules and memory index
// for system-prompt injection. Best-effort with a short timeout — a server
// hiccup must not block session spawn.
func fetchDataPlaneContext(base, token, project string) string {
	client := &http.Client{Timeout: 3 * time.Second}
	get := func(path string, out any) bool {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+path, nil)
		if err != nil {
			return false
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		return json.NewDecoder(resp.Body).Decode(out) == nil
	}

	var b strings.Builder
	var rules []struct {
		Content string `json:"content"`
		Enabled bool   `json:"enabled"`
	}
	if get("/api/agent/rules?project="+url.QueryEscape(project), &rules) {
		var enabled []string
		for _, r := range rules {
			if r.Enabled {
				enabled = append(enabled, r.Content)
			}
		}
		if len(enabled) > 0 {
			b.WriteString("\n\n## Project rules (user-approved standing instructions)\n\n")
			for _, r := range enabled {
				b.WriteString("- " + r + "\n")
			}
		}
	}
	var memories []struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Summary string `json:"summary"`
	}
	if get("/api/agent/memories?project="+url.QueryEscape(project), &memories) && len(memories) > 0 {
		b.WriteString("\n\n## Project memory index (retrieve full entries with knowledge_search)\n\n")
		for _, m := range memories {
			b.WriteString("- " + m.Name + " (" + m.Kind + "): " + m.Summary + "\n")
		}
	}
	return b.String()
}

// daemonStageReporter sends start_stage events for a daemon-hosted start,
// the way the cluster pod's reporter does (runner/stages.go): the server
// merges them into the session's start plan as authoritative. Best effort —
// a report the connection drops is only a progress line.
type daemonStageReporter struct {
	sender    Sender
	sessionID string
}

func (r daemonStageReporter) report(stages ...protocol.StartStage) {
	if r.sender == nil || len(stages) == 0 {
		return
	}
	raw, err := json.Marshal(protocol.StartStagePayload{Stages: stages})
	if err != nil {
		return
	}
	_ = r.sender.Send(protocol.AgentEvent{
		Type: "agent_event", SessionID: r.sessionID, ClientEventID: ccUUID(),
		Ts: time.Now().UTC().Format(time.RFC3339Nano), Kind: protocol.StartStageKind, Payload: raw,
	})
}
