package daemon

// Interactive grants preserve the native toolset. Only MCP is replaced by the
// daemon's credential-holding bridge; user configuration is copied, never edited.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func sanitizeCodexInteractiveHome(ctx context.Context, home, source, workDir string) error {
	return sanitizeCodexInteractiveHomeFor(ctx, home, source, workDir, false)
}

// sanitizeCodexInteractiveHomeFor is sanitizeCodexInteractiveHome; for a sandboxed session the
// person's config.toml first goes through the sandbox allowlist (sanitizeCodexConfig), so no notify
// command, shell environment, provider header or token reaches the container through this home.
func sanitizeCodexInteractiveHomeFor(ctx context.Context, home, source, workDir string, sandboxed bool) error {
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
	if sandboxed {
		raw = sanitizeCodexConfig(raw)
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
	// A new file renamed over the name: whatever is there (the normal home's config symlink, a link
	// or FIFO a sandboxed session planted in a reused snapshot) is replaced, never followed or opened.
	if err := replaceInDir(home, "config.toml", body); err != nil {
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
		raw, err := readRootRegular(root, path, credentialMax)
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
		return replaceInRoot(root, path, body)
	})
}

// readRootRegular reads a regular file inside root (the snapshot a sandboxed session may have
// written to): opened non-blocking, so a FIFO cannot hold the start, checked on the handle to be the
// very file looked at (no link swapped in), and bounded.
func readRootRegular(root *os.Root, path string, limit int64) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("codex plugin snapshot holds something that is not a regular file")
	}
	f, err := root.OpenFile(path, os.O_RDONLY|nonBlockFlag, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(opened, info) {
		return nil, errors.New("codex plugin snapshot changed while it was read")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("codex plugin snapshot file is too large")
	}
	return raw, nil
}

// replaceInRoot puts data at path inside root (0600) through a new file in the same directory and a
// rename: a link or FIFO at path is replaced, never followed or opened.
func replaceInRoot(root *os.Root, path string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.blerg-%d.tmp", filepath.Base(path), time.Now().UnixNano()))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = root.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, path); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

func prepareCodexInteractiveHome(ctx context.Context, source, workDir string) (string, error) {
	return prepareCodexInteractiveHomeFor(ctx, source, workDir, false)
}

// prepareCodexInteractiveHomeFor is prepareCodexInteractiveHome; sandboxed: see
// sanitizeCodexInteractiveHomeFor.
func prepareCodexInteractiveHomeFor(ctx context.Context, source, workDir string, sandboxed bool) (string, error) {
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
	if err := sanitizeCodexInteractiveHomeFor(ctx, home, source, workDir, sandboxed); err != nil {
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
