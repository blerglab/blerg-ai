// Package daemon implements the blerg-runner daemon: a WebSocket client that
// connects to the blerg-runner server and manages local Claude Code sessions.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

const (
	defaultProtocolVersion   = "1"
	defaultHeartbeatInterval = 30 * time.Second
	defaultReconnectInitial  = 2 * time.Second
	defaultReconnectMax      = 60 * time.Second
)

// Config holds the daemon client configuration.
type Config struct {
	ServerURL       string
	DaemonToken     string
	DaemonName      string
	DaemonMode      string
	ReposRoot       string
	ProtocolVersion string
	Version         string
	// AllowHostCredentialClone is reported in the hello — see
	// ManagerConfig.AllowHostCredentialClone.
	AllowHostCredentialClone bool

	// Tunable timings. Zero values use the package defaults.
	HeartbeatInterval time.Duration
	ReconnectInitial  time.Duration
	ReconnectMax      time.Duration
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

// WSClient is a WebSocket client that maintains a persistent connection to the
// blerg-runner server, reconnecting automatically with exponential backoff.
type WSClient struct {
	config        Config
	mu            sync.Mutex // guards conn and handlers
	writeMu       sync.Mutex // guards WriteMessage calls (at most one concurrent writer)
	conn          *websocket.Conn
	handlers      map[string]func([]byte)
	onConnect     func()
	sessionStates func() map[string]string
	engineModels  EngineModelSource
	reposRootFn   func() string
	hbKick        chan struct{} // RequestHeartbeat: send one now

	// sleep is the injectable backoff wait, so tests can observe/short-circuit
	// it without real time passing. Returns false if ctx was done.
	sleep func(ctx context.Context, d time.Duration) bool
}

// SetOnConnect registers a callback fired (in its own goroutine) after each
// successful connection and hello. Used to reattach persistent sessions.
func (c *WSClient) SetOnConnect(fn func()) {
	c.onConnect = fn
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
	return &WSClient{
		config:   cfg,
		handlers: make(map[string]func([]byte)),
		hbKick:   make(chan struct{}, 1),
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
func (c *WSClient) Send(msg any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("daemon: not connected to server")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, raw)
}

// Run connects to the server, sends daemon_hello, and runs the read loop. On
// disconnect it reconnects with exponential backoff. Run blocks until ctx is
// cancelled.
//
// activeSessions is called each time a connection is (re-)established and
// its return value populates the active_sessions field of daemon_hello.
func (c *WSClient) Run(ctx context.Context, activeSessions func() []string) {
	backoff := c.config.reconnectInitial()
	var lastErr string
	var lastLog time.Time
	for {
		connected, err := c.connect(ctx, activeSessions)
		if ctx.Err() != nil || err == nil {
			return
		}
		if connected {
			// The link was up at least once: start the ladder over.
			backoff = c.config.reconnectInitial()
		}
		// Log the first failure, then identical failures once a minute — a
		// server that's down for an hour must not produce 1800 lines.
		if err.Error() != lastErr || time.Since(lastLog) > time.Minute {
			log.Printf("daemon: disconnected (%v); reconnecting in %s", err, backoff)
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

// connect dials the server, sends hello, starts the heartbeat goroutine, then
// reads until the connection closes or ctx is cancelled. It returns whether
// the dial + hello succeeded (connected) and the error from the read loop
// (nil error if shutdown was due to ctx cancellation).
func (c *WSClient) connect(ctx context.Context, activeSessions func() []string) (connected bool, err error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.config.ServerURL, nil) //nolint:bodyclose // gorilla/websocket: after a successful dial the response body is the connection and is closed with conn
	if err != nil {
		return false, err
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	// Send daemon_hello.
	sessions := activeSessions()
	if sessions == nil {
		sessions = []string{}
	}
	root := c.reposRoot()
	checkedOut := listReposOnDisk(root)
	hello := protocol.DaemonHello{
		Type:               "daemon_hello",
		Name:               c.config.DaemonName,
		Mode:               c.config.DaemonMode,
		ReposRoot:          root,
		Token:              c.config.DaemonToken,
		ProtocolVersion:    c.config.ProtocolVersion,
		Version:            c.config.Version,
		ActiveSessions:     sessions,
		CheckedOutRepos:    checkedOut,
		SandboxAvailable:   sandboxImageAvailable(),
		AvailableEngines:   availableEngines(),
		ClaudeCLIAvailable: claudeOnPath(),
		AnthropicKeySet:    os.Getenv("ANTHROPIC_API_KEY") != "",

		SandboxClaudeCredential: sandboxClaudeCredentialReport(),
	}
	hello.RepoOrigins, hello.RepoRemotes = repoReport(root, checkedOut)
	hello.GitProviders = gitprovider.Default.CredentialKinds()
	hello.CloneFrom = true // SpawnSession.CloneFrom (EnsureClonedTarget)
	hello.AllowHostCredentialClone = c.config.AllowHostCredentialClone
	hello.MCPGateway = true    // SpawnSession.MCPGateway (mcpgateway.go)
	hello.RestrictTools = true // SpawnSession.RestrictTools (claudecode.go ccHardeningFlags)
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
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		return false, err
	}
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		_ = conn.Close()
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		return false, err
	}

	log.Printf("daemon: connected to %s as %q", c.config.ServerURL, c.config.DaemonName)

	// Fire the connect callback (e.g. reattach persistent sessions) without
	// blocking the read loop. The hello above already reported persistent
	// session IDs, so the server won't reap them before reattach completes.
	if c.onConnect != nil {
		go c.onConnect()
	}

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
			sessions := activeSessions()
			if sessions == nil {
				sessions = []string{}
			}
			var states map[string]string
			if c.sessionStates != nil {
				states = c.sessionStates()
			}
			root := c.reposRoot()
			repos := listReposOnDisk(root)
			hb := protocol.DaemonHeartbeat{
				Type:               "daemon_heartbeat",
				ActiveSessions:     sessions,
				SessionStates:      states,
				SandboxAvailable:   sandboxImageAvailable(),
				CheckedOutRepos:    repos,
				AvailableEngines:   availableEngines(),
				ClaudeCLIAvailable: claudeOnPath(),
				AnthropicKeySet:    os.Getenv("ANTHROPIC_API_KEY") != "",
				ReposRoot:          root,

				SandboxClaudeCredential: sandboxClaudeCredentialReport(),
			}
			hb.RepoOrigins, hb.RepoRemotes = repoReport(root, repos)
			var gen uint64
			if c.engineModels != nil {
				var snap map[string]models.Report
				snap, gen = c.engineModels.Snapshot()
				if gen != sentModelsGen {
					hb.EngineModels = snap
				}
			}
			if err := c.Send(hb); err != nil {
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

	readErr := c.readLoop(conn)

	// Stop the heartbeat, then close the conn to unblock any in-progress write.
	close(hbStop)
	_ = conn.Close()
	<-hbDone

	c.mu.Lock()
	c.conn = nil
	c.mu.Unlock()

	if ctx.Err() != nil {
		return true, nil //nolint:nilerr // the read loop ended because the daemon is shutting down; that is not a failure
	}
	return true, readErr
}

// readLoop reads messages from conn until it returns an error, dispatching each
// to the registered handler (if any).
func (c *WSClient) readLoop(conn *websocket.Conn) error {
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
func listReposOnDisk(reposRoot string) []string {
	if reposRoot == "" {
		return []string{}
	}
	entries, err := os.ReadDir(reposRoot)
	if err != nil {
		return []string{}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names
}
