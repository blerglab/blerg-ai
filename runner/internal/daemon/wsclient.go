// Package daemon implements the blerg-runner daemon: a WebSocket client that
// connects to the blerg-runner server and manages local Claude Code sessions.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
)

const (
	defaultProtocolVersion   = "1"
	defaultHeartbeatInterval = 30 * time.Second
	defaultReconnectInitial  = 2 * time.Second
	defaultReconnectMax      = 60 * time.Second
	defaultUnconfirmedAfter  = 5 * time.Second

	// closeUnauthorized is the close code the server answers a refused token with.
	closeUnauthorized = 4401
)

// Config holds the daemon client configuration.
type Config struct {
	WorkspaceCapabilities protocol.WorkspaceCapabilities
	// Evaluated before each dial, using installed runtime backends and helpers.
	WorkspaceCapabilitiesProvider func(context.Context) protocol.WorkspaceCapabilities
	RequiredWorkspace             *protocol.WorkspaceManifest
	OnUnsupportedWorkspace        func()
	ServerURL                     string
	DaemonToken                   string
	DesktopID                     string
	DaemonName                    string
	DaemonMode                    string
	ReposRoot                     string
	ProtocolVersion               string
	Version                       string
	// AllowHostCredentialClone is reported in the hello — see
	// ManagerConfig.AllowHostCredentialClone.
	AllowHostCredentialClone bool

	// ConnectionFile is where the client keeps connection.json (ConnectionState)
	// up to date; "" keeps none. Never written in mode "runner": a session
	// pod's HOME is the agent's workspace.
	ConnectionFile string

	// Tunable timings. Zero values use the package defaults.
	HeartbeatInterval time.Duration
	ReconnectInitial  time.Duration
	ReconnectMax      time.Duration
	// UnconfirmedAfter is how long a link must stay open without a
	// daemon_welcome (a server older than 0.2.1 sends none) before it is taken
	// as accepted, unconfirmed.
	UnconfirmedAfter time.Duration
}

func (c *Config) heartbeatInterval() time.Duration {
	if c.HeartbeatInterval > 0 {
		return c.HeartbeatInterval
	}
	return defaultHeartbeatInterval
}

func (c *Config) reconnectInitial() time.Duration {
	if c.ReconnectInitial > 0 {
		return c.ReconnectInitial
	}
	return defaultReconnectInitial
}

func (c *Config) reconnectMax() time.Duration {
	if c.ReconnectMax > 0 {
		return c.ReconnectMax
	}
	return defaultReconnectMax
}

func (c *Config) unconfirmedAfter() time.Duration {
	if c.UnconfirmedAfter > 0 {
		return c.UnconfirmedAfter
	}
	return defaultUnconfirmedAfter
}

// WSClient is a WebSocket client that maintains a persistent connection to the
// blerg-runner server, reconnecting automatically with exponential backoff.
type WSClient struct {
	workspaceOutput        workspaceOutputQueue
	workspaceDisconnect    func()
	serverWorkspaceVersion atomic.Int32
	workspaceNegotiated    atomic.Bool
	discoveryVersion       int
	discoveryCtx           context.Context
	shellRuntime           *shellruntime.Service
	fair                   *shellruntime.FairWriter
	fairConn               *websocket.Conn
	recoveryMu             sync.Mutex
	recoveryState          RecoveryState
	recoveryHandler        func(RecoveryState)
	serverPlanNudges       atomic.Bool
	config                 Config
	mu                     sync.Mutex // guards conn and handlers
	writeMu                sync.Mutex // guards WriteMessage calls (at most one concurrent writer)
	conn                   *websocket.Conn
	background             *backgroundTaskTracker // guarded by mu; process lifetime and replay-safe live IDs
	handlers               map[string]func([]byte)
	onConnect              func()
	sessionStates          func() map[string]string
	// queuedSessions lists the spawns received but not started (Manager.QueuedSpawnIDs).
	queuedSessions           func() []string
	recoveryRequiredSessions func() []string
	engineModels             EngineModelSource
	reposRootFn              func() string
	hbKick                   chan struct{} // RequestHeartbeat: send one now
	// status is the link's state, mirrored to Config.ConnectionFile.
	status *connReporter

	// sleep is the injectable backoff wait, so tests can observe/short-circuit
	// it without real time passing. Returns false if ctx was done.
	sleep func(ctx context.Context, d time.Duration) bool
}

