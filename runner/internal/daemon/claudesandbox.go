package daemon

import (
	"encoding/json"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Claude Code's own sandbox. When a person runs Claude Code with its sandbox on (`sandbox.enabled`
// in their settings), a session's Bash commands run without a direct route to this machine's
// localhost and go out only through the sandbox's proxy, to the hosts its settings allow. The
// messaging CLI then cannot reach the runner, and Claude asks every time to run `blerg-runner`
// outside the sandbox.
//
// So a Claude session Blerg starts on such a machine gets the runner's host and port (and the
// board's and the board gateway's, when the session has them) on `sandbox.network.allowedDomains`
// in ITS OWN settings (`--settings`, the session-only tier), never in the person's settings files.
// The CLI sends a request for exactly those addresses through the sandbox's proxy (blerg-runner,
// _claude_sandbox_proxy), which lets them through. The CLI reads its token file under the daemon's
// state directory, which Claude's sandbox leaves readable.
//
// Nothing else is changed: no command is excluded from the sandbox. When the person's managed
// settings allow only managed domains (`sandbox.network.allowManagedDomainsOnly`), a domain from the
// session would be ignored, and the policy is not overridden: the session is told how the
// administrator can allow the runner.
//
// Keys checked against Claude Code 2.1.305's settings schema.

// claudeManagedSettingsDir is where Claude Code reads managed settings (a var for tests).
var claudeManagedSettingsDir = func() string {
	if runtime.GOOS == "darwin" {
		return "/Library/Application Support/ClaudeCode"
	}
	return "/etc/claude-code"
}

// claudeSandboxPosture is what the person's settings say about Claude Code's sandbox.
type claudeSandboxPosture struct {
	enabled            bool // some settings source turns it on
	managedDomainsOnly bool // managed settings allow only their own domains
}

type claudeSandboxSettingsFile struct {
	Sandbox *struct {
		Enabled *bool `json:"enabled"`
		Network *struct {
			AllowManagedDomainsOnly *bool `json:"allowManagedDomainsOnly"`
		} `json:"network"`
	} `json:"sandbox"`
}

// readClaudeSandboxPosture reads every settings source Claude Code would read for a session in
// workDir: managed (managed-settings.json and managed-settings.d), the user's
// ($CLAUDE_CONFIG_DIR or ~/.claude), and the project's. Sources are only read, never written. A
// file that is missing or does not parse says nothing.
func readClaudeSandboxPosture(home, workDir string) claudeSandboxPosture {
	var p claudeSandboxPosture
	read := func(path string, managed bool) {
		raw, err := readBounded(path, 4<<20)
		if err != nil {
			return
		}
		var f claudeSandboxSettingsFile
		if json.Unmarshal(raw, &f) != nil || f.Sandbox == nil {
			return
		}
		if f.Sandbox.Enabled != nil && *f.Sandbox.Enabled {
			p.enabled = true
		}
		if managed && f.Sandbox.Network != nil && f.Sandbox.Network.AllowManagedDomainsOnly != nil && *f.Sandbox.Network.AllowManagedDomainsOnly {
			p.managedDomainsOnly = true
		}
	}
	if dir := claudeManagedSettingsDir(); dir != "" {
		read(filepath.Join(dir, "managed-settings.json"), true)
		if entries, err := os.ReadDir(filepath.Join(dir, "managed-settings.d")); err == nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".json") {
					read(filepath.Join(dir, "managed-settings.d", e.Name()), true)
				}
			}
		}
	}
	userDir := ""
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" && filepath.IsAbs(v) {
		userDir = v
	} else if home != "" {
		userDir = filepath.Join(home, ".claude")
	}
	if userDir != "" {
		read(filepath.Join(userDir, "settings.json"), false)
	}
	if workDir != "" {
		read(filepath.Join(workDir, ".claude", "settings.json"), false)
		read(filepath.Join(workDir, ".claude", "settings.local.json"), false)
	}
	return p
}

