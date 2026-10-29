package daemon

// The Codex provider of engineExtensions (engineextensions.go).
//
// Catalog: `codex plugin marketplace list --json` names the marketplaces Codex considers and their
// local roots; each root holds .agents/plugins/marketplace.json ({name, plugins: [{name, source:
// {source: "local", path}, category, policy}]}), and a local plugin's own .codex-plugin/plugin.json
// carries its description and version. Installed and enabled come from [plugins."<plugin>@<market>"]
// in the person's $CODEX_HOME/config.toml. Two homes are read: the person's own and the daemon's
// Codex plugin workshop (the marketplaces Blerg added). Every file is read with a size cap and only
// from inside its home (symlinks resolved); the result is bounded plain text.
// Skills: Codex plugins can bundle skills, so the plugin list covers installable ones; skills a
// person places by hand are already in the Capabilities report (skills/list).
//
// Sign-in: Codex app-server's mcpServer/oauth/login, on the session's own app-server and thread.
// It answers {authorizationUrl}; Codex listens for the callback on 127.0.0.1 on the session's
// machine and keeps the token in its own credential store.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/pluginsinstall"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	toml "github.com/pelletier/go-toml/v2"
)

const (
	codexConfigMax      = 1 << 20
	codexPluginJSONMax  = 256 << 10
	codexOAuthTimeout   = 300 // seconds Codex waits for the callback
	codexListBinDefault = "codex"
)

func init() { extensionProviders["codex"] = newCodexExtensions }

type codexExtensions struct {
	d        *codexDriver
	workshop *pluginWorkshop
	home     string // the person's CODEX_HOME
	userHome string
	loaded   []pluginspec.Entry
	run      pluginsinstall.Runner // nil = exec
}

func newCodexExtensions(h *AgentHost, sess *agentSession) (engineExtensions, error) {
	d, ok := sess.loop.(*codexDriver)
	if !ok || sess.sandboxed || len(d.prefix) > 0 {
		return nil, extUnsupported
	}
	h.mu.Lock()
	loaded := append([]pluginspec.Entry(nil), sess.pluginSpecs...)
	h.mu.Unlock()
	userHome, _ := os.UserHomeDir()
	var run pluginsinstall.Runner
	if h.cfg.Plugins != nil {
		run = h.cfg.Plugins.run
	}
	return &codexExtensions{d: d, workshop: h.cfg.Plugins, home: codexUserHome(d.env, userHome), userHome: userHome, loaded: loaded, run: run}, nil
}