// SetOnConnect registers a callback fired (in its own goroutine) after each
// successful connection and hello. Used to reattach persistent sessions.
// SetWorkspaceDisconnect installs the explorer's cancel/join hook. It runs
// after the old connection closes and before another connection is accepted.
func (c *WSClient) SetWorkspaceDisconnect(fn func()) {
	c.mu.Lock()
	c.workspaceDisconnect = fn
	c.mu.Unlock()
}

func (c *WSClient) SetOnConnect(fn func()) {
	c.onConnect = fn
}

// SetQueuedSessions registers a provider for the spawns received but not
// started yet, reported in the hello and every heartbeat (QueuedSessions).
func (c *WSClient) SetQueuedSessions(fn func() []string) {
	c.queuedSessions = fn
}

func (c *WSClient) SetRecoveryRequiredSessions(fn func() []string) {
	c.recoveryRequiredSessions = fn
}

// SetSessionStates registers a provider for per-session detector states,
// included in each heartbeat so the server can reconcile dropped state changes.
func (c *WSClient) SetSessionStates(fn func() map[string]string) {
	c.sessionStates = fn
}

// SetReposRoot registers the live repos-root source (the daemon's
// ReposRootSetting), so hello and heartbeat report — and list repos under —
// the root in effect now rather than the one the client was built with. The
// owner can change it from the app without a reconnect.
func (c *WSClient) SetReposRoot(fn func() string) {
	c.reposRootFn = fn
}

// RequestHeartbeat asks for a heartbeat now instead of at the next tick
// (after something it reports changed). Never blocks; requests made while
// one is already pending collapse into it.
func (c *WSClient) RequestHeartbeat() {
	select {
	case c.hbKick <- struct{}{}:
	default:
	}
}

// reposRoot is the repos root to report: the live source when one is
// registered, else the configured value (the cluster pod).
func (c *WSClient) reposRoot() string {
	if c.reposRootFn != nil {
		return c.reposRootFn()
	}
	return c.config.ReposRoot
}

// EngineModelSource is what the client reports as DaemonHello/Heartbeat
// .EngineModels — a ModelProber in the daemon. Snapshot's generation changes
// exactly when a list does; Changed is signalled when it has.
type EngineModelSource interface {
	Snapshot() (map[string]models.Report, uint64)
	Changed() <-chan struct{}
}

// SetEngineModels registers the probed model lists to report: in full with
// every hello, and in a heartbeat only when they changed since the last
// message on this connection — sent right away rather than on the next tick.
// Without one (the cluster pod) nothing is reported.
func (c *WSClient) SetEngineModels(src EngineModelSource) {
	c.engineModels = src
}

// NewWSClient creates a new WSClient with the given configuration.
func NewWSClient(cfg Config) *WSClient {
	if cfg.ProtocolVersion == "" {
		cfg.ProtocolVersion = defaultProtocolVersion
	}
	if cfg.DaemonMode == "runner" {
		cfg.ConnectionFile = ""
	}
	return &WSClient{
		background: newBackgroundTaskTracker(),
		config:     cfg,
		handlers:   make(map[string]func([]byte)),
		hbKick:     make(chan struct{}, 1),
		status:     newConnReporter(cfg.ConnectionFile, cfg.ServerURL),
		sleep: func(ctx context.Context, d time.Duration) bool {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(d):
				return true
			}
		},
	}
}

