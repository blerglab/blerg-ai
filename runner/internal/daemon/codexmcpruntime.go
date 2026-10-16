package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type codexRestrictedRuntime struct {
	Home                string
	Restricted          bool
	nativeSchemaChanged bool
	prefix              sandboxExec
	files               *codexRestrictedFiles
	bridge              *codexMCPBridge
	specs               []map[string]any
}

func prepareCodexRestrictedRuntime(ctx context.Context, workDir, authHome string, prefix sandboxExec, gateway *protocol.MCPGatewayConfig, externalAuth bool) (*codexRestrictedRuntime, error) {
	if externalAuth && prefix.enabled() {
		return nil, errors.New("managed authentication is not supported in a workstation sandbox")
	}
	var version []byte
	var err error
	if prefix.enabled() {
		version, err = exec.CommandContext(ctx, "docker", "exec", prefix.container(), "codex", "--version").Output() //nolint:gosec // Fixed docker executable and argument vector; container comes from the daemon sandbox handle, never a shell.
	} else {
		version, err = exec.CommandContext(ctx, resolveEngineBinary("codex", false), "--version").Output() //nolint:gosec // Daemon-resolved installed binary, fixed version argument, caller context, and no shell.
	}
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli "+codexRestrictedVersion {
		return nil, errors.New("restricted Codex requires the verified CLI " + codexRestrictedVersion)
	}
	files, err := newCodexRestrictedFiles(workDir)
	if err != nil {
		return nil, err
	}
	r := &codexRestrictedRuntime{files: files, prefix: prefix, Restricted: true}
	success := false
	defer func() {
		if !success {
			r.Close()
		}
	}()
	if gateway != nil {
		r.bridge, err = newCodexMCPBridge(ctx, gateway)
		if err != nil {
			return nil, err
		}
	}
	runtimeWork := workDir
	if prefix.enabled() {
		runtimeWork = sandboxWorkdir
		authHome = "/home/agent/.codex"
	}
	config := codexRestrictedConfig("", "", nil)
	quoted, _ := json.Marshal(runtimeWork)
	config["projects."+string(quoted)+".trust_level"] = "untrusted"
	body, err := codexRestrictedConfigBytes(config)
	if err != nil {
		return nil, err
	}
	if prefix.enabled() {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, errors.New("restricted runtime setup failed")
		}
		home := "/tmp/blerg-codex-restricted-" + hex.EncodeToString(random[:])
		const script = `umask 077 && mkdir -m 700 "$1" && test -f "$2/auth.json" && ln -s "$2/auth.json" "$1/auth.json" && cat > "$1/config.toml"`
		if err := dockerRunStdin(body, "exec", "-i", prefix.container(), "sh", "-c", script, "sh", home, authHome); err != nil {
			return nil, errors.New("restricted Codex login or configuration is unavailable inside the sandbox")
		}
		r.Home = home
	} else {
		auth := filepath.Join(authHome, "auth.json")
		if info, err := os.Stat(auth); !externalAuth && (err != nil || !info.Mode().IsRegular()) {
			return nil, errors.New("restricted Codex needs a file-backed Codex login")
		}
		home, err := os.MkdirTemp("", "blerg-codex-restricted-")
		if err != nil {
			return nil, errors.New("restricted Codex private home could not be created")
		}
		r.Home = home
		if !externalAuth {
			if err := os.Symlink(auth, filepath.Join(home, "auth.json")); err != nil {
				return nil, errors.New("restricted Codex login could not be attached")
			}
		}
		if err := os.WriteFile(filepath.Join(home, "config.toml"), body, 0600); err != nil {
			return nil, errors.New("restricted Codex policy could not be written")
		}
	}
	r.specs = codexRestrictedFileSpecs()
	if r.bridge != nil {
		r.specs = append(r.specs, r.bridge.Specs()...)
	}
	success = true
	return r, nil
}
func (r *codexRestrictedRuntime) Close() {
	if r == nil {
		return
	}
	if r.files != nil {
		_ = r.files.Close()
	}
	if r.bridge != nil {
		r.bridge.Close()
	}
	if r.Home != "" {
		if r.prefix.enabled() && r.Restricted {
			_ = dockerRunStdin(nil, "exec", r.prefix.container(), "rm", "-rf", "--", r.Home)
		} else {
			_ = os.RemoveAll(r.Home)
		}
	}
}
func (r *codexRestrictedRuntime) Specs() []map[string]any {
	return append([]map[string]any(nil), r.specs...)
}
func (r *codexRestrictedRuntime) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if strings.HasPrefix(name, "blerg_mcp__") {
		if r.bridge == nil {
			return "", errors.New("no MCP grant is attached")
		}
		return r.bridge.Call(ctx, name, args)
	}
	if r.files == nil {
		return "", errors.New("file tool is not supplied by this runtime")
	}
	return r.files.ExecuteContext(ctx, name, args)
}
func codexRestrictedFileSpecs() []map[string]any {
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer", "minimum": 0}
	specs := []map[string]any{}
	add := func(name, description string, props map[string]any, required []string) {
		specs = append(specs, map[string]any{"type": "function", "name": name, "description": description, "inputSchema": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}})
	}
	add("read_file", "Read a regular workspace file. Paths are relative; symlinks and control state are denied.", map[string]any{"path": str, "offset": num, "limit": num}, []string{"path"})
	add("write_file", "Write a regular workspace file; creates parent directories. Relative paths only.", map[string]any{"path": str, "content": str}, []string{"path", "content"})
	add("edit_file", "Replace exactly one matching string in a regular workspace file.", map[string]any{"path": str, "old_string": str, "new_string": str}, []string{"path", "old_string", "new_string"})
	add("glob", "List workspace file paths matching a pattern; * and ? supported, ** not supported.", map[string]any{"pattern": str}, []string{"pattern"})
	add("grep", "Search regular workspace files using a Go regular expression.", map[string]any{"pattern": str}, []string{"pattern"})
	return specs
}

