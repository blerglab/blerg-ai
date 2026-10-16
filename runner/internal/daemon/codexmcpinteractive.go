package daemon

// Interactive grants preserve the native toolset. Only MCP is replaced by the
// daemon's credential-holding bridge; user configuration is copied, never edited.
import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

func sanitizeCodexInteractiveHome(ctx context.Context, home, source, workDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return errors.New("codex interactive settings could not be read")
	}
	defer func() { _ = sourceRoot.Close() }()
	raw, err := sourceRoot.ReadFile("config.toml")
	config := map[string]any{}
	if err != nil && !os.IsNotExist(err) {
		return errors.New("codex interactive settings could not be read")
	}
	if len(raw) > 0 && toml.Unmarshal(raw, &config) != nil {
		return errors.New("codex interactive settings could not be parsed")
	}
	stripCodexMCPConfig(config)
	config["projects"] = map[string]any{workDir: map[string]any{"trust_level": "untrusted"}}
	body, err := toml.Marshal(config)
	if err != nil {
		return err
	}
	// Never write through the normal-home config symlink.
	path := filepath.Join(home, "config.toml")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		return err
	}
	return sanitizeCodexPluginMCP(home)
}
func stripCodexMCPConfig(config map[string]any) {
	delete(config, "mcp_servers")
	delete(config, "plugins")
	delete(config, "marketplaces")
	delete(config, "projects")
	for _, value := range config {
		if m, ok := value.(map[string]any); ok {
			stripCodexMCPConfig(m)
		}
	}
}

// Native plugin MCP declarations are data-only JSON. Remove their entry points
// from the private copy, retaining skills/hooks and every original source file.
func sanitizeCodexPluginMCP(home string) error {
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer func() { _ = homeRoot.Close() }()
	info, err := homeRoot.Lstat("plugins")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("codex plugin snapshot contains a symbolic link")
	}
	root, err := homeRoot.OpenRoot("plugins")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("codex plugin snapshot contains a symbolic link")
		}
		if d.Name() != "plugin.json" && d.Name() != ".mcp.json" && d.Name() != "mcp.json" {
			return nil
		}
		raw, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		var value map[string]any
		if json.Unmarshal(raw, &value) != nil {
			return errors.New("codex plugin MCP configuration is invalid")
		}
		delete(value, "mcpServers")
		delete(value, "mcp_servers")
		if d.Name() != "plugin.json" {
			value = map[string]any{"mcpServers": map[string]any{}}
		}
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return root.WriteFile(path, body, 0600)
	})
}

func prepareCodexInteractiveHome(ctx context.Context, source, workDir string) (string, error) {
	home, err := os.MkdirTemp("", "blerg-codex-granted-")
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(home)
		}
	}()
	for _, name := range []string{"auth.json", "skills", "AGENTS.md", "sessions"} {
		target := filepath.Join(source, name)
		if name == "sessions" {
			if err := os.MkdirAll(target, 0700); err != nil {
				return "", err
			}
		}
		if _, err := os.Stat(target); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", err
		}
		if err := os.Symlink(target, filepath.Join(home, name)); err != nil {
			return "", err
		}
	}
	if err := sanitizeCodexInteractiveHome(ctx, home, source, workDir); err != nil {
		return "", err
	}
	success = true
	return home, nil
}

// config/read resolves system/profile layers without starting a thread or MCP.
// Explicit disables also close the map-merge loophole in native thread overrides.
func codexDisableAmbientMCP(a *codexApp, cwd string, config map[string]any) error {
	raw, err := a.call("config/read", map[string]any{"cwd": cwd, "includeLayers": false})
	if err != nil {
		return errors.New("codex MCP configuration could not be inspected")
	}
	return codexAmbientMCPOverrides(raw, config)
}
func codexAmbientMCPOverrides(raw json.RawMessage, config map[string]any) error {
	var result struct {
		Config map[string]any `json:"config"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Config == nil {
		return errors.New("codex MCP configuration could not be inspected")
	}
	servers, _ := result.Config["mcp_servers"].(map[string]any)
	disabled := map[string]any{}
	for name := range servers {
		disabled[name] = map[string]any{"enabled": false}
	}
	if len(disabled) > 0 {
		config["mcp_servers"] = disabled
	}
	return nil
}
