package daemon

// The Claude Code provider of engineExtensions (engineextensions.go).
//
// Catalog: Claude Code records the marketplaces it knows in <config dir>/plugins/
// known_marketplaces.json ({name: {source: {source: "github", repo}, installLocation}}), clones each
// one under <config dir>/plugins/marketplaces/<name>, and describes its plugins in that clone's
// .claude-plugin/marketplace.json. Installs are in plugins/installed_plugins.json (version 2) and
// the on/off switch is enabledPlugins in the user settings file. Nothing is read from the session's
// workspace (the session can write there). The catalog
// reads two config dirs: the person's own (CLAUDE_CONFIG_DIR or ~/.claude: what they added
// themselves) and the daemon's plugin workshop (the marketplaces Blerg added). Every file is read
// with a size cap, a manifest only from inside its config dir's marketplaces directory (symlinks
// resolved), and the result is bounded and reduced to plain text (protocol.SanitizePluginCatalog).
// Skills: Claude Code ships installable skills inside plugins, so the plugin list covers them.
//
// Sign-in: Claude Code's stream-json control request mcp_authenticate (claudemcpstate.go).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/pluginsinstall"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// File caps for the catalog read.
const (
	ccKnownMarketplacesMax = 256 << 10
	ccMarketplaceManifest  = 8 << 20
	ccInstalledPluginsMax  = 4 << 20
	ccSettingsMax          = 1 << 20
)

type claudeExtensions struct {
	d         *claudeCodeDriver
	workshop  *pluginWorkshop
	personDir string
	cwd       string
	loaded    []pluginspec.Entry
}

func newClaudeExtensions(h *AgentHost, sess *agentSession) (engineExtensions, error) {
	d, ok := sess.loop.(*claudeCodeDriver)
	if !ok || sess.sandboxed || len(d.prefix) > 0 {
		return nil, extUnsupported
	}
	h.mu.Lock()
	loaded := append([]pluginspec.Entry(nil), sess.pluginSpecs...)
	h.mu.Unlock()
	home, _ := os.UserHomeDir()
	return &claudeExtensions{d: d, workshop: h.cfg.Plugins, personDir: claudeUserConfigDir(d.env, home), cwd: d.workDir, loaded: loaded}, nil
}

// claudeUserConfigDir is the config dir the session's Claude Code reads: CLAUDE_CONFIG_DIR from
// the environment it starts with (absolute only), else ~/.claude.
func claudeUserConfigDir(env []string, home string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "CLAUDE_CONFIG_DIR="); ok {
			if filepath.IsAbs(v) {
				return v
			}
			break
		}
	}
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" && filepath.IsAbs(v) {
		return v
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

func (c *claudeExtensions) Catalog(ctx context.Context) (extCatalog, error) {
	dirs := []string{c.personDir}
	if c.workshop != nil {
		dirs = append(dirs, c.workshop.dir)
	}
	return readClaudeCatalog(ctx, dirs, c.personDir, c.cwd, c.loaded), nil
}

func (c *claudeExtensions) AddMarketplace(ctx context.Context, source string) error {
	if c.workshop == nil {
		return extUnsupported
	}
	if !c.workshop.allow.Allows(source) {
		return extNotAllowed
	}
	if err := c.workshop.addMarketplace(ctx, source); err != nil {
		if errors.Is(err, pluginsinstall.ErrNotAllowed) {
			return extNotAllowed
		}
		return extAddFailed
	}
	return nil
}

func (c *claudeExtensions) MCPSignIn(ctx context.Context, server string) (extSignIn, error) {
	probe, err := c.d.MCPStatus(ctx)
	if err != nil {
		return extSignIn{}, extUnavailable
	}
	var found *protocol.MCPServerStatus
	for i := range probe.Servers {
		if probe.Servers[i].Name == server {
			found = &probe.Servers[i]
			break
		}
	}
	switch {
	case found == nil, found.Source == "gateway":
		return extSignIn{}, extNotFound
	case found.Source == "claude.ai":
		return extSignIn{}, extConnector
	case found.Status != "needs-auth":
		return extSignIn{}, extNotFound
	case found.Source != "user":
		// Only a server from the person's own user settings: one the workspace defined (project,
		// local), or any other scope, could hand Blerg's Sign in an authorization URL the session
		// chose. Those are signed in to from a terminal.
		return extSignIn{}, extUntrusted
	}
	reply, ok, err := c.d.mcpAuthenticate(ctx, server)
	if err != nil {
		return extSignIn{}, extUnavailable
	}
	if !ok {
		return extSignIn{}, extRefused
	}
	out := extSignIn{AuthURL: reply.AuthURL, RequiresUserAction: reply.RequiresUserAction, CallbackExpected: reply.CallbackExpected, Source: "user"}
	return out, nil
}

