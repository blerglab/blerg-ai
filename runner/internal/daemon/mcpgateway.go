package daemon

// MCP gateway delivery: turning a session's grant (protocol.MCPGatewayConfig)
// into the MCP config file Claude Code reads with --mcp-config.
//
//   - The file holds the bearer tokens, so it is 0600 in a private 0700
//     directory OUTSIDE the session's working directory (the agent's file
//     tools can read the workdir, and a token there would be one Read away
//     from a card or a proposal). The tokens are never in argv, the
//     environment or a log, except that the selected-board CLI receives only
//     its board gateway grant in its environment (never an upstream token).
//   - In a Docker sandbox the engine runs inside the container, where a host
//     temp file does not exist: the file is written there through `docker
//     exec` with the content on stdin, into a 0700 directory under /tmp.
//   - The file is written again whenever it is missing (a restarted daemon, a
//     temp cleaner, a recreated container): the driver asks for the path
//     before every turn.
//   - It is removed when the session ends.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// MCPGatewayEnvVar is the ONE environment variable a cluster pod receives its
// gateway grant through (sourced from the per-session Kubernetes Secret with a
// secretKeyRef, never written into the Job spec). Its value is the JSON of a
// protocol.MCPGatewayConfig. The pod entrypoint reads it and unsets it before
// anything is spawned (runner.ConfigFromEnv); the session environment builder
// drops it too (sanitizedEnviron), and it starts with BLERG_RUNNER_ so
// validExtraEnvKey refuses it as ExtraEnv: it can never reach a child process.
const MCPGatewayEnvVar = "BLERG_RUNNER_MCP_CONFIG"

// RestrictToolsEnvVar is the plain environment variable ("1") a cluster pod
// receives when its session must run restricted (every cron session, and every
// session with a grant): the built-in tool allow-list, no ambient MCP server,
// no user settings, plugins, hooks or skills. Not a secret. The pod entrypoint
// reads it and unsets it (runner.ConfigFromEnv) and the session environment
// builder drops it, so no child process sees it.
const RestrictToolsEnvVar = "BLERG_RUNNER_RESTRICT_TOOLS"

// InteractionEnvVar is the plain environment variable a cluster pod receives with its
// session's interaction mode ("interactive" or "unattended", protocol.SpawnSession.Interaction).
// Not a secret. The pod entrypoint reads it and unsets it (runner.ConfigFromEnv) and the
// session environment builder drops it, so no child process sees it.
const InteractionEnvVar = "BLERG_RUNNER_INTERACTION"

// maxMCPGatewayServers caps the connections one grant may name (an account has
// at most 20).
const maxMCPGatewayServers = 20

// mcpGatewayName is the shape of a gateway server name (a connection slug):
// safe in a URL path segment and as a JSON key.
var mcpGatewayName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidateMCPGateway checks a grant received from the server or the pod
// environment. The error never contains a token.
func ValidateMCPGateway(cfg *protocol.MCPGatewayConfig) error {
	if cfg == nil {
		return errors.New("no MCP gateway configuration")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("the MCP gateway base URL must be a plain http(s) URL")
	}
	if len(cfg.Servers) == 0 || len(cfg.Servers) > maxMCPGatewayServers {
		return fmt.Errorf("an MCP gateway grant names 1 to %d connections", maxMCPGatewayServers)
	}
	seen := map[string]bool{}
	for _, s := range cfg.Servers {
		if !mcpGatewayName.MatchString(s.Name) {
			return errors.New("an MCP gateway connection name is not valid")
		}
		if seen[s.Name] {
			return errors.New("an MCP gateway connection is named twice")
		}
		seen[s.Name] = true
		if s.Token == "" || len(s.Token) > 512 || strings.ContainsFunc(s.Token, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			return fmt.Errorf("the MCP gateway token for %q is not valid", s.Name)
		}
	}
	return nil
}