// codexUserHome is the person's CODEX_HOME: from the session's environment (absolute only), the
// daemon's, else ~/.codex.
func codexUserHome(env []string, home string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "CODEX_HOME="); ok {
			if filepath.IsAbs(v) {
				return v
			}
			break
		}
	}
	if v := os.Getenv("CODEX_HOME"); v != "" && filepath.IsAbs(v) {
		return v
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// codexWorkshopDirs are the Codex workshop's CODEX_HOME and the empty HOME its CLI runs with
// (pluginworkshop prepareCodex).
func codexWorkshopDirs(w *pluginWorkshop) (home, emptyHome string) {
	base := filepath.Join(filepath.Dir(w.dir), "codex")
	return filepath.Join(base, "workshop"), filepath.Join(base, "installer-home")
}

func (c *codexExtensions) listOptions(codexHome, home string) pluginsinstall.Options {
	return pluginsinstall.Options{Bin: codexListBinDefault, Env: pluginsinstall.ChildEnv(home, append([]string{"CODEX_HOME=" + codexHome}, gitSafetyEnv()...)...),
		Run: c.run, LogPrefix: "daemon: codex catalog"}
}

func (c *codexExtensions) Catalog(ctx context.Context) (extCatalog, error) {
	type home struct{ codexHome, home string }
	homes := []home{}
	if c.home != "" {
		homes = append(homes, home{c.home, c.userHome})
	}
	if c.workshop != nil {
		wh, empty := codexWorkshopDirs(c.workshop)
		if _, err := os.Stat(wh); err == nil {
			homes = append(homes, home{wh, empty})
		}
	}
	var homesRead []codexHomeMarketplaces
	for _, h := range homes {
		list, err := pluginsinstall.ListCodexMarketplaces(ctx, c.listOptions(h.codexHome, h.home), protocol.CatalogMaxMarketplaces)
		if err != nil {
			continue
		}
		homesRead = append(homesRead, codexHomeMarketplaces{Dir: h.codexHome, List: list})
	}
	out := readCodexCatalog(homesRead, c.home, c.loaded)
	// Remote-only entries have no local manifest. Read them from this session's
	// trusted engine, never from a catalog path supplied by a browser or agent.
	if c.d != nil {
		c.d.mu.Lock()
		app := c.d.app
		c.d.mu.Unlock()
		if app != nil {
			raw, err := app.callContext(ctx, "plugin/list", map[string]any{"forceRefetch": false})
			if err == nil {
				out = appendCodexRemoteCatalog(out, raw, c.loaded)
			}
		}
	}
	return out, nil
}

func (c *codexExtensions) AddMarketplace(ctx context.Context, source string) error {
	w := c.workshop
	if w == nil {
		return extUnsupported
	}
	if !w.currentAllow().Allows(source) {
		return extNotAllowed
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	wh, empty := codexWorkshopDirs(w)
	for _, dir := range []string{wh, empty} {
		if _, err := secureAgentRecordsDir(dir, true); err != nil {
			return extAddFailed
		}
	}
	opts := pluginsinstall.Options{Bin: "codex", Env: pluginsinstall.ChildEnv(empty, append([]string{"CODEX_HOME=" + wh}, gitSafetyEnv()...)...),
		Allow: w.currentAllow(), Run: w.run, LogPrefix: "daemon: codex plugins"}
	if err := pluginsinstall.AddMarketplace(ctx, opts, source); err != nil {
		return extAddFailed
	}
	return nil
}

func (c *codexExtensions) MCPSignIn(ctx context.Context, server string) (extSignIn, error) {
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
	if found == nil || found.Source == "gateway" || found.Status != "needs-auth" {
		return extSignIn{}, extNotFound
	}
	// Only a server from the person's own global config.toml that no project layer redefines: one
	// the workspace (or a plugin) defined could hand Blerg's Sign in an authorization URL the
	// session chose. Those are signed in to from a terminal.
	if found.Source == "plugin" || !c.codexTrusted(server) {
		return extSignIn{}, extUntrusted
	}
	c.d.mu.Lock()
	a, thread := c.d.app, c.d.threadID
	c.d.mu.Unlock()
	if a == nil {
		return extSignIn{}, extUnavailable
	}
	params := map[string]any{"name": server, "timeoutSecs": codexOAuthTimeout}
	if thread != "" {
		params["threadId"] = thread
	}
	raw, err := a.callContext(ctx, "mcpServer/oauth/login", params)
	if err != nil {
		var rejected codexRPCRejected
		if errors.As(err, &rejected) {
			return extSignIn{}, extRefused
		}
		return extSignIn{}, extUnavailable
	}
	var res struct {
		AuthorizationURL string `json:"authorizationUrl"`
	}
	if len(raw) > 64<<10 || json.Unmarshal(raw, &res) != nil || res.AuthorizationURL == "" {
		return extSignIn{}, extRefused
	}
	// Check again now that the engine has answered: a project file renamed in while it read its
	// configuration would otherwise pass the first check and still hand over its URL.
	if !c.codexTrusted(server) {
		return extSignIn{}, extUntrusted
	}
	return extSignIn{AuthURL: res.AuthorizationURL, RequiresUserAction: true, CallbackExpected: true, Source: "user"}, nil
}

type codexManifest struct {
	Plugins []json.RawMessage `json:"plugins"`
}

type codexManifestPlugin struct {
	Name     ccLoose `json:"name"`
	Category ccLoose `json:"category"`
	Source   struct {
		Source ccLoose `json:"source"`
		Path   ccLoose `json:"path"`
	} `json:"source"`
}

type codexPluginJSON struct {
	Description ccLoose  `json:"description"`
	Version     ccLoose  `json:"version"`
	Author      ccAuthor `json:"author"`
	Interface   struct {
		ShortDescription ccLoose `json:"shortDescription"`
	} `json:"interface"`
}

// codexHomeMarketplaces is what `codex plugin marketplace list` reported for one CODEX_HOME.
type codexHomeMarketplaces struct {
	Dir  string
	List []pluginsinstall.CodexMarketplace
}

// readCodexCatalog reads the manifests of the marketplaces each home reported; earlier homes win
// a name both know. Only roots inside their home are read.
func readCodexCatalog(homes []codexHomeMarketplaces, personHome string, loaded []pluginspec.Entry) extCatalog {
	var out extCatalog
	installed, enabled := codexInstallState(personHome)
	loadedBy := map[string]bool{}
	for _, e := range loaded {
		loadedBy[strings.ToLower(e.Marketplace)+"\x00"+e.Plugin] = true
	}
	seen := map[string]bool{}
	for _, h := range homes {
		for _, m := range h.List {
			if seen[m.Name] || !protocol.ValidCatalogMarketplaceName(m.Name) || !filepath.IsAbs(m.Root) {
				continue
			}
			if len(out.Marketplaces) >= protocol.CatalogMaxMarketplaces {
				out.Truncated = true
				break
			}
			root, ok := within(h.Dir, m.Root)
			if !ok {
				continue
			}
			manifestPath, ok := within(root, filepath.Join(root, ".agents", "plugins", "marketplace.json"))
			if !ok {
				continue
			}
			body, err := readCatalogFile(manifestPath, ccMarketplaceManifest)
			if err != nil {
				continue
			}
			var man codexManifest
			if json.Unmarshal(body, &man) != nil {
				continue
			}
			seen[m.Name] = true
			out.Marketplaces = append(out.Marketplaces, protocol.CatalogMarketplace{Name: m.Name, Source: m.Source})
			for _, rp := range man.Plugins {
				if len(out.Plugins) >= protocol.CatalogMaxPlugins {
					out.Truncated = true
					break
				}
				var p codexManifestPlugin
				if json.Unmarshal(rp, &p) != nil || !pluginspec.ValidPluginName(string(p.Name)) {
					continue
				}
				entry := protocol.CatalogPlugin{Name: string(p.Name), Marketplace: m.Name, Category: string(p.Category)}
				if p.Source.Source == "local" && p.Source.Path != "" && !filepath.IsAbs(string(p.Source.Path)) {
					if pj, ok := within(root, filepath.Join(root, string(p.Source.Path), ".codex-plugin", "plugin.json")); ok {
						if raw, err := readCatalogFile(pj, codexPluginJSONMax); err == nil {
							var meta codexPluginJSON
							if json.Unmarshal(raw, &meta) == nil {
								entry.Description = string(meta.Description)
								if entry.Description == "" {
									entry.Description = string(meta.Interface.ShortDescription)
								}
								entry.Version, entry.Author = string(meta.Version), string(meta.Author)
							}
						}
					}
				}
				id := entry.Name + "@" + m.Name
				fromBlerg := m.Source != "" && loadedBy[strings.ToLower(m.Source)+"\x00"+entry.Name]
				entry.Managed = fromBlerg
				entry.Installed = installed[id] || fromBlerg
				entry.Enabled = (installed[id] && enabled[id]) || fromBlerg
				out.Plugins = append(out.Plugins, entry)
			}
		}
	}
	return out
}

// codexInstallState reads [plugins."<id>"] from $CODEX_HOME/config.toml: present = installed,
// enabled unless enabled = false.
func codexInstallState(home string) (installed, enabled map[string]bool) {
	installed, enabled = map[string]bool{}, map[string]bool{}
	if home == "" {
		return
	}
	raw, err := readHomeFile(filepath.Join(home, "config.toml"), codexConfigMax)
	if err != nil {
		return
	}
	var cfg struct {
		Plugins map[string]struct {
			Enabled *bool `toml:"enabled"`
		} `toml:"plugins"`
	}
	if toml.Unmarshal(raw, &cfg) != nil {
		return
	}
	for id, p := range cfg.Plugins {
		installed[id] = true
		enabled[id] = p.Enabled == nil || *p.Enabled
	}
	return
}

// codexMCPServerNames reads the [mcp_servers] names of one config.toml (home: an engine-home file,
// whose symlink may be followed; otherwise a workspace file, read without following anything).
// ok is false when the file exists but cannot be read safely or parsed: callers treat that as
// "defines every name".
func codexMCPServerNames(path string, home bool) (names map[string]bool, exists, ok bool) {
	if _, err := os.Lstat(path); err != nil {
		return nil, false, true
	}
	read := readCatalogFile
	if home {
		read = readHomeFile
	}
	raw, err := read(path, codexConfigMax)
	if err != nil {
		return nil, true, false
	}
	var cfg struct {
		MCP map[string]any `toml:"mcp_servers"`
	}
	if toml.Unmarshal(raw, &cfg) != nil {
		return nil, true, false
	}
	names = map[string]bool{}
	for n := range cfg.MCP {
		names[n] = true
	}
	return names, true, true
}

// codexRootMarkers are the names that end the walk for project config: Codex's
// project_root_markers from the global config when it can be read there, else [".git"].
func codexRootMarkers(home string) []string {
	raw, err := readHomeFile(filepath.Join(home, "config.toml"), codexConfigMax)
	if err != nil {
		return []string{".git"}
	}
	var cfg struct {
		Markers *[]string `toml:"project_root_markers"`
	}
	if toml.Unmarshal(raw, &cfg) != nil || cfg.Markers == nil {
		return []string{".git"}
	}
	var out []string
	for _, m := range *cfg.Markers {
		if m != "" && !strings.ContainsAny(m, "/\\\x00") && m != "." && m != ".." {
			out = append(out, m)
		}
	}
	return out // empty: no root marker, the walk goes up to the filesystem root
}

// codexProjectServers are the MCP server names that project-level .codex/config.toml files define
// for workDir, as Codex layers them over the global config: every directory from the workspace up
// to its project root (the first directory holding a root marker), for the workspace path as given
// and as resolved through symlinks. all: a project file could not be read or parsed (it might
// define anything).
func codexProjectServers(home, workDir string) (names map[string]bool, all bool) {
	names = map[string]bool{}
	if workDir == "" {
		return names, false
	}
	markers := codexRootMarkers(home)
	globalConfig := ""
	if home != "" {
		globalConfig, _ = filepath.Abs(filepath.Join(home, "config.toml"))
		if r, err := filepath.EvalSymlinks(globalConfig); err == nil {
			globalConfig = r
		}
	}
	starts := []string{filepath.Clean(workDir)}
	if r, err := filepath.EvalSymlinks(workDir); err == nil && r != starts[0] {
		starts = append(starts, r)
	}
	for _, dir := range starts {
		for range 64 {
			path := filepath.Join(dir, ".codex", "config.toml")
			resolved := path
			if r, err := filepath.EvalSymlinks(path); err == nil {
				resolved = r
			}
			if path != globalConfig && resolved != globalConfig {
				defined, exists, ok := codexMCPServerNames(path, false)
				if exists && !ok {
					all = true
				}
				for n := range defined {
					names[n] = true
				}
			}
			root := false
			for _, m := range markers {
				if _, err := os.Lstat(filepath.Join(dir, m)); err == nil {
					root = true
					break
				}
			}
			parent := filepath.Dir(dir)
			if root || parent == dir {
				break
			}
			dir = parent
		}
	}
	return names, all
}

// codexGlobalServer reports whether server is defined in the person's global $CODEX_HOME/config.toml.
func codexGlobalServer(home, server string) bool {
	if home == "" {
		return false
	}
	global, _, ok := codexMCPServerNames(filepath.Join(home, "config.toml"), true)
	return ok && global[server]
}

// codexTrusted: from the global config, and redefined by no project file, now or (snapshot) when
// the session's engine loaded its configuration.
func (c *codexExtensions) codexTrusted(server string) bool {
	if !codexGlobalServer(c.home, server) {
		return false
	}
	c.d.mu.Lock()
	snapAll, snap := c.d.projectMCPAll, c.d.projectMCPNames[server]
	c.d.mu.Unlock()
	if snapAll || snap {
		return false
	}
	names, all := codexProjectServers(c.home, c.d.workDir)
	return !all && !names[server]
}

// recordProjectMCP snapshots, when the app-server starts (and so loads its configuration), the
// server names project files define for the workspace. Snapshots only add: a file removed after
// the engine loaded it still counts.
func (d *codexDriver) recordProjectMCP() {
	if d.prefix.enabled() {
		return
	}
	userHome, _ := os.UserHomeDir()
	names, all := codexProjectServers(codexUserHome(d.env, userHome), d.workDir)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.projectMCPNames == nil {
		d.projectMCPNames = map[string]bool{}
	}
	for n := range names {
		d.projectMCPNames[n] = true
	}
	d.projectMCPAll = d.projectMCPAll || all
}

// appendCodexRemoteCatalog accepts only native remote entries, never arbitrary
// local paths masquerading as an installable marketplace source.
func appendCodexRemoteCatalog(out extCatalog, raw []byte, loaded []pluginspec.Entry) extCatalog {
	if len(raw) > protocol.CatalogResultMaxFrame {
		return out
	}
	var response struct {
		Marketplaces []struct {
			Name    string
			Path    *string
			Plugins []struct {
				Name, Version, LocalVersion, InstallPolicy, Availability, DisabledReason string
				Installed, Enabled                                                       bool
				Source                                                                   struct{ Type string }
				Interface                                                                struct{ ShortDescription, DeveloperName, Category string }
			}
		}
	}
	if json.Unmarshal(raw, &response) != nil {
		return out
	}
	known := map[string]bool{}
	for _, market := range out.Marketplaces {
		known[market.Name] = true
	}
	for _, market := range response.Marketplaces {
		source := "codex:" + market.Name
		if _, ok := pluginspec.CodexMarketplaceName(source); !ok || market.Path != nil || known[market.Name] {
			continue
		}
		if len(out.Marketplaces) >= protocol.CatalogMaxMarketplaces {
			out.Truncated = true
			break
		}
		added := false
		for _, p := range market.Plugins {
			if p.Source.Type != "remote" || !pluginspec.ValidPluginName(p.Name) {
				continue
			}
			if len(out.Plugins) >= protocol.CatalogMaxPlugins {
				out.Truncated = true
				break
			}
			if !added {
				out.Marketplaces = append(out.Marketplaces, protocol.CatalogMarketplace{Name: market.Name, Source: source})
				known[market.Name] = true
				added = true
			}
			managed := false
			for _, e := range loaded {
				if e.Marketplace == source && e.Plugin == p.Name {
					managed = true
				}
			}
			reason := ""
			if p.InstallPolicy == "NOT_AVAILABLE" || p.Availability == "DISABLED_BY_ADMIN" || p.DisabledReason != "" {
				reason = "Codex does not allow this plugin for your account."
			}
			version := p.Version
			if version == "" {
				version = p.LocalVersion
			}
			out.Plugins = append(out.Plugins, protocol.CatalogPlugin{Name: p.Name, Marketplace: market.Name, Description: p.Interface.ShortDescription, Version: version, Author: p.Interface.DeveloperName, Category: p.Interface.Category, Installed: p.Installed || managed, Enabled: p.Enabled || managed, Managed: managed, InstallReason: reason})
		}
	}
	return out
}