// addMarketplace adds one allow-listed source to the workshop (fixed argv, the install children's
// environment), serialized with prepare.
func (w *pluginWorkshop) addMarketplace(ctx context.Context, source string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(w.dir, 0o755); err != nil { //nolint:gosec // G301: the workshop is mounted read-only into sandboxes; it holds no secrets
		return err
	}
	return pluginsinstall.AddMarketplace(ctx, w.options(), source)
}

// readCatalogFile reads a regular file of at most limit bytes; anything else is an error. It never
// blocks on what it finds: the path is Lstat-ed first (a FIFO, device, socket or symlink is
// refused), opened with O_NONBLOCK|O_NOFOLLOW (a FIFO or a symlink swapped in after the Lstat
// cannot hold the open, nor redirect it), and checked again on the open file.
func readCatalogFile(path string, limit int64) ([]byte, error) {
	if li, err := os.Lstat(path); err != nil || !li.Mode().IsRegular() || li.Size() > limit { //nolint:gosec // G703: callers pass paths under an engine home or workspace they resolved; nothing here follows or writes
		return nil, errors.New("not a regular file within the size cap")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: callers pass paths under an engine home they resolved
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > limit {
		return nil, errors.New("file too large")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("file too large")
	}
	return raw, nil
}

// readHomeFile reads a file of an engine's own home (settings, config, records): like
// readCatalogFile, except that a symlink (a dotfile manager's) is followed when its final target is
// a regular file owned by the person the daemon runs as. A FIFO, a device or a file of someone else
// is still refused, and the target is opened with the same non-blocking, no-follow check.
func readHomeFile(path string, limit int64) ([]byte, error) {
	li, err := os.Lstat(path) //nolint:gosec // G703: callers pass paths under an engine home or workspace they resolved; nothing here follows or writes
	if err != nil {
		return nil, err
	}
	if li.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		ti, err := os.Lstat(target) //nolint:gosec // G703: callers pass paths under an engine home or workspace they resolved; nothing here follows or writes
		if err != nil || !ti.Mode().IsRegular() || !ownedByDaemon(ti) {
			return nil, errors.New("symlink target is not a regular file of this user")
		}
		path = target
	}
	return readCatalogFile(path, limit)
}

// within reports whether path, symlinks resolved, is strictly inside root (resolved too).
func within(root, path string) (string, bool) {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(r, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return p, true
}

type ccKnownMarketplace struct {
	Source struct {
		Source string `json:"source"`
		Repo   string `json:"repo"`
		URL    string `json:"url"`
	} `json:"source"`
	InstallLocation string `json:"installLocation"`
}

// ccAuthor is a manifest author: a string or {name}.
type ccAuthor string

func (a *ccAuthor) UnmarshalJSON(raw []byte) error {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		*a = ccAuthor(s)
		return nil
	}
	var o struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &o) == nil {
		*a = ccAuthor(o.Name)
	}
	return nil
}

// ccLoose is a manifest string field that tolerates another JSON type (it is then empty).
type ccLoose string

func (l *ccLoose) UnmarshalJSON(raw []byte) error {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		*l = ccLoose(s)
	}
	return nil
}

type ccManifest struct {
	Description ccLoose `json:"description"`
	Metadata    struct {
		Description ccLoose `json:"description"`
	} `json:"metadata"`
	Plugins []json.RawMessage `json:"plugins"`
}

type ccManifestPlugin struct {
	Name        ccLoose  `json:"name"`
	Description ccLoose  `json:"description"`
	Version     ccLoose  `json:"version"`
	Author      ccAuthor `json:"author"`
	Category    ccLoose  `json:"category"`
}

