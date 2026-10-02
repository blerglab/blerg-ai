package daemon

// MCP gateway delivery: turning a session's grant (protocol.MCPGatewayConfig)
// into the MCP config file Claude Code reads with --mcp-config.
//
//   - The file holds the bearer tokens, so it is 0600 in a private 0700
//     directory OUTSIDE the session's working directory (the agent's file
//     tools can read the workdir, and a token there would be one Read away
//     from a card or a proposal). The tokens are never in argv, the
//     environment or a log.
//   - In a Docker sandbox the engine runs inside the container, where a host
//     temp file does not exist: the file is written there through `docker
//     exec` with the content on stdin, into a 0700 directory under /tmp.
//   - The file is written again whenever it is missing (a restarted daemon, a
//     temp cleaner, a recreated container): the driver asks for the path
//     before every turn.
//   - It is removed when the session ends.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
// which is itself the sandbox) refuses a spawn carrying a grant, or "".
func mcpGatewaySpawnProblem(msg protocol.SpawnSession) string {
	switch {
	case msg.MCPGateway == nil && !msg.RestrictTools:
		return ""
	case msg.Kind != "agent":
		return "an MCP gateway grant or tool restriction needs an agent-kind session"
	case !msg.Sandbox:
		return "an MCP gateway grant or tool restriction runs only in a sandbox, never directly on this machine"
	}
	return ""
}

// sandboxMCPDirBase is the directory, inside the container, that the private
// config directory is made under. A var so a test with a fake docker (which
// runs the "in-container" command on the host) can aim it at a scratch dir.
var sandboxMCPDirBase = "/tmp"

// mcpConfigFile is one session's MCP config file: on the host, or inside the
// session's sandbox container when prefix is enabled.
type mcpConfigFile struct {
	cfg    *protocol.MCPGatewayConfig
	prefix sandboxExec

	mu     sync.Mutex
	dir    string // private directory (host path, or path inside the container)
	path   string
	closed bool // Remove was called: the file is never written again
}

func newMCPConfigFile(cfg *protocol.MCPGatewayConfig, prefix sandboxExec) *mcpConfigFile {
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
	st, err := os.Stat(f.path)
	return err == nil && st.Mode().IsRegular()
}

func (f *mcpConfigFile) removeLocked() {
	if f.dir == "" {
		return
	}
	if f.prefix.enabled() {
		_ = dockerRun("exec", f.prefix.container(), "rm", "-rf", f.dir)
	} else {
		_ = os.RemoveAll(f.dir)
	}
	f.dir, f.path = "", ""
}

func (f *mcpConfigFile) writeOnHost(body []byte) error {
	dir, err := os.MkdirTemp("", "blerg-mcp-") // 0700, unique, under $TMPDIR — never under a workdir
	if err != nil {
		return fmt.Errorf("could not create the MCP config directory: %w", err)
	}
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		_ = os.RemoveAll(dir)
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
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	dir := sandboxMCPDirBase + "/blerg-mcp-" + hex.EncodeToString(rnd[:])
	// mkdir -m 700 (no -p) fails if the name exists, so nobody else's directory
	// is adopted; the umask makes the file 0600. The directory is an argument
	// ($1), not interpolated into the script.
	const script = `umask 077 && mkdir -m 700 "$1" && cat > "$1/mcp.json"`
	if err := dockerRunStdin(body, "exec", "-i", f.prefix.container(), "sh", "-c", script, "sh", dir); err != nil {
		return fmt.Errorf("could not write the MCP config inside the sandbox: %w", err)
	}
	f.dir, f.path = dir, dir+"/mcp.json"
	return nil
}
