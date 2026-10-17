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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/agent/anthropic"
	"github.com/blerglab/blerg-ai/runner/internal/agent/skills"
	"github.com/blerglab/blerg-ai/runner/internal/agent/tools"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/pluginsinstall"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// AgentHostConfig configures agent-kind session hosting inside the daemon.
type AgentHostConfig struct {
	CredentialSource EngineCredentialSource
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
	// RecordsDir is where recovery records of agent sessions are kept (agentrecord.go), so a
	// session survives a restart of the daemon process. "" — the cluster pod's, and a daemon
	// with no state directory — keeps none.
	RecordsDir string
	// NativeHistory is the pod's session-scoped conversation-only volume.
	NativeHistory *NativeHistoryConfig
	// RestartCause is why the previous run of the daemon ended (restartcause.go): what a session
	// it cut off is told about it. The zero value says the run crashed.
	RestartCause RestartCause
	// UsageLimits hears what moves the providers' usage limits (limits.go): a Codex or Claude
	// Code turn ended, Claude refused a call for its limit. nil — the cluster pod's — reports none.
	UsageLimits UsageLimitsSink
}

// UsageLimitsSink is what agent sessions tell the daemon's UsageLimitsTracker.
type UsageLimitsSink interface {
	CodexTurnEnded()
	ClaudeTurnEnded()
	ClaudeRateLimited(resetsAt time.Time)
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

	// frozen stops every recovery-record write once the daemon is stopping (Freeze).
	frozen atomic.Bool
	// Adoption after a daemon restart (agentadopt.go), all guarded by mu. adopting are the
	// recorded sessions not hosted again yet: they are reported as this daemon's (the server
	// must not write them off while they are being brought back) and messages for them are
	// held. adoptFailed are those that could not be hosted; their end is reported once the
	// daemon is connected.
	adopting    map[string]*adoptingSession
	adoptFailed []string
	// openAsks are the sessions the server says have a `blerg-runner ask` open (NoteOpenAsks): an
	// adopted interactive session among them waits for the person.
	openAsks map[string]bool
	// connected is closed the first time the daemon has a connection to its server.
	connected     chan struct{}
	connectedOnce sync.Once
}

type agentSession struct {
	// workspace is the host folder mounted at /workspace for sandbox sessions.
	// It stays bound to this launch even if the daemon's repos root changes.
	workspace     string
	workspaceInfo os.FileInfo
	workspaceCtx  context.Context
	loop          sessionDriver
	cancel        context.CancelFunc
	// runDone closes only after the driver has finished subprocess and file cleanup.
	runDone <-chan struct{}
	emitter *wsEmitter
	status  atomic.Value // last status string for heartbeats
	skills  []skills.Skill
	// sandboxed marks a session whose engine runs inside a container, so Kill
	// tears the container down the way killTmuxSession does for a terminal one.
	sandboxed  bool
	plugins    bool // holds a Claude plugin snapshot in the workshop (released on Kill)
	pluginHome bool // holds a native plugin home, released on Kill
	// mcp is the session's MCP gateway config file (nil: no grant), removed
	// when the session ends.
	mcp             *mcpConfigFile
	mcpState        atomic.Pointer[mcpTracker]
	codexRestricted *codexRestrictedRuntime
	// engine is the session's engine id ("" = Claude): the rules every
	// in-session model/effort change is checked against (changeModel).
	engine string
	// record is the session's recovery record (nil: not recoverable, see recordable).
	record     *recordKeeper
	inputs     *inputJournal
	inputFault atomic.Bool
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
	return &AgentHost{sender: sender, cfg: cfg, sessions: make(map[string]*agentSession),
		adopting: make(map[string]*adoptingSession), connected: make(chan struct{})}
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

func (e *wsEmitter) Emit(ev agent.Event) { e.emit(ev, true) }

// Replay resends a receipt without applying old turn/status bookkeeping again.
func (e *wsEmitter) Replay(ev agent.Event) { e.emit(ev, false) }

func (e *wsEmitter) emit(ev agent.Event, observe bool) {
	if observe && e.onEvent != nil {
		e.onEvent(ev)
	}
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		log.Printf("agent %s: marshal %s payload: %v", e.sessionID, ev.Kind, err)
		return
	}
	// A NUL in tool output would make the server's jsonb insert fail forever (see protocol.StripNUL).
	payload = protocol.StripNUL(payload)
	transient := agent.IsTransient(ev)
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
	duplicate := false
	if !observe {
		for _, pending := range e.pending {
			if pending.clientEventID == ev.ClientEventID {
				duplicate = true
				break
			}
		}
	}
	for len(e.pending) >= pendingCap && !e.closed && !duplicate {
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
	buffered := false
	if !observe {
		for _, pending := range e.pending {
			if pending.clientEventID == ev.ClientEventID {
				buffered = true
				break
			}
		}
	}
	if !buffered {
		e.pending = append(e.pending, pendingEvent{ev.ClientEventID, raw})
	}
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
	turnID    string // optional native-question turn, recorded atomically with its server ask
	client    *http.Client
	ctx       context.Context     // session lifetime; bounds answer polling
	onAnswer  func(answer string) // called from the answer-poll goroutine
	onPollEnd func()              // optional: lets native requests stop waiting when the ask expires

	pollMu sync.Mutex
	polls  map[string]context.CancelFunc // ask id → its answer poll, while it runs
}

func (m *httpMessenger) post(kind, body string) (string, error) {
	payload, _ := json.Marshal(map[string]string{
		"session_id": m.sessionID, "kind": kind, "body": body, "turn_id": m.turnID,
	})
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+"/api/messages", bytes.NewReader(payload))
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
	pctx, cancel := context.WithCancel(ctx)
	m.pollMu.Lock()
	if m.polls == nil {
		m.polls = make(map[string]context.CancelFunc)
	}
	m.polls[id] = cancel
	m.pollMu.Unlock()
	go func() {
		if m.onPollEnd != nil {
			defer m.onPollEnd()
		}
		defer func() {
			m.pollMu.Lock()
			delete(m.polls, id)
			m.pollMu.Unlock()
			cancel()
		}()
		m.pollAnswer(pctx, id)
	}()
	return id, nil
}