// readClaudeCatalog lists the marketplaces known in dirs (earlier dirs win a name both know) and
// their plugins. Installed and enabled come from personDir's records for the workspace cwd, and
// from loaded (what Blerg loaded into the session).
func readClaudeCatalog(ctx context.Context, dirs []string, personDir, cwd string, loaded []pluginspec.Entry) extCatalog {
	var out extCatalog
	seen := map[string]bool{}
	installed, enabled := claudeInstallState(personDir, cwd)
	loadedBy := map[string]bool{} // lower(source) + "\x00" + plugin
	for _, e := range loaded {
		loadedBy[strings.ToLower(e.Marketplace)+"\x00"+e.Plugin] = true
	}
	for _, dir := range dirs {
		if dir == "" || ctx.Err() != nil {
			continue
		}
		raw, err := readHomeFile(filepath.Join(dir, "plugins", "known_marketplaces.json"), ccKnownMarketplacesMax)
		if err != nil {
			continue
		}
		var known map[string]ccKnownMarketplace
		if json.Unmarshal(raw, &known) != nil {
			continue
		}
		names := make([]string, 0, len(known))
		for n := range known {
			names = append(names, n)
		}
		sort.Strings(names)
		root := filepath.Join(dir, "plugins", "marketplaces")
		for _, name := range names {
			if seen[name] || !protocol.ValidCatalogMarketplaceName(name) {
				continue
			}
			if len(out.Marketplaces) >= protocol.CatalogMaxMarketplaces {
				out.Truncated = true
				break
			}
			m := known[name]
			source := ""
			switch m.Source.Source {
			case "github":
				if pluginspec.ValidMarketplaceSource(m.Source.Repo) {
					source = m.Source.Repo
				}
			case "git", "url":
				if s, ok := protocol.MarketplaceSourceFromInput(m.Source.URL); ok {
					source = s
				}
			}
			loc := m.InstallLocation
			if loc == "" || !filepath.IsAbs(loc) {
				loc = filepath.Join(root, name)
			}
			manifestPath, ok := within(root, filepath.Join(loc, ".claude-plugin", "marketplace.json"))
			if !ok {
				continue
			}
			body, err := readCatalogFile(manifestPath, ccMarketplaceManifest)
			if err != nil {
				continue
			}
			var man ccManifest
			if json.Unmarshal(body, &man) != nil {
				continue
			}
			seen[name] = true
			desc := string(man.Description)
			if desc == "" {
				desc = string(man.Metadata.Description)
			}
			out.Marketplaces = append(out.Marketplaces, protocol.CatalogMarketplace{Name: name, Source: source, Description: desc})
			for _, rp := range man.Plugins {
				if len(out.Plugins) >= protocol.CatalogMaxPlugins {
					out.Truncated = true
					break
				}
				var p ccManifestPlugin
				if json.Unmarshal(rp, &p) != nil || !pluginspec.ValidPluginName(string(p.Name)) {
					continue
				}
				id := string(p.Name) + "@" + name
				fromBlerg := source != "" && loadedBy[strings.ToLower(source)+"\x00"+string(p.Name)]
				out.Plugins = append(out.Plugins, protocol.CatalogPlugin{
					Name: string(p.Name), Marketplace: name, Description: string(p.Description), Version: string(p.Version),
					Author: string(p.Author), Category: string(p.Category),
					Installed: installed[id] || fromBlerg, Enabled: (installed[id] && enabled[id]) || fromBlerg,
				})
			}
		}
	}
	return out
}

// claudeInstallState reads which plugins the person has installed for cwd and which are enabled,
// from the engine home only (installed_plugins.json, settings.json). The workspace's own
// .claude/settings*.json are deliberately not read: the session can write them, so they could
// only forge the flag (or, as a FIFO, hold the read).
func claudeInstallState(personDir, cwd string) (installed, enabled map[string]bool) {
	installed, enabled = map[string]bool{}, map[string]bool{}
	if personDir == "" {
		return
	}
	cleanCwd := ""
	if cwd != "" {
		cleanCwd = filepath.Clean(cwd)
	}
	if raw, err := readHomeFile(filepath.Join(personDir, "plugins", "installed_plugins.json"), ccInstalledPluginsMax); err == nil {
		var f struct {
			Version int `json:"version"`
			Plugins map[string][]struct {
				Scope       string `json:"scope"`
				ProjectPath string `json:"projectPath"`
			} `json:"plugins"`
		}
		if json.Unmarshal(raw, &f) == nil && f.Version == installedPluginsVersion {
			for id, recs := range f.Plugins {
				for _, r := range recs {
					switch r.Scope {
					case "user", "managed":
						installed[id] = true
					case "project", "local":
						if cleanCwd != "" && r.ProjectPath != "" && filepath.Clean(r.ProjectPath) == cleanCwd {
							installed[id] = true
						}
					}
				}
			}
		}
	}
	if raw, err := readHomeFile(filepath.Join(personDir, "settings.json"), ccSettingsMax); err == nil {
		var s struct {
			Enabled map[string]json.RawMessage `json:"enabledPlugins"`
		}
		if json.Unmarshal(raw, &s) == nil {
			for id, v := range s.Enabled {
				var on bool
				if json.Unmarshal(v, &on) == nil {
					enabled[id] = on
				}
			}
		}
	}
	return
}