// OnMessage registers a handler for a given message type field value. The
// handler receives the raw JSON bytes of the message.
func (c *WSClient) OnMessage(msgType string, handler func([]byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[msgType] = handler
}

// Send JSON-encodes msg and writes it to the current connection. Returns an
// error if the client is disconnected.
func (c *WSClient) Send(msg any) error { return c.SendContext(context.Background(), msg) }
func (c *WSClient) SendContext(ctx context.Context, msg any) error {
	if !c.workspaceDispatchAllowed() {
		return errors.New("workspace peer is unavailable or unsupported")
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if !isOwnerShellFrame(raw) {
		c.observeBackgroundTasks(raw)
	}
	return c.sendFair(ctx, raw)
}

// SendBounded sends private runtime replies with a bounded lock and write wait.
func (c *WSClient) SendBounded(ctx context.Context, msg any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 || !c.workspaceDispatchAllowed() {
		return errors.New("runtime reply unavailable")
	}
	return c.sendFair(ctx, raw)
}

// Run connects to the server, sends daemon_hello, and runs the read loop. On
// disconnect it reconnects with exponential backoff. Run blocks until ctx is
// cancelled.
//
// activeSessions is called each time a connection is (re-)established and
// its return value populates the active_sessions field of daemon_hello.
func (c *WSClient) Run(ctx context.Context, activeSessions func() []string) {
	defer func() {
		c.mu.Lock()
		service := c.shellRuntime
		fair := c.fair
		c.mu.Unlock()
		if fair != nil {
			fair.Close()
		}
		if service != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			_ = service.Shutdown(cleanup)
		}
	}()
	backoff := c.config.reconnectInitial()
	var lastErr string
	var lastLog time.Time
	// Before the first dial: whatever an earlier process left in the file is not this one's.
	c.status.set(ConnStateConnecting, "starting; connecting to the server")
	for {
		link, err := c.connect(ctx, activeSessions)
		if ctx.Err() != nil || err == nil {
			return
		}
		refused := isRefusal(err)
		if link.up && !refused {
			// The link was up (confirmed, or open for the unconfirmed window):
			// start the ladder over. A refusal never does — a daemon with a
			// wrong token must not retry every 2 s forever.
			backoff = c.config.reconnectInitial()
		}
		switch {
		case refused:
			c.status.set(ConnStateRefused, "the server refused the daemon token (close 4401)")
		case link.up || c.status.current() != ConnStateRefused:
			// After a refusal, only a link that is up says anything new about the
			// token; a server that cannot be reached leaves "refused" standing.
			c.status.set(ConnStateConnecting, shortDetail("not connected: "+err.Error()))
		}
		// Log the first failure, then identical failures once a minute — a
		// server that's down for an hour must not produce 1800 lines.
		if err.Error() != lastErr || time.Since(lastLog) > time.Minute {
			if refused {
				log.Print(c.refusedLine(backoff))
			} else {
				log.Printf("daemon: disconnected (%v); reconnecting in %s", err, backoff)
			}
			lastErr, lastLog = err.Error(), time.Now()
		}
		if !c.sleep(ctx, backoff) {
			return
		}
		backoff *= 2
		if backoff > c.config.reconnectMax() {
			backoff = c.config.reconnectMax()
		}
	}
}

// isRefusal reports whether err is the server's 4401 close: the token was refused.
func isRefusal(err error) bool {
	var ce *websocket.CloseError
	return errors.As(err, &ce) && ce.Code == closeUnauthorized
}

// refusedLine is the one loud log line for a 4401: what was refused, where that token comes from
// for this kind of daemon, and that it keeps retrying.
func (c *WSClient) refusedLine(retry time.Duration) string {
	server := redactServerURL(c.config.ServerURL)
	if c.config.DaemonMode == "runner" {
		return fmt.Sprintf("daemon: REFUSED: the server at %s refused this session pod's runtime token (close 4401): "+
			"the launch it was issued for has ended or was replaced. Retrying in %s.", server, retry)
	}
	return fmt.Sprintf("daemon: REFUSED: the server at %s refused the daemon token (close 4401): this daemon's "+
		"BLERG_RUNNER_DAEMON_TOKEN is not the server's. It comes from the daemon's environment: for the installed "+
		"service, install/desktop/daemon/install.sh copies it from BLERG_RUNNER_DAEMON_TOKEN in install/desktop/.env "+
		"(a daemon started by hand takes it from your shell). Put the server's token there and run install.sh "+
		"install again. Retrying in %s.", server, retry)
}

// shortDetail keeps a connection-file detail to one short line.
func shortDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:197] + "..."
	}
	return s
}