// CloseAsk implements agent.AskCloser: the ask was answered in the chat, so its poll stops (it
// must not deliver the answer a second time) and its message is marked answered (its card stops
// asking). Best effort: the loop already has its answer.
func (m *httpMessenger) CloseAsk(id, answer string) {
	m.pollMu.Lock()
	if cancel := m.polls[id]; cancel != nil {
		cancel()
		delete(m.polls, id)
	}
	m.pollMu.Unlock()
	go func() {
		payload, _ := json.Marshal(map[string]string{"answer": answer})
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			m.base+"/api/messages/"+url.PathEscape(id)+"/reply", bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+m.token)
		resp, err := m.client.Do(req)
		if err != nil {
			log.Printf("agent session %s: closing ask %s: %v", m.sessionID, id, err)
			return
		}
		_ = resp.Body.Close()
	}()
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
func (h *AgentHost) Spawn(msg protocol.SpawnSession) { h.spawn(msg, nil, "", nil) }

// SpawnResumed starts a session with provider context rebuilt from persisted
// transcript events (pod/daemon restart). wsState describes the workspace
// ("resumed-from-wip" | "fresh-clone"); a fresh clone means uncommitted work
// from before the restart is gone, and the model is told so.
// WorkspaceInitialised is the workspace state of a "New repository" session
// whose repository could not be cloned (runner.PrepareWorkspace).
const WorkspaceInitialised = "initialised"

func (h *AgentHost) SpawnResumed(msg protocol.SpawnSession, events []agent.RestoredEvent, wsState string) {
	note := ""
	switch wsState {
	case "empty":
		// A "No repository" cluster session (runner.WorkspaceEmpty): nothing
		// to clone, and nothing survived the old pod.
		note = "[system] Session resumed after a restart. This session has no repository, so its workspace is a new, empty directory: nothing written before the restart survived. Re-create anything you still need."
	case WorkspaceInitialised:
		// A "New repository" cluster session whose repository still does not
		// exist (runner.WorkspaceInitialised): nothing could be cloned back.
		note = "[system] Session resumed after a restart. This session's repository could not be cloned (it was never created on its git host, or is not reachable), so the workspace is a new, empty repository again: nothing written before the restart survived. Re-create anything you still need, and create the remote before pushing."
	case "resumed-from-wip":
	default:
		note = "[system] Session resumed after a restart. The workspace is a fresh clone: committed work on the wip branch survived, but any uncommitted changes from before the restart are gone. Re-verify workspace state before continuing."
	}
	h.spawn(msg, events, note, nil)
}