// blergRunnerHosts are the sandbox allowlist entries for the services the messaging CLI talks to,
// from the session's environment, always as host:port: a URL without a port gets its scheme's
// default (a bare host would open every port of it, on loopback every local service).
func blergRunnerHosts(env []string) []string {
	get := func(key string) string {
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, key+"="); ok {
				return v
			}
		}
		return ""
	}
	seen := map[string]bool{}
	var hosts []string
	add := func(raw string) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil {
			return
		}
		h := u.Hostname()
		if h == "" || strings.ContainsAny(h, " *") {
			return
		}
		port := u.Port()
		if port == "" {
			switch u.Scheme {
			case "http", "ws":
				port = "80"
			case "https", "wss":
				port = "443"
			default:
				return
			}
		}
		entry := net.JoinHostPort(h, port)
		if !seen[entry] {
			seen[entry] = true
			hosts = append(hosts, entry)
		}
	}
	// The CLI prefers SERVER_HTTP and otherwise derives the address from the daemon's server URL.
	if v := get("BLERG_RUNNER_SERVER_HTTP"); v != "" {
		add(v)
	} else {
		add(get("BLERG_RUNNER_SERVER_URL"))
	}
	add(get("BLERG_BOARD_URL"))
	add(get("BLERG_RUNNER_BOARD_GATEWAY_URL"))
	sort.Strings(hosts)
	return hosts
}

// claudeSandboxException is the "sandbox" settings a session needs for the messaging CLI to work
// under Claude Code's sandbox (nil when the sandbox is off or nothing can be allowed), and a note for
// the person when their managed policy keeps the session from allowing the runner.
func claudeSandboxException(env []string, home, workDir string) (map[string]any, string) {
	p := readClaudeSandboxPosture(home, workDir)
	if !p.enabled {
		return nil, ""
	}
	hosts := blergRunnerHosts(env)
	if len(hosts) == 0 {
		return nil, ""
	}
	if p.managedDomainsOnly {
		return nil, "Claude Code's sandbox is on and your organization's managed settings allow only managed domains, so this session cannot reach Blerg from inside the sandbox (blerg-runner will fail there). " +
			"Ask your administrator to add " + strings.Join(hosts, ", ") + " to sandbox.network.allowedDomains in the managed settings."
	}
	return map[string]any{"network": map[string]any{"allowedDomains": hosts}}, ""
}

// ccSessionSettings is the JSON of an agent session's own `--settings`: the progress hook, and
// the sandbox exception when there is one.
func ccSessionSettings(sandbox map[string]any) string {
	if sandbox == nil {
		return progressHookSettings
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(progressHookSettings), &settings); err != nil {
		return progressHookSettings
	}
	settings["sandbox"] = sandbox
	raw, err := json.Marshal(settings)
	if err != nil {
		return progressHookSettings
	}
	return string(raw)
}

// withClaudeSandboxSettings gives every turn of a driver the sandbox exception (nil = none).
func withClaudeSandboxSettings(sandbox map[string]any) ccOption {
	return func(o *ccOptions) { o.SandboxSettings = sandbox }
}

// claudeTerminalSandboxArgs is the `--settings` argument of a terminal Claude session that needs
// the exception (nothing otherwise). A sandboxed (Docker) session is left alone: its addresses are
// the sandbox network's, and Claude Code's own sandbox does not run in the image. A note the person
// should see is logged (a terminal has no other channel from here).
func claudeTerminalSandboxArgs(sessionID string, env []string, home, workDir string, dockerSandboxed bool) []string {
	if dockerSandboxed {
		return nil
	}
	sandbox, note := claudeSandboxException(env, home, workDir)
	if note != "" {
		log.Printf("session %s: %s", sessionID, note)
	}
	if sandbox == nil {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"sandbox": sandbox})
	if err != nil {
		return nil
	}
	return []string{"--settings", string(raw)}
}