// linkResult is how far one connection got: up means the server confirmed it (daemon_welcome)
// or it stayed open for the unconfirmed window.
type linkResult struct {
	up bool
}

// linkWatch tracks one connection from the written hello until it closes: the welcome, the
// unconfirmed window, and what each of them logs and records.
type linkWatch struct {
	c         *WSClient
	mu        sync.Mutex
	confirmed bool // the welcome arrived
	assumed   bool // the window passed without one
	closed    bool
}

// welcome handles daemon_welcome: the token was accepted. Late (after the window) is fine.
func (w *linkWatch) welcome(raw []byte) {
	var msg protocol.DaemonWelcome
	_ = json.Unmarshal(raw, &msg)
	name := msg.Name
	if name == "" {
		name = w.c.config.DaemonName
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.confirmed {
		return
	}
	w.confirmed = true
	w.c.status.set(ConnStateConnected, shortDetail(fmt.Sprintf("accepted by the server as %q", name)))
	log.Printf("daemon: connected to %s as %q", redactServerURL(w.c.config.ServerURL), name)
}

// windowPassed runs when the link has stayed open for the unconfirmed window.
func (w *linkWatch) windowPassed() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.confirmed || w.assumed {
		return
	}
	w.assumed = true
	if w.c.config.RequiredWorkspace != nil {
		w.c.status.set(ConnStateUnconfirmed, "workspace v2 requires confirmed peer capabilities")
		if w.c.config.OnUnsupportedWorkspace != nil {
			w.c.config.OnUnsupportedWorkspace()
		}
		return
	}
	w.c.status.set(ConnStateUnconfirmed, "connected, but the server did not confirm it (a server older than 0.2.1 sends no confirmation); treated as accepted")
	// Not "daemon: connected to …": that line means the server confirmed the token.
	log.Printf("daemon: link to %s open for %s but not confirmed by the server (a server older than 0.2.1 sends no confirmation); treating it as accepted",
		redactServerURL(w.c.config.ServerURL), w.c.config.unconfirmedAfter())
}

// close ends the watch and reports whether the link was up.
func (w *linkWatch) close() linkResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return linkResult{up: w.confirmed || w.assumed}
}

// queued is the queued-spawn report, nil without a provider.
func (c *WSClient) queued() []string {
	if c.queuedSessions == nil {
		return nil
	}
	return c.queuedSessions()
}