// spawn hosts a session. With ad (agentadopt.go) it hosts one AGAIN, from a recovery record,
// after a daemon restart: the same environment, tools and driver, but nothing that belongs to a
// start — no session_started (it would reset the row to "starting"), no start-stage events, no
// initial prompt, no state change — and a refusal is returned in ad instead of being sent as the
// session's error.
func (h *AgentHost) spawn(msg protocol.SpawnSession, restore []agent.RestoredEvent, note string, ad *adoptRequest) {
	fail := func(reason string) {
		if ad != nil {
			ad.failed = reason
			return
		}
		h.sendError(msg.SessionID, reason)
	}
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
		fail(problem)
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
			fail("invalid folder path")
			return
		}
		workDir = wd
	}
	// Backstop: Manager.prepareWorkspace already creates/clones the workspace
	// before every spawn_session reaches here, so this only fires for callers
	// that bypass it (recovery with a stale path). Name the repo, not just the
	// path — this text becomes the session's error_reason.
	workspaceInfo, workspaceErr := os.Stat(workDir)
	if workspaceErr != nil {
		fail(fmt.Sprintf("workspace not found for repo %q: %s is missing — clone it under this daemon's repos root", msg.Repo, workDir))
		return
	}

	// Which driver runs this session is one decision (chooseAgentDriver),
	// shared with the engine preflight and recovery.
	if msg.Sandbox && msg.Engine == "openclaw" {
		fail(sandboxOpenclawRefusal)
		return
	}
	kind, refusal := h.driverFor(msg)
	if kind == driverNone {
		fail(refusal)
		return
	}
	cliEngine := kind == driverCLI
	useClaudeCode := kind == driverClaudeCode
	useCodex := cliEngine && msg.Engine == "codex"

	// In the sandbox Claude Code runs inside the container, and the
	// credential travels with it — see sandboxClaudeCredentialEnv.
	var sandboxCred sandboxCredential
	if msg.Sandbox && useClaudeCode {
		cred, ok := sandboxClaudeCredentialEnv(h.cfg.APIKey)
		if !ok {
			fail(sandboxNoClaudeCredentialRefusal)
			return
		}
		sandboxCred = cred
	}

	if msg.RestrictTools && !useClaudeCode && !useCodex {
		fail(restrictToolsNeedsClaudeRefusal)
		return
	}
	if msg.MCPGateway != nil {
		if !useClaudeCode && !useCodex {
			fail(mcpGatewayNeedsClaudeRefusal)
			return
		}
		// The error names the problem, never a token.
		if err := ValidateMCPGateway(msg.MCPGateway); err != nil {
			fail(err.Error())
			return
		}
	}
	var native *nativeHistoryKeeper
	if h.cfg.NativeHistory != nil {
		var err error
		native, err = openNativeHistory(*h.cfg.NativeHistory, msg.SessionID, msg.Engine, workDir)
		if err != nil {
			fail(err.Error())
			return
		}
	}
	nativeOwned := false
	defer func() {
		if native != nil && !nativeOwned {
			_ = native.root.Close()
		}
	}()

	skillList, _ := skills.Discover(workDir, h.cfg.HomeDir)
	agentTypes := agent.DiscoverAgentTypes(workDir, h.cfg.HomeDir)

	journalDir := ""
	if h.cfg.RecordsDir != "" {
		journalDir = filepath.Join(h.cfg.RecordsDir, "inputs")
	}
	if h.cfg.NativeHistory != nil {
		journalDir = filepath.Join(h.cfg.NativeHistory.Directory, "receipts")
	}
	inputs, err := newInputJournal(journalDir, msg.SessionID)
	if err != nil {
		fail("the session's message receipt journal could not be opened")
		return
	}
	sess := &agentSession{skills: skillList, engine: msg.Engine, inputs: inputs, workspace: workDir, workspaceInfo: workspaceInfo}
	// A new session is idle until its first turn starts (the driver says
	// "running" then). Reporting "running" here made a session with no
	// initial prompt look busy — spinner, working indicator — while it was
	// really waiting for its first message.
	sess.status.Store("idle")
	emitter := newWSEmitter(h.sender, msg.SessionID, func(ev agent.Event) {
		if native != nil && native.observe(ev) != nil {
			if sess.inputFault.CompareAndSwap(false, true) {
				if sess.cancel != nil {
					sess.cancel()
				}
				go h.sendError(msg.SessionID, "Native conversation state could not be retained; the session stopped before accepting more work.")
				go h.kill(msg.SessionID, 1)
			}
		}
		if err := sess.inputs.settle(ev); err != nil {
			log.Printf("agent session %s: message receipt could not be saved: %v", msg.SessionID, err)
			if sess.inputFault.CompareAndSwap(false, true) {
				if sess.cancel != nil {
					sess.cancel()
				}
				// A full event buffer must not hold teardown hostage. The server also
				// fails any remaining outbox entries when the session's end arrives.
				go h.kill(msg.SessionID, 1)
				go func() {
					payload, _ := json.Marshal(agent.ErrorPayload{Message: "The session stopped because its message receipt could not be saved. Check what ran before retrying.", Retryable: false, ClientID: inputEventClientID(ev)})
					_ = h.sender.Send(protocol.AgentEvent{Type: "agent_event", SessionID: msg.SessionID, ClientEventID: ccUUID(), Ts: time.Now().UTC().Format(time.RFC3339Nano), Kind: "error", Payload: payload})
				}()
			}
		}
		if ev.Kind == "status_changed" {
			if p, ok := ev.Payload.(agent.StatusPayload); ok {
				sess.status.Store(p.Status)
				sess.record.setStatus(p.Status)
			}
		}
		if ev.Kind == "turn_done" && h.cfg.OnTurnDone != nil {
			h.cfg.OnTurnDone(msg.SessionID)
		}
		if ev.Kind == "turn_done" && h.cfg.UsageLimits != nil {
			switch {
			case msg.Engine == "codex":
				h.cfg.UsageLimits.CodexTurnEnded()
			case useClaudeCode: // the native loop bills an API key, not the account's limits
				h.cfg.UsageLimits.ClaudeTurnEnded()
			}
		}
		// What the recovery record keeps up with (a nil record ignores it all).
		switch p := ev.Payload.(type) {
		case agent.UserMessagePayload:
			if ev.Kind == "user_message" {
				sess.record.setCurrentTurnID(p.TurnID)
			}
		case agent.TurnDonePayload:
			if ev.Kind == "turn_done" {
				sess.record.turnEnded(p.StopReason)
			}
		case agent.ToolCallPayload:
			if ev.Kind == "tool_call" {
				sess.record.toolCall(p.CallID, p.Tool, p.Input, p.ParentCallID, true)
			}
		case agent.ToolResultPayload:
			if ev.Kind == "tool_result" {
				sess.record.toolCall(p.CallID, "", nil, p.ParentCallID, false)
			}
		case agent.ModelChangedPayload:
			if ev.Kind == "model_changed" {
				sess.record.setModel(p.Model, p.Effort)
			}
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
	sess.workspaceCtx = ctx

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
	var pluginHome string
	var pluginConfig map[string]any
	nativePluginPrepare := engineRegistry[msg.Engine].PreparePlugins
	if len(msg.Plugins) > 0 {
		stages := daemonStageReporter{sender: h.sender, sessionID: msg.SessionID}
		if ad != nil {
			stages.sender = nil // an adoption is not a start: there is no start panel to fill
		}
		reason := ""
		switch {
		case msg.RestrictTools:
			reason = "not loaded: a restricted session runs without plugins"
		case (!useClaudeCode && nativePluginPrepare == nil) || msg.Kind != "agent":
			reason = "not loaded: this engine has no native plugin installer"
		case h.cfg.Plugins == nil:
			reason = "not loaded: this daemon has no plugin workshop (no state directory)"
		}
		if reason != "" {
			if ad != nil {
				ad.pluginsLost = true
			}
			stages.report(protocol.StartStage{ID: protocol.StagePlugins, Label: "Installing plugins", State: protocol.StageStateWarning, Detail: reason})
		} else {
			stages.report(protocol.StartStage{ID: protocol.StagePlugins, Label: "Installing plugins",
				State: protocol.StageStateActive, Detail: fmt.Sprintf("Installing %d plugin(s)", len(msg.Plugins))})
			var dirs []string
			var res pluginsinstall.Result
			if nativePluginPrepare != nil {
				pluginHome, pluginConfig, res = nativePluginPrepare(ctx, h.cfg.Plugins, msg.SessionID, msg.Plugins)
			} else {
				dirs, res = h.cfg.Plugins.prepare(ctx, msg.SessionID, msg.Plugins)
				pluginDirs = dirs
			}
			if h.cfg.SpawnKilled != nil && h.cfg.SpawnKilled(msg.SessionID) {
				// Stopped while the plugins installed: the session must not start behind the user's back.
				log.Printf("agent session %s: spawn abandoned — killed while its plugins were being prepared", msg.SessionID)
				cancel()
				if pluginHome != "" {
					h.cfg.Plugins.releaseCodex(msg.SessionID)
				}
				if len(dirs) > 0 {
					h.cfg.Plugins.release(msg.SessionID)
				}
				return
			}
			log.Printf("agent session %s: plugins: %s", msg.SessionID, res.Detail())
			state := protocol.StageStateDone
			if len(res.Failed)+len(res.Skipped) > 0 {
				state = protocol.StageStateWarning
				if ad != nil {
					ad.pluginsLost = true
				}
			}
			stages.report(protocol.StartStage{ID: protocol.StagePlugins, Label: "Installing plugins", State: state, Detail: res.Detail()})
		}
	}
	// Interactive grant homes are prepared before Docker mounts are assembled.
	runtimeHome := pluginHome
	standaloneGrantHome := ""
	grantSource := engineHome(h.cfg.HomeDir, "CODEX_HOME", ".codex")
	if useCodex && msg.MCPGateway != nil && !msg.RestrictTools {
		runtimeWork := workDir
		if msg.Sandbox {
			runtimeWork = sandboxWorkdir
		}
		var err error
		if runtimeHome == "" {
			runtimeHome, err = prepareCodexInteractiveHome(ctx, grantSource, runtimeWork)
			standaloneGrantHome = runtimeHome
		} else {
			err = sanitizeCodexInteractiveHome(ctx, runtimeHome, grantSource, runtimeWork)
		}
		if err != nil {
			cancel()
			if pluginHome != "" {
				h.cfg.Plugins.releaseCodex(msg.SessionID)
			}
			fail("Codex interactive MCP isolation could not be prepared")
			return
		}
	}
	// releasePlugins drops the session's plugin snapshot when the spawn fails
	// after preparing it; a started session's goes on Kill.
	releasePlugins := func() {
		if standaloneGrantHome != "" {
			_ = os.RemoveAll(standaloneGrantHome)
		}
		if pluginHome != "" {
			h.cfg.Plugins.releaseCodex(msg.SessionID)
		}
		if len(pluginDirs) > 0 {
			h.cfg.Plugins.release(msg.SessionID)
		}
	}

	var prefix sandboxExec
	if msg.Sandbox {
		sbOpts := h.cfg.Sandbox
		if runtimeHome != "" {
			sbOpts.CodexHome = runtimeHome
			sbOpts.CodexSourceHome = grantSource
		}
		sbOpts.ClaudeOnly = msg.RestrictTools || (!useCodex && msg.MCPGateway != nil)
		if useCodex && sbOpts.ClaudeOnly {
			sbOpts.RestrictedEngine = "codex"
		}
		if len(pluginDirs) > 0 {
			sbOpts.PluginCache = h.cfg.Plugins.sessionDir(msg.SessionID)
			pluginDirs = translatePluginDirs(pluginDirs, sbOpts.PluginCache)
		}
		container, err := startSandboxContainer(msg.SessionID, workDir, env, sandboxCred, sbOpts)
		if err != nil {
			cancel()
			releasePlugins()
			fail("could not start the sandbox container: " + err.Error())
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
		// Restricted (a cron's, or a board-started grant session). A grant alone
		// is not: the driver adds only the grant's own flags for it.
		ccOpts = append(ccOpts, withRestrictTools())
	}
	// Whether a person is reading the chat as the session works: the driver says so in the
	// system prompt of every unrestricted turn ("" from an older server is interactive).
	ccOpts = append(ccOpts, withInteraction(msg.Interaction))
	if len(pluginDirs) > 0 {
		ccOpts = append(ccOpts, withPluginDirs(pluginDirs))
	}
	if msg.MCPGateway != nil && !useCodex {
		mcpFile = newMCPConfigFile(msg.MCPGateway, prefix)
		if _, err := mcpFile.Ensure(); err != nil {
			cancel()
			if msg.Sandbox {
				removeSandboxContainer(msg.SessionID)
			}
			releasePlugins()
			fail(err.Error())
			return
		}
		ccOpts = append(ccOpts, withMCPConfigSource(mcpFile))
		sess.mcp = mcpFile
	}
	if useCodex && (msg.RestrictTools || msg.MCPGateway != nil) {
		var runtime *codexRestrictedRuntime
		var err error
		if msg.RestrictTools {
			runtime, err = prepareCodexRestrictedRuntime(ctx, workDir, grantSource, prefix, msg.MCPGateway, h.cfg.CredentialSource != nil)
		} else {
			runtime, err = prepareCodexGrantedRuntime(ctx, runtimeHome, prefix, msg.MCPGateway)
		}
		if err != nil {
			cancel()
			if msg.Sandbox {
				removeSandboxContainer(msg.SessionID)
			}
			releasePlugins()
			fail(err.Error())
			return
		}
		sess.codexRestricted = runtime
	}

	var loop *agent.Loop
	var driver sessionDriver
	switch {
	case cliEngine:
		spec := engineRegistry[msg.Engine]
		log.Printf("agent session %s: %s driver", msg.SessionID, spec.DisplayName)
		driver = spec.NewAgentDriver(AgentDriverOpts{
			CredentialSource: h.cfg.CredentialSource, Spec: spec, WorkDir: workDir, Model: model, Effort: msg.Effort,
			SessionID: msg.SessionID, Emitter: emitter, Env: env,
		})
		if cx, ok := driver.(*codexDriver); ok {
			cx.DeveloperInstructions = ccSystemPromptFor(msg.Interaction)
			cx.AskUser = codexQuestionHandler(h.cfg.ServerHTTP, h.cfg.DaemonToken, msg.SessionID, emitter, func() string { cx.mu.Lock(); defer cx.mu.Unlock(); return cx.usageTurnID })
			cx.CodexHome = pluginHome
			if pluginConfig != nil {
				cx.RuntimeConfig = pluginConfig
			}
			if sess.codexRestricted != nil {
				cx.RestrictedRuntime = sess.codexRestricted
				cx.CodexHome = sess.codexRestricted.Home
			}
		}
		if cx, ok := driver.(*codexDriver); ok && h.recordable(msg) {
			sess.record = h.keepRecord(msg, workDir, model, ad)
			cx.onThreadID = func(id string) { sess.record.update(func(r *AgentRecord) bool { r.CodexThreadID = id; return true }) }
			cx.onEngine = sess.record.setEngine
			cx.onOpenWork = sess.record.setOpenWork
			cx.onKnownChildren = sess.record.setKnownSubagents
			if ad != nil {
				cx.threadID = ad.resumeID
				cx.seedKnownChildren(append(append([]OpenSubagent(nil), ad.rec.KnownSubagents...), ad.rec.OpenSubagents...))
				cx.usageTurnID = ad.rec.CurrentTurnID
				// What was cut off, then why the daemon restarted (as finishAdoption shows it).
				note := cutOffNotice(ad.rec.OpenCalls, ad.rec.OpenTasks, ad.rec.OpenSubagents)
				if note != "" && ad.resumeID != "" {
					note += "\n\n" + h.restartCauseWords(ad.rec)
				}
				cx.adoptionNote = note
			}
		}
	case useClaudeCode:
		log.Printf("agent session %s: claude-code driver (the engine's own login)", msg.SessionID)
		cc := newClaudeCodeDriver(workDir, model, msg.Effort, emitter, env, ccOpts...)
		cc.skills, cc.home = skillList, h.cfg.HomeDir
		if h.cfg.UsageLimits != nil {
			cc.onRateLimit = h.cfg.UsageLimits.ClaudeRateLimited
		}
		driver = cc
		if h.recordable(msg) {
			sess.record = h.keepRecord(msg, workDir, model, ad)
			cc.onSessionID = sess.record.setClaudeSessionID
			cc.onEngine = sess.record.setEngine
			cc.onOpenWork = sess.record.setOpenWork
		}
		if ad != nil {
			// What was cut off goes with the next message: the person's, or recovery's own continue
			// (finishAdoption).
			a := adoption{hadConversation: ad.rec.ClaudeSessionID != "", cutOff: ad.rec.TurnActive,
				work:  cutOffNotice(ad.rec.OpenCalls, ad.rec.OpenTasks, ad.rec.OpenSubagents),
				cause: h.restartCauseWords(ad.rec)}
			cc.adopt(ad.resumeID, a)
			cc.usageTurnID = ad.rec.CurrentTurnID
			// The agent may resume what the restart cut off: its runs open as resumed subagents.
			_, agents := cutOffSplit(ad.rec.OpenTasks, ad.rec.OpenSubagents)
			cc.seedKnownAgents(agents)
		}
	default:
		// The native loop's provider, registry, data-plane context and system prompt (with the
		// enabled plugins' SessionStart hook context) are built here, after the engine decision:
		// a CLI engine never uses them, and preparing them runs plugin hook commands.
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
		loop = agent.NewLoop(h.nativeLoopConfig(msg, nativeLoopInputs{
			workDir: workDir, env: env, model: model, skills: skillList, agentTypes: agentTypes,
			emitter: emitter, messenger: messenger, confined: msg.Sandbox || msg.RestrictTools || prefix.enabled(),
		}))
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
			fail(sandboxUnsupportedDriverRefusal)
			return
		}
		sb.useSandbox(prefix)
	}
	if len(restore) > 0 {
		driver.RestoreContext(restore)
	}
	if native != nil {
		fault := func() {
			if sess.inputFault.CompareAndSwap(false, true) {
				cancel()
				go h.sendError(msg.SessionID, "Native conversation identity could not be retained; the session stopped before accepting more work.")
				go h.kill(msg.SessionID, 1)
			}
		}
		if err := native.bind(driver, h.cfg.HomeDir, fault); err != nil {
			cancel()
			mcpFile.Remove()
			if sess.codexRestricted != nil {
				sess.codexRestricted.Close()
			}
			releasePlugins()
			fail(err.Error())
			return
		}
	}

	runDone := make(chan struct{})
	sess.runDone = runDone
	sess.loop = driver
	sess.cancel = cancel
	sess.plugins = len(pluginDirs) > 0
	sess.pluginHome = pluginHome != ""

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

	if ad == nil {
		_ = h.sender.Send(protocol.SessionStarted{
			Type: "session_started", SessionID: msg.SessionID,
			ProjectPath: workDir, Repo: msg.Repo, Title: msg.Title, Model: model,
			Kind: "agent",
		})
	}
	// What the session has loaded, for the Skills & plugins panel. Claude
	// Code reports its own from each turn's init line (claudeCodeDriver); the
	// native loop knows its lists; other engines may have a best-effort
	// source on their EngineSpec. Nothing else is guessed.
	var caps capEmitter
	switch {
	case loop != nil:
		caps.emit(emitter, nativeCapabilities(model, skillList, agentTypes, loop.ToolDefs(), workDir, h.cfg.HomeDir))
	case cliEngine:
		if _, runtimeCaps := driver.(*codexDriver); runtimeCaps {
			break
		}
		if p, ok := engineCapabilities(engineRegistry[msg.Engine], model, CapabilityContext{
			WorkDir: workDir, HomeDir: h.cfg.HomeDir, Sandboxed: prefix.enabled(),
		}); ok {
			caps.emit(emitter, p)
		}
	}
	driver.Start()
	if source, ok := driver.(mcpStatusSource); ok {
		if gate, supported := driver.(interface{ SetMCPInputGate(func(context.Context)) }); supported {
			engine := msg.Engine
			if engine == "" {
				engine = "claude"
			}
			tracker := newMCPTracker(ctx, engine, source, emitter, mcpTrackerOptions{
				Wait:      parseMCPPromptWait(os.Getenv("BLERG_RUNNER_MCP_PROMPT_WAIT_S")),
				FirstUsed: mcpFirstPromptUsed(restore, ad),
			})
			sess.mcpState.Store(tracker)
			gate.SetMCPInputGate(tracker.beforeFirstPrompt)
		}
	}
	nativeOwned = true
	go func() {
		defer close(runDone)
		if native != nil {
			defer func() { _ = native.root.Close() }()
		}
		driver.Run(ctx)
	}()
	if ad != nil {
		h.finishAdoption(ad, sess, driver, msg)
		return
	}
	if note != "" {
		// The daemon's note, not the person's: source "system" keeps it off their name.
		driver.EnqueueInput(agent.Input{Text: note, Source: "system"})
	}
	if strings.TrimSpace(msg.InitialPrompt) != "" {
		source := msg.InitialPromptSource
		if source == "" {
			source = "chat"
		}
		h.UserInput(msg.SessionID, agent.Input{Text: msg.InitialPrompt, Source: source, ClientID: msg.InitialPromptClientID})
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
	h.UserInput(sessionID, agent.Input{Text: text, Source: source})
}

// UserInput is UserMessage with the sender's client id (in.ClientID), echoed on the message's
// user_message.
func (h *AgentHost) UserInput(sessionID string, in agent.Input) {
	sess := h.get(sessionID)
	if sess == nil {
		// Being brought back after a restart: the message waits for it.
		if h.holdForAdoption(sessionID, in) {
			return
		}
		// Not a session this daemon is hosting (it ended, or it was lost in a restart and could
		// not be adopted). Say so in its transcript rather than swallowing the message.
		h.reportUndelivered(sessionID, in.ClientID)
		return
	}
	if sess.inputFault.Load() {
		h.reportUndelivered(sessionID, in.ClientID)
		return
	}
	if in.Source == "" {
		in.Source = "chat"
	}
	text := in.Text
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "/model ") {
		sess.changeModel(strings.TrimSpace(strings.TrimPrefix(trimmed, "/model ")), "", "command")
		return
	}
	if strings.HasPrefix(trimmed, "/effort ") {
		sess.changeModel("", strings.TrimSpace(strings.TrimPrefix(trimmed, "/effort ")), "command")
		return
	}
	send, receipt, err := sess.inputs.accept(in)
	if err != nil {
		sess.emitter.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "error", Payload: agent.ErrorPayload{Message: "Message not delivered: it could not be safely queued.", Retryable: true, ClientID: in.ClientID}})
		return
	}
	if receipt != nil {
		sess.emitter.Replay(*receipt)
		return
	}
	if !send {
		return
	}
	if resolved, ok, err := skills.ResolveSlash(sess.skills, trimmed); sess.engine != "codex" && err == nil && ok {
		// The engine gets the skill's instructions; the transcript keeps what the person typed.
		in.Text, in.Shown = resolved, text
		sess.loop.EnqueueInput(in)
		return
	}
	sess.loop.EnqueueInput(in)
}