func (r *codexRestrictedRuntime) capabilities(model string) protocol.CapabilitiesPayload {
	group := protocol.CapabilityGroup{ID: protocol.CapGroupTools, Label: "Tools", Items: []protocol.CapabilityItem{}}
	for _, spec := range r.specs {
		name, _ := spec["name"].(string)
		description, _ := spec["description"].(string)
		group.Items = append(group.Items, protocol.CapabilityItem{Name: name, Description: description})
	}
	caps := protocol.CapabilitiesPayload{Engine: "codex", EngineName: "Codex", Model: model, PermissionMode: "restricted", Groups: []protocol.CapabilityGroup{group}, Note: "Workspace file access and granted MCP calls use these tools. Confined JavaScript orchestration and clock/resource helpers may also be available; native writes are blocked. No shell, web, subagents, plugins, hooks or ambient MCP connections."}
	if r.nativeSchemaChanged {
		caps.Note += " This native conversation retains earlier tool definitions. Start a new session for updated definitions; current Blerg grants still apply."
	}
	return caps
}

// Interactive grants keep the native coding surface. Their separately
// prepared private home removes ambient MCP before startup; only gateway
// tools are bridged here. The home must already be owned session state.
func prepareCodexGrantedRuntime(ctx context.Context, home string, prefix sandboxExec, gateway *protocol.MCPGatewayConfig) (*codexRestrictedRuntime, error) {
	if !filepath.IsAbs(home) {
		return nil, errors.New("interactive Codex grants require a private configuration home")
	}
	var version []byte
	var err error
	if prefix.enabled() {
		version, err = exec.CommandContext(ctx, "docker", "exec", prefix.container(), "codex", "--version").Output() //nolint:gosec // Fixed docker executable and argument vector; container comes from the daemon sandbox handle, never a shell.
	} else {
		version, err = exec.CommandContext(ctx, resolveEngineBinary("codex", false), "--version").Output() //nolint:gosec // Daemon-resolved installed binary, fixed version argument, caller context, and no shell.
	}
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli "+codexRestrictedVersion {
		return nil, errors.New("codex MCP grants require the verified CLI " + codexRestrictedVersion)
	}
	bridge, err := newCodexMCPBridge(ctx, gateway)
	if err != nil {
		return nil, err
	}
	return &codexRestrictedRuntime{Home: home, Restricted: false, prefix: prefix, bridge: bridge, specs: bridge.Specs()}, nil
}