// connect dials the server, sends hello, starts the heartbeat goroutine, then
// reads until the connection closes or ctx is cancelled. It returns whether
// the link was up (linkResult) and the error from the read loop (nil error if
// shutdown was due to ctx cancellation).
func (c *WSClient) connect(ctx context.Context, activeSessions func() []string) (linkResult, error) {
	c.workspaceNegotiated.Store(false)
	c.serverWorkspaceVersion.Store(0)
	capabilities := c.config.WorkspaceCapabilities
	if c.config.WorkspaceCapabilitiesProvider != nil {
		capabilities = c.config.WorkspaceCapabilitiesProvider(ctx)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.config.ServerURL, nil) //nolint:bodyclose // gorilla/websocket: after a successful dial the response body is the connection and is closed with conn
	if err != nil {
		return linkResult{}, err
	}

	linkCtx, linkCancel := context.WithCancel(ctx)
	defer linkCancel()
	c.mu.Lock()
	shellService := c.shellRuntime
	c.mu.Unlock()
	if shellService != nil {
		shellService.SetCapabilities(protocol.WorkspaceCapabilities{}, protocol.WorkspaceCapabilities{})
		defer shellService.SetCapabilities(protocol.WorkspaceCapabilities{}, protocol.WorkspaceCapabilities{})
		rebind, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := shellService.Rebind(rebind)
		cancel()
		if err != nil {
			_ = conn.Close()
			return linkResult{}, err
		}
	}
	generation := c.beginRecoveryLink()
	defer c.noteRecoveryLink(generation, RecoveryDisconnected)

	// Send daemon_hello. The connection is not c.conn until the hello is written: the server
	// reads the first frame as the hello, so Send (any other sender) fails "not connected" until then.
	sessions := activeSessions()
	if sessions == nil {
		sessions = []string{}
	}
	root := c.reposRoot()
	checkedOut, origins, remotes, limited := []string{}, map[string]protocol.RepoOrigin(nil), map[string]string(nil), false
	if c.config.DaemonMode != "runner" {
		checkedOut, origins, remotes, limited = diskRepoInventory(ctx, root)
	}
	hello := protocol.DaemonHello{
		Type:                  "daemon_hello",
		WorkspaceCapabilities: capabilities,
		Name:                  c.config.DaemonName,
		Mode:                  c.config.DaemonMode,
		ReposRoot:             root,
		Token:                 c.config.DaemonToken,
		DesktopID:             c.config.DesktopID,
		DiscoveryVersion:      c.discoveryVersion,
		ProtocolVersion:       c.config.ProtocolVersion,
		Version:               c.config.Version,
		ActiveSessions:        sessions,
		CheckedOutRepos:       checkedOut,
		SandboxAvailable:      sandboxImageAvailable(),
		AvailableEngines:      availableEngines(),
		LocalEngines:          localEngines(),
		ClaudeCLIAvailable:    claudeOnPath(),
		AnthropicKeySet:       os.Getenv("ANTHROPIC_API_KEY") != "",

		SandboxClaudeCredential: sandboxClaudeCredentialReport(),
	}
	hello.RepoOrigins, hello.RepoRemotes = origins, remotes
	hello.RepoInventoryLimited = limited
	hello.GitProviders = gitprovider.Default.CredentialKinds()
	hello.CloneFrom = true // SpawnSession.CloneFrom (EnsureClonedTarget)
	hello.AllowHostCredentialClone = c.config.AllowHostCredentialClone
	hello.MCPGateway = true                         // SpawnSession.MCPGateway (mcpgateway.go)
	hello.MCPGatewayViaServer = true                // MCPGatewayConfig.ViaServer, host board grants (mcpgateway.go)
	hello.CodexRestricted = runtime.GOOS == "linux" // Spawn rechecks the actual CLI version before exposing only rooted dynamic tools.
	hello.PlanNudges = true
	hello.RestrictTools = true // SpawnSession.RestrictTools (claudecode.go ccHardeningFlags)
	hello.PluginEngines = pluginEnginesCapability()
	if pluginsCapability() {
		hello.SessionPluginEngines = []string{"claude"}
	}
	hello.Plugins = pluginsCapability()             // SpawnSession.Plugins (pluginworkshop.go)
	hello.PluginCatalogEngines = extensionEngines() // ReadPluginCatalog (engineextensions.go)
	hello.MCPSignInEngines = extensionEngines()     // MCPSignIn (engineextensions.go)
	hello.InputReceipts = true
	hello.SandboxClones = true    // every Sandbox spawn works in its own clone (sandboxclone.go)
	hello.SessionTokenFile = true // SessionToken → SessionTokenResult (sessiontoken.go)
	hello.RecoveryReady = true
	if c.recoveryRequiredSessions != nil {
		hello.RecoveryRequiredSessions = c.recoveryRequiredSessions()
	}
	hello.Welcome = true // DaemonWelcome (linkWatch): "connected" only once the server says so
	hello.QueuedSessions = c.queued()
	hello.BackgroundTasks = c.backgroundTaskInventory()
	// sentModelsGen is the generation of the model lists this connection
	// has reported; only the heartbeat goroutine touches it after the hello.
	var sentModelsGen uint64
	var modelsChanged <-chan struct{}
	if c.engineModels != nil {
		hello.EngineModels, sentModelsGen = c.engineModels.Snapshot()
		modelsChanged = c.engineModels.Changed()
	}
	raw, err := json.Marshal(hello)
	if err != nil {
		_ = conn.Close()
		return linkResult{}, err
	}
	c.writeMu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, raw)
	if err == nil {
		c.mu.Lock()
		c.conn = conn
		c.discoveryCtx = linkCtx
		c.mu.Unlock()
	}
	c.writeMu.Unlock()
	if err != nil {
		_ = conn.Close()
		return linkResult{}, err
	}

	// Written is not accepted: the server answers a wrong token only by closing with 4401.
	// "connected" waits for its daemon_welcome, or for the link to stay open for the window.
	watch := &linkWatch{c: c}
	window := time.AfterFunc(c.config.unconfirmedAfter(), watch.windowPassed)
	defer window.Stop()

	// Fire the connect callback (e.g. reattach persistent sessions) without
	// blocking the read loop. The hello above already reported persistent
	// session IDs, so the server won't reap them before reattach completes.
	if c.onConnect != nil && c.config.RequiredWorkspace == nil {
		go c.onConnect()
	}
	// A heartbeat at once, not at the first tick: the server learns its sessions' real states
	// (an adopted session's "idle") within a second of the hello.
	c.RequestHeartbeat()

	// hbStop signals the heartbeat goroutine to exit.
	// hbDone is closed by the heartbeat goroutine when it has exited.
	hbStop := make(chan struct{})
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		ticker := time.NewTicker(c.config.heartbeatInterval())
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-hbStop:
				return
			case <-ticker.C:
			case <-modelsChanged:
				// A probe changed a list: report it now, not on the next tick.
			case <-c.hbKick:
			}
			if !c.workspaceDispatchAllowed() {
				continue
			}
			sessions := activeSessions()
			if sessions == nil {
				sessions = []string{}
			}
			var states map[string]string
			if c.sessionStates != nil {
				states = c.sessionStates()
			}
			root := c.reposRoot()
			repos, origins, remotes, limited := []string{}, map[string]protocol.RepoOrigin(nil), map[string]string(nil), false
			if c.config.DaemonMode != "runner" {
				repos, origins, remotes, limited = diskRepoInventory(ctx, root)
			}
			hb := protocol.DaemonHeartbeat{
				Type:               "daemon_heartbeat",
				ActiveSessions:     sessions,
				SessionStates:      states,
				QueuedSessions:     c.queued(),
				BackgroundTasks:    c.backgroundTaskInventory(),
				SandboxAvailable:   sandboxImageAvailable(),
				CheckedOutRepos:    repos,
				AvailableEngines:   availableEngines(),
				LocalEngines:       localEngines(),
				ClaudeCLIAvailable: claudeOnPath(),
				AnthropicKeySet:    os.Getenv("ANTHROPIC_API_KEY") != "",
				ReposRoot:          root,

				SandboxClaudeCredential: sandboxClaudeCredentialReport(),
			}
			hb.RepoOrigins, hb.RepoRemotes = origins, remotes
			hb.RepoInventoryLimited = limited
			var gen uint64
			if c.engineModels != nil {
				var snap map[string]models.Report
				snap, gen = c.engineModels.Snapshot()
				if gen != sentModelsGen {
					hb.EngineModels = snap
				}
			}
			if err := c.SendContext(ctx, hb); err != nil {
				return
			}
			sentModelsGen = gen
		}
	}()

	// Force-close connection when context is cancelled so readLoop unblocks.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	c.serverPlanNudges.Store(false)
	c.workspaceNegotiated.Store(false)
	c.serverWorkspaceVersion.Store(0)
	welcomed := false
	var recoverySessionID string
	readErr := c.readLoop(conn, func(raw []byte) {
		watch.welcome(raw)
		var msg protocol.DaemonWelcome
		if json.Unmarshal(raw, &msg) != nil || welcomed {
			return
		}
		welcomed = true
		c.serverPlanNudges.Store(msg.PlanNudges)
		negotiated := protocol.NegotiateWorkspaceCapabilities(capabilities, msg.WorkspaceCapabilities)
		if shellService != nil {
			shellService.SetCapabilities(negotiated, capabilities)
		}
		if negotiated.ManifestVersion == protocol.WorkspaceVersion {
			c.serverWorkspaceVersion.Store(protocol.WorkspaceVersion)
		} else {
			c.serverWorkspaceVersion.Store(0)
		}
		c.workspaceNegotiated.Store(true)
		if c.config.RequiredWorkspace != nil && negotiated.ManifestVersion != protocol.WorkspaceVersion {
			if c.config.OnUnsupportedWorkspace != nil {
				c.config.OnUnsupportedWorkspace()
			}
			_ = conn.Close()
			return
		}
		if c.config.RequiredWorkspace != nil {
			if c.onConnect != nil {
				go c.onConnect()
			}
			c.RequestHeartbeat()
		}
		recoverySessionID = msg.SessionID
		phase := RecoveryLegacy
		if msg.RecoveryHold {
			phase = RecoveryHeld
		}
		c.noteRecoveryLink(generation, phase)
	}, func(raw []byte) {
		var ready protocol.DaemonRecoveryReady
		if json.Unmarshal(raw, &ready) == nil && ready.SessionID == recoverySessionID {
			c.noteRecoveryLink(generation, RecoveryReady)
		}
	})
	c.noteRecoveryLink(generation, RecoveryDisconnected)
	link := watch.close()

	// Stop the heartbeat, then close the conn to unblock any in-progress write.
	close(hbStop)
	_ = conn.Close()
	<-hbDone

	c.mu.Lock()
	c.conn = nil
	workspaceDisconnect := c.workspaceDisconnect
	c.mu.Unlock()
	if workspaceDisconnect != nil {
		workspaceDisconnect()
	}

	if ctx.Err() != nil {
		// The read loop ended because the daemon is shutting down; that is not a failure.
		readErr = nil
	}
	return link, readErr
}