// SetModel applies a model/effort change from the UI.
func (h *AgentHost) SetModel(sessionID, model, effort string) {
	if sess := h.get(sessionID); sess != nil {
		sess.changeModel(model, effort, "ui")
	}
}

// RefreshTools asks the session's engine to list its MCP tools again (a grant was re-pinned or
// extended). Engines that cannot are left alone.
func (h *AgentHost) RefreshTools(sessionID string) {
	sess := h.get(sessionID)
	if sess == nil {
		return
	}
	if r, ok := sess.loop.(toolRefresher); ok {
		r.refreshTools()
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
// ReleaseHeldPrompt is idempotent and never creates or resumes a session.
func (h *AgentHost) ReleaseHeldPrompt(sessionID string) {
	if sess := h.get(sessionID); sess != nil {
		if tracker := sess.mcpState.Load(); tracker != nil {
			tracker.release()
		}
	}
}

func (h *AgentHost) Interrupt(sessionID string) {
	if sess := h.get(sessionID); sess != nil {
		sess.loop.Interrupt()
	}
}

// agentDriverStopTimeout bounds teardown when a driver fails to honor cancellation.
var agentDriverStopTimeout = 5 * time.Second

// Kill stops the session entirely.
func (h *AgentHost) Kill(sessionID string) { h.kill(sessionID, 0) }

func (h *AgentHost) kill(sessionID string, exitCode int) {
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
	// Drivers finish subprocess teardown and final filesystem writes after their
	// context is canceled. Keep their container, plugin home and records intact
	// until then; otherwise cleanup can race deletion or recreate killed state.
	if sess.runDone != nil {
		timer := time.NewTimer(agentDriverStopTimeout)
		select {
		case <-sess.runDone:
		case <-timer.C:
			log.Printf("agent session %s: driver did not stop before the cleanup deadline", sessionID)
		}
		timer.Stop()
	}
	sess.mcp.Remove() // before the container goes, so the file is deleted in it too
	if sess.codexRestricted != nil {
		sess.codexRestricted.Close()
	}
	if sess.sandboxed {
		// Same teardown a terminal sandbox gets on kill (killTmuxSession): the
		// container outlives nothing.
		removeSandboxContainer(sessionID)
	}
	if sess.pluginHome {
		h.cfg.Plugins.releaseCodex(sessionID)
	}
	if sess.plugins {
		h.cfg.Plugins.release(sessionID) // after the container: the snapshot was mounted in it
	}
	sess.record.delete() // killed for good: nothing to host again after a restart
	sess.inputs.close()
	removeSessionTokenFile(DaemonStateDir(), sessionID)
	_ = h.sender.Send(protocol.SessionEnded{Type: "session_ended", SessionID: sessionID, ExitCode: exitCode})
}

// HandleAck removes a server-persisted event from the pending buffer.
func (h *AgentHost) HandleAck(sessionID, clientEventID string) {
	if sess := h.get(sessionID); sess != nil {
		if err := sess.inputs.ack(clientEventID); err != nil {
			log.Printf("agent session %s: message receipt acknowledgment could not be saved: %v", sessionID, err)
		}
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
	ids := make([]string, 0, len(h.sessions)+len(h.adopting))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	// Sessions still being brought back after a restart are this daemon's too: left out, the
	// server would end them as "no longer reported" while they are being restored.
	for id := range h.adopting {
		if _, hosted := h.sessions[id]; !hosted {
			ids = append(ids, id)
		}
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

// nativeLoopInputs is what the native loop's configuration is built from.
type nativeLoopInputs struct {
	workDir    string
	env        []string
	model      string
	skills     []skills.Skill
	agentTypes []agent.AgentTypeDef
	emitter    agent.Emitter
	messenger  agent.Messenger
	// confined: a restricted or sandboxed session. Its preparation runs no plugin hook command
	// on the host (today a confined session never reaches the native loop: this is the backstop).
	confined bool
}

// nativeLoopConfig builds the native API loop's configuration: its provider, tool registry,
// data-plane context and system prompt. Only the native driver calls it.
func (h *AgentHost) nativeLoopConfig(msg protocol.SpawnSession, in nativeLoopInputs) agent.Config {
	provider := h.cfg.Provider
	if provider == nil {
		provider = anthropic.New(h.cfg.APIKey, "")
	}
	registryTools := []agent.Tool{
		tools.ReadFile(in.workDir), tools.Glob(in.workDir), tools.Grep(in.workDir),
		tools.WriteFile(in.workDir), tools.EditFile(in.workDir), tools.BashEnv(in.workDir, in.env),
		agent.SkillTool(in.skills),
	}
	dataPlaneCtx := ""
	if h.cfg.ServerHTTP != "" {
		pub := tools.PublishConfig{Base: h.cfg.ServerHTTP, Token: h.cfg.DaemonToken}
		dp := tools.DataPlaneConfig{Base: h.cfg.ServerHTTP, Token: h.cfg.DaemonToken, Project: msg.Repo}
		registryTools = append(registryTools,
			tools.PushMockup(in.workDir, pub), tools.PushScreenshot(in.workDir, pub),
			tools.MemoryWrite(dp), tools.MemoryDelete(dp),
			tools.RulePropose(dp), tools.KnowledgeSearch(dp))
		dataPlaneCtx = fetchDataPlaneContext(h.cfg.ServerHTTP, h.cfg.DaemonToken, msg.Repo)
	}
	return agent.Config{
		Provider:  provider,
		Emitter:   in.emitter,
		Registry:  agent.NewRegistry(registryTools...),
		System:    BuildSystemPrompt(in.workDir, h.cfg.HomeDir, in.skills, in.agentTypes, msg.Interaction, !in.confined) + dataPlaneCtx,
		Model:     in.model,
		Effort:    msg.Effort,
		Messenger: in.messenger,
		BudgetUSD: h.cfg.BudgetUSD,
		Pricing:   h.cfg.Pricing,
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