// mcpConfigJSON renders the config Claude Code reads:
// {"mcpServers":{"<name>":{"type":"http","url":"<base>/mcp-gw/<name>",
// "headers":{"Authorization":"Bearer <token>"}}}}. Built with encoding/json,
// so nothing in a name or token can break out of a string.
func mcpConfigJSON(cfg *protocol.MCPGatewayConfig) ([]byte, error) {
	if err := ValidateMCPGateway(cfg); err != nil {
		return nil, err
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	servers := make(map[string]any, len(cfg.Servers))
	for _, s := range cfg.Servers {
		servers[s.Name] = map[string]any{
			"type":    "http",
			"url":     base + "/mcp-gw/" + s.Name,
			"headers": map[string]string{"Authorization": "Bearer " + s.Token},
		}
	}
	return json.Marshal(map[string]any{"mcpServers": servers})
}

// mcpGatewaySpawnProblem is the reason a daemon (as opposed to a cluster pod,
// which is itself the sandbox) refuses a spawn carrying a grant, or "". The
// one grant a session directly on this machine may hold is the person's board
// alone, for an unrestricted (launch-sheet) session, dialled through the
// server (hostBoardGrant).
func mcpGatewaySpawnProblem(msg protocol.SpawnSession) string {
	switch {
	case msg.MCPGateway == nil && !msg.RestrictTools:
		return ""
	case msg.Kind != "agent":
		return "an MCP gateway grant or tool restriction needs an agent-kind session"
	case !msg.Sandbox && !hostBoardGrant(msg):
		return "an MCP gateway grant or tool restriction runs only in a sandbox, never directly on this machine"
	}
	return ""
}

// hostBoardGrant reports whether a spawn's grant is one a session on this
// machine's host may hold: only the built-in board connection, dialled through
// the server, for a session that is not restricted.
func hostBoardGrant(msg protocol.SpawnSession) bool {
	g := msg.MCPGateway
	return g != nil && !msg.RestrictTools && g.ViaServer &&
		len(g.Servers) == 1 && g.Servers[0].Name == "board"
}

// gatewayServerBase is the server address a daemon dials the gateway at for a
// grant that asks for it (MCPGatewayConfig.ViaServer): its server HTTP base
// (BLERG_RUNNER_SERVER_HTTP) when set, else derived from the WebSocket URL it
// connects to (ws://host/ws/daemon → http://host), the blerg-runner CLI's own
// fallback. "" when neither gives one.
func gatewayServerBase(serverHTTP, serverURL string) string {
	if v := strings.TrimRight(strings.TrimSpace(serverHTTP), "/"); v != "" {
		return v
	}
	ws := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	ws = strings.TrimRight(strings.TrimSuffix(ws, "/ws/daemon"), "/")
	switch {
	case strings.HasPrefix(ws, "ws://"):
		return "http://" + strings.TrimPrefix(ws, "ws://")
	case strings.HasPrefix(ws, "wss://"):
		return "https://" + strings.TrimPrefix(ws, "wss://")
	}
	return ""
}

// viaServerGateway is the grant as this daemon dials it: cfg itself, unless the
// server asked for the gateway to be reached through its own web address
// (ViaServer); then a copy whose BaseURL is base, the address this daemon
// already reaches the server at. The server's BaseURL is often an in-cluster
// address no workstation can reach. cfg is never modified.
func viaServerGateway(cfg *protocol.MCPGatewayConfig, base string) (*protocol.MCPGatewayConfig, error) {
	if cfg == nil || !cfg.ViaServer {
		return cfg, nil
	}
	if base == "" {
		return nil, errors.New("this daemon has no server address to reach the MCP gateway through: set BLERG_RUNNER_SERVER_HTTP")
	}
	out := *cfg
	out.BaseURL = base
	out.Servers = append([]protocol.MCPGatewayServer(nil), cfg.Servers...)
	return &out, nil
}

// sandboxGatewayView is the grant as dialled from inside a sandbox container:
// a loopback BaseURL names the host, which is the container itself in there,
// so it becomes the runner's service URL on the sandbox network (the same
// rewrite sandboxNetworkEnv makes for the messaging CLI). Any other BaseURL is
// kept. cfg is never modified.
func sandboxGatewayView(cfg *protocol.MCPGatewayConfig) *protocol.MCPGatewayConfig {
	if cfg == nil {
		return nil
	}
	u, loop := loopbackURL(cfg.BaseURL)
	if !loop {
		return cfg
	}
	out := *cfg
	out.BaseURL = sandboxRunnerURL + strings.TrimRight(u.EscapedPath(), "/")
	return &out
}

// sandboxMCPDirBase is the directory, inside the container, that the private
// config directory is made under. A var so a test with a fake docker (which
// runs the "in-container" command on the host) can aim it at a scratch dir.
var sandboxMCPDirBase = "/tmp"

// mcpConfigFile is one session's MCP config file: on the host, or inside the
// session's sandbox container when prefix is enabled.
type mcpConfigFile struct {
	cfg           *protocol.MCPGatewayConfig
	sessionFiling bool
	prefix        sandboxExec

	mu     sync.Mutex
	dir    string // private directory (host path, or path inside the container)
	path   string
	closed bool // Remove was called: the file is never written again
}

func newMCPConfigFile(cfg *protocol.MCPGatewayConfig, prefix sandboxExec) *mcpConfigFile {
	if prefix.enabled() {
		// The engine reads this file inside the container and dials from there.
		cfg = sandboxGatewayView(cfg)
	}
	return &mcpConfigFile{cfg: cfg, prefix: prefix}
}

// Ensure returns the config file's path, writing the file first when it does
// not exist (or no longer does).
func (f *mcpConfigFile) Ensure() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		// A turn racing the session's kill must not recreate the token file.
		return "", errors.New("the session has ended")
	}
	if f.path != "" && f.exists() {
		return f.path, nil
	}
	body, err := mcpConfigJSON(f.cfg)
	if f.sessionFiling {
		body, err = sessionFilingMCPConfigJSON(f.cfg)
	}
	if err != nil {
		return "", err
	}
	f.removeLocked() // a half-present directory is not reused
	if f.prefix.enabled() {
		err = f.writeInContainer(body)
	} else {
		err = f.writeOnHost(body)
	}
	if err != nil {
		f.dir, f.path = "", ""
		return "", err
	}
	return f.path, nil
}