// readLoop reads messages from conn until it returns an error, dispatching each
// to the registered handler (if any); daemon_welcome goes to onWelcome.
func (c *WSClient) readLoop(conn *websocket.Conn, onWelcome func([]byte), onRecoveryReady ...func([]byte)) error {
	type envelope struct {
		Type string `json:"type"`
	}
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			log.Printf("daemon: malformed message: %v", err)
			continue
		}
		if env.Type != "daemon_welcome" && !c.workspaceDispatchAllowed() {
			continue
		}
		if env.Type == "daemon_recovery_ready" {
			if len(onRecoveryReady) > 0 {
				onRecoveryReady[0](raw)
			}
			continue
		}
		if env.Type == "daemon_welcome" {
			onWelcome(raw)
			continue
		}
		c.mu.Lock()
		handler, ok := c.handlers[env.Type]
		c.mu.Unlock()
		if ok {
			handler(raw)
		} else {
			log.Printf("daemon: unhandled message type: %s", env.Type)
		}
	}
}

// listReposOnDisk returns the names of non-hidden directories present in
// reposRoot — dotdirs (.blerg-runner's own state dir, .git-adjacent tooling
// dirs someone might keep there, etc.) are never real checked-out repos, so
// they're excluded rather than showing up as bogus entries in the launch UI.
// If reposRoot is empty or unreadable, returns an empty slice.
func listReposOnDisk(root string) []string {
	names, _, _ := discovery.New(func() string { return root }).Inventory(context.Background())
	return names
}
func diskRepoInventory(ctx context.Context, root string) ([]string, map[string]protocol.RepoOrigin, map[string]string, bool) {
	names, origins, limited := discovery.New(func() string { return root }).Inventory(ctx)
	legacy := map[string]string{}
	for name, origin := range origins {
		if origin.Provider == "github" {
			legacy[name] = origin.FullName
		}
	}
	return names, origins, legacy, limited
}

func (c *WSClient) CanPlanNudges() bool { return c.serverPlanNudges.Load() }

// WaitWorkspace refuses absent/unknown peers before runtime preparation. It
// never treats the legacy unconfirmed-connection timeout as negotiation.
func (c *WSClient) WaitWorkspace(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if c.workspaceNegotiated.Load() {
			if c.serverWorkspaceVersion.Load() != protocol.WorkspaceVersion {
				return errors.New("server does not support workspace v2")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *WSClient) workspaceDispatchAllowed() bool {
	return c.config.RequiredWorkspace == nil || (c.workspaceNegotiated.Load() && c.serverWorkspaceVersion.Load() == protocol.WorkspaceVersion)
}