// Remove deletes the file and its directory; safe to call twice.
func (f *mcpConfigFile) Remove() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.removeLocked()
}

func (f *mcpConfigFile) exists() bool {
	if f.prefix.enabled() {
		return dockerRun("exec", f.prefix.container(), "test", "-f", f.path) == nil
	}
	st, err := os.Stat(f.path) //nolint:gosec // G703: f.path is the fixed mcp.json inside this object’s private MkdirTemp directory.
	return err == nil && st.Mode().IsRegular()
}

func (f *mcpConfigFile) removeLocked() {
	if f.dir == "" {
		return
	}
	if f.prefix.enabled() {
		_ = dockerRun("exec", f.prefix.container(), "rm", "-rf", f.dir)
	} else {
		_ = os.RemoveAll(f.dir) //nolint:gosec // G703: f.dir is created only by this object’s MkdirTemp, never supplied by a tool or request.
	}
	f.dir, f.path = "", ""
}

func (f *mcpConfigFile) writeOnHost(body []byte) error {
	dir, path, err := writePrivateOnHost("blerg-mcp-", "mcp.json", body)
	if err != nil {
		return fmt.Errorf("could not write the MCP config: %w", err)
	}
	f.dir, f.path = dir, path
	return nil
}

// dockerRunStdin is dockerRun with stdin — for content that must not be in an
// argv. A var so tests can stand in for docker.
var dockerRunStdin = func(stdin []byte, args ...string) error {
	cmd := exec.Command("docker", args...) //nolint:gosec // literal docker binary, args assembled by the daemon (no shell); a short local call like dockerRun
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.Run()
}

func (f *mcpConfigFile) writeInContainer(body []byte) error {
	dir, path, err := writePrivateInContainer(f.prefix.container(), "blerg-mcp-", "mcp.json", body)
	if err != nil {
		return fmt.Errorf("could not write the MCP config inside the sandbox: %w", err)
	}
	f.dir, f.path = dir, path
	return nil
}
