package daemon

// Codex has no equivalent of Claude's --plugin-dir. An unrestricted session therefore gets
// a private CODEX_HOME with copied plugin cache entries and fixed links to its existing
// login, settings and conversations. Installation runs separately; native marketplaces use
// a disposable login copy and never writable links to the person's configuration.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/pluginsinstall"
)

// prepareEnginePlugins is registered with the engine that owns a native plugin lifecycle.
type prepareEnginePlugins func(context.Context, *pluginWorkshop, string, []pluginspec.Entry) (string, map[string]any, pluginsinstall.Result)

func prepareCodexPluginEngine(ctx context.Context, w *pluginWorkshop, id string, entries []pluginspec.Entry) (string, map[string]any, pluginsinstall.Result) {
	return w.prepareCodex(ctx, id, entries)
}

type codexSnapshotOptions struct {
	Requested []pluginspec.Entry
	Names     map[string]string
}

type codexPluginSnapshot struct {
	Entries    []pluginspec.Entry `json:"entries"`
	Requested  []pluginspec.Entry `json:"requested,omitempty"`
	IDs        []string           `json:"ids"`
	SourceHome string             `json:"source_home"`
}

// prepareCodex is for unrestricted native sessions. A sandbox mounts the private
// and source homes at their original absolute paths so fixed links remain valid.
func (w *pluginWorkshop) prepareCodex(ctx context.Context, sessionID string, entries []pluginspec.Entry, sources ...string) (string, map[string]any, pluginsinstall.Result) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(entries) > pluginspec.MaxEntries {
		return "", nil, failedCodexPlugins(entries)
	}
	for _, e := range entries {
		if pluginspec.ValidateCodex(e) != nil || !w.currentAllow().Allows(e.Marketplace) {
			res := pluginsinstall.Result{Total: len(entries)}
			for _, entry := range entries {
				res.Skipped = append(res.Skipped, safePluginName(entry.Plugin))
			}
			return "", nil, res
		}
	}
	source := os.Getenv("CODEX_HOME")
	if source == "" {
		source = filepath.Join(w.home, ".codex")
	}
	if len(sources) > 0 && sources[0] != "" {
		source = sources[0]
	}
	base := filepath.Join(filepath.Dir(w.dir), "codex")
	snapshot := filepath.Join(base, "sessions", safeFileName(sessionID))
	if home, config, err := existingCodexPluginHome(snapshot, source, entries); err == nil {
		return home, config, codexSnapshotResult(snapshot, entries)
	}
	workshop := filepath.Join(base, "workshop")
	emptyHome := filepath.Join(base, "installer-home")
	for _, dir := range []string{workshop, emptyHome} {
		if _, err := secureAgentRecordsDir(dir, true); err != nil {
			return "", nil, failedCodexPlugins(entries)
		}
	}
	// Native remote installs need Codex authentication, never the person's writable config.
	// Copy only auth into the daemon-owned install home and remove it even on timeout.
	native := false
	for _, entry := range entries {
		if _, ok := pluginspec.CodexMarketplaceName(entry.Marketplace); ok {
			native = true
		}
	}
	if native {
		// A disposable install home also removes copied login state after a failure;
		// its config cannot affect any running or later session.
		privateHome, err := os.MkdirTemp(base, ".native-install-")
		if err != nil {
			return "", nil, failedCodexPlugins(entries)
		}
		defer func() { _ = os.RemoveAll(privateHome) }()
		workshop = privateHome
		cleanup, err := copyCodexInstallerAuth(source, workshop)
		if err != nil {
			return "", nil, failedCodexPlugins(entries)
		}
		defer cleanup()
	}
	opts := pluginsinstall.Options{PrivateOutput: true, Bin: "codex", Env: pluginsinstall.ChildEnv(emptyHome, append([]string{"CODEX_HOME=" + workshop}, gitSafetyEnv()...)...), Allow: w.currentAllow(), Run: w.run, LogPrefix: "daemon: codex plugins"}
	res := pluginsinstall.InstallCodex(ctx, opts, entries)
	if res.Installed == 0 && len(entries) > 0 {
		return "", nil, res
	}
	home, config, err := snapshotCodexPluginHome(snapshot, source, workshop, res.Entries, res.Paths, codexSnapshotOptions{Requested: entries, Names: res.Names})
	if err != nil {
		return "", nil, failedCodexPlugins(entries)
	}
	return home, config, res
}

// prepareCodexRemoval copies only an already-loaded subset. Revocation must not
// force the owner to reinstall retained plugins or silently drop the others.
func (w *pluginWorkshop) prepareCodexRemoval(ctx context.Context, id string, entries []pluginspec.Entry, previous, source string) (string, map[string]any, pluginsinstall.Result) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fail := func() (string, map[string]any, pluginsinstall.Result) { return "", nil, failedCodexPlugins(entries) }
	if ctx.Err() != nil || len(entries) > pluginspec.MaxEntries {
		return fail()
	}
	raw, err := readCodexSnapshotMetadata(previous)
	var meta codexPluginSnapshot
	if err != nil || json.Unmarshal(raw, &meta) != nil || len(meta.IDs) != len(meta.Entries) {
		return fail()
	}
	selected := meta.Requested
	if selected == nil {
		selected = meta.Entries
	}
	if _, _, err := existingCodexPluginHome(previous, source, selected); err != nil {
		return fail()
	}
	paths, names := map[string]string{}, map[string]string{}
	for _, entry := range entries {
		if pluginspec.ValidateCodex(entry) != nil {
			return fail()
		}
		found := false
		for i, retained := range meta.Entries {
			if !strings.EqualFold(retained.Marketplace, entry.Marketplace) || retained.Plugin != entry.Plugin {
				continue
			}
			plugin, market, ok := strings.Cut(meta.IDs[i], "@")
			if !ok || plugin != entry.Plugin || !pluginsinstall.ValidMarketplaceName(market) {
				return fail()
			}
			path := filepath.Join(previous, "plugins", "cache", market, plugin)
			versions, err := os.ReadDir(path)
			if err != nil || len(versions) != 1 || !versions[0].IsDir() {
				return fail()
			}
			paths[meta.IDs[i]] = filepath.Join(path, versions[0].Name())
			names[strings.ToLower(entry.Marketplace)] = market
			found = true
			break
		}
		if !found {
			return fail()
		}
	}
	snapshot := filepath.Join(filepath.Dir(w.dir), "codex", "sessions", safeFileName(id))
	home, config, err := snapshotCodexPluginHome(snapshot, source, previous, entries, paths, codexSnapshotOptions{Requested: entries, Names: names})
	if err != nil {
		return fail()
	}
	return home, config, codexSnapshotResult(home, entries)
}

func failedCodexPlugins(entries []pluginspec.Entry) pluginsinstall.Result {
	res := pluginsinstall.Result{Total: len(entries)}
	if len(entries) == 0 {
		res.Failed = []string{"snapshot"}
	}
	for _, e := range entries {
		res.Failed = append(res.Failed, safePluginName(e.Plugin))
	}
	return res
}
func codexPluginConfig(home string, ids []string) map[string]any {
	plugins := map[string]any{}
	marketplaces := map[string]any{}
	for _, id := range ids {
		plugins[id] = map[string]any{"enabled": true}
		_, market, _ := strings.Cut(id, "@")
		marketplaces[market] = map[string]any{"source_type": "local", "source": filepath.Join(home, "marketplaces", market)}
	}
	return map[string]any{"plugins": plugins, "marketplaces": marketplaces}
}
func existingCodexPluginHome(snapshot, source string, entries []pluginspec.Entry) (string, map[string]any, error) {
	info, err := os.Lstat(snapshot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByDaemon(info) || info.Mode().Perm()&0o077 != 0 {
		return "", nil, errors.New("invalid Codex plugin snapshot")
	}
	raw, err := readCodexSnapshotMetadata(snapshot)
	if err != nil {
		return "", nil, err
	}
	var meta codexPluginSnapshot
	if json.Unmarshal(raw, &meta) != nil || meta.SourceHome != source {
		return "", nil, errors.New("codex plugin snapshot identity changed")
	}
	want, _ := json.Marshal(entries)
	selected := meta.Requested
	if selected == nil {
		selected = meta.Entries
	}
	got, _ := json.Marshal(selected)
	if string(want) != string(got) || len(meta.IDs) != len(meta.Entries) {
		return "", nil, errors.New("codex plugin snapshot selection changed")
	}
	return snapshot, codexPluginConfig(snapshot, meta.IDs), nil
}

func snapshotCodexPluginHome(snapshot, source, workshop string, entries []pluginspec.Entry, paths map[string]string, options ...codexSnapshotOptions) (string, map[string]any, error) {
	if home, config, err := existingCodexPluginHome(snapshot, source, entries); err == nil {
		return home, config, nil
	}
	if _, err := os.Lstat(snapshot); err == nil {
		return "", nil, errors.New("codex plugin snapshot already exists")
	}
	if !filepath.IsAbs(snapshot) || !filepath.IsAbs(source) || !filepath.IsAbs(workshop) {
		return "", nil, errors.New("codex plugin homes must be absolute")
	}
	if _, err := secureAgentRecordsDir(filepath.Dir(snapshot), true); err != nil {
		return "", nil, err
	}
	temp, err := os.MkdirTemp(filepath.Dir(snapshot), ".codex-plugin-home-")
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	meta := codexPluginSnapshot{Entries: entries, SourceHome: source}
	if len(options) > 0 {
		meta.Requested = options[0].Requested
	}
	catalogs := map[string][]map[string]any{}
	cache, err := filepath.EvalSymlinks(filepath.Join(workshop, "plugins", "cache"))
	if err != nil && len(entries) > 0 {
		return "", nil, err
	}
	for _, e := range entries {
		if pluginspec.ValidateCodex(e) != nil {
			return "", nil, errors.New("invalid Codex plugin entry")
		}
		found := false
		for id, path := range paths {
			plugin, market, ok := strings.Cut(id, "@")
			if len(options) > 0 && options[0].Names != nil && options[0].Names[strings.ToLower(e.Marketplace)] != market {
				continue
			}
			if !ok || plugin != e.Plugin || !pluginsinstall.ValidMarketplaceName(market) {
				continue
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", nil, err
			}
			rel, err := filepath.Rel(cache, resolved)
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if err != nil || len(parts) != 3 || parts[0] != market || parts[1] != plugin || parts[2] == ".." {
				return "", nil, errors.New("codex plugin path is outside selected cache entry")
			}
			if !codexPluginManifest(resolved, plugin) {
				return "", nil, errors.New("codex plugin manifest does not match selection")
			}
			if err := copyCodexPluginTree(resolved, filepath.Join(temp, "plugins", "cache", rel)); err != nil {
				return "", nil, err
			}
			// A frozen local catalogue makes native discovery independent of a later
			// workshop refresh. Catalogue sources alias only this session's copied files.
			catalogRoot := filepath.Join(temp, "marketplaces", market)
			if err := linkTree(filepath.Join(temp, "plugins", "cache", rel), filepath.Join(catalogRoot, "plugins", plugin)); err != nil {
				return "", nil, err
			}
			catalogs[market] = append(catalogs[market], map[string]any{"name": plugin, "source": map[string]any{"source": "local", "path": "./plugins/" + plugin}, "policy": map[string]any{"installation": "AVAILABLE", "authentication": "ON_INSTALL"}, "category": "Productivity"})
			meta.IDs = append(meta.IDs, id)
			found = true
			break
		}
		if !found {
			return "", nil, errors.New("selected Codex plugin was not installed")
		}
	}
	for market, plugins := range catalogs {
		dir := filepath.Join(temp, "marketplaces", market, ".agents", "plugins")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", nil, err
		}
		raw, err := json.Marshal(map[string]any{"name": market, "plugins": plugins})
		if err != nil {
			return "", nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "marketplace.json"), raw, 0o600); err != nil {
			return "", nil, err
		}
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = sourceRoot.Close() }()
	// Fixed names only; no plugin-supplied link or config path can reach the login.
	for _, name := range []string{"auth.json", "config.toml", "sessions"} {
		target := filepath.Join(source, name)
		info, err := sourceRoot.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) && name == "sessions" {
			// Rollouts must stay discoverable from the normal login home, even for
			// the first conversation ever started with managed plugins.
			if err := sourceRoot.MkdirAll(name, 0o700); err != nil {
				return "", nil, err
			}
			info, err = sourceRoot.Lstat(name)
		}
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !ownedByDaemon(info) || (name == "sessions" && !info.IsDir()) || (name != "sessions" && !info.Mode().IsRegular()) {
			return "", nil, errors.New("codex user state is not an owned regular path")
		}
		if name == "auth.json" && info.Mode().Perm()&0o077 != 0 {
			return "", nil, errors.New("codex login is not private")
		}
		if err := os.Symlink(target, filepath.Join(temp, name)); err != nil {
			return "", nil, err
		}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(temp, "blerg-plugins.json"), raw, 0o600); err != nil {
		return "", nil, err
	}
	if err := os.Rename(temp, snapshot); err != nil {
		return "", nil, err
	}
	return snapshot, codexPluginConfig(snapshot, meta.IDs), nil
}
func readCodexSnapshotMetadata(home string) ([]byte, error) {
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile("blerg-plugins.json")
}

func codexPluginManifest(path, name string) bool {
	root, err := os.OpenRoot(path)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	for _, path := range []string{"plugin.json", ".codex-plugin/plugin.json", ".claude-plugin/plugin.json"} {
		raw, err := root.ReadFile(path)
		if err != nil {
			continue
		}
		var p struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &p) == nil && p.Name == name {
			return true
		}
	}
	return false
}
func copyCodexPluginTree(src, dst string) error {
	source, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}
	destination, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = destination.Close() }()
	return fs.WalkDir(source.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return destination.MkdirAll(path, 0700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("codex plugin contains a link or special file")
		}
		// Root-scoped opens keep a concurrent symlink replacement inside the plugin
		// snapshot, including when an ancestor directory changes during traversal.
		in, err := source.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := destination.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// launchArgs exposes only selected, validated plugin enablement on argv. MCP configuration
// and every other RuntimeConfig value stay on stdin, where credentials cannot appear in ps.
func (d *codexDriver) launchArgs() []string {
	args := []string{"app-server", "--listen", "stdio://"}
	if d.NativeStateHome != "" {
		quoted, _ := json.Marshal(d.NativeStateHome)
		args = append(args, "-c", "sqlite_home="+string(quoted))
	}
	if d.codexToolsRestricted() {
		return args
	}
	// Native questions are delivered through Blerg's private ask/answer route.
	args = append(args, "-c", "features.default_mode_request_user_input=true")
	markets, _ := d.RuntimeConfig["marketplaces"].(map[string]any)
	names := make([]string, 0, len(markets))
	for name := range markets {
		names = append(names, name)
	}
	sort.Strings(names)
	marketEntries := []string{}
	for _, name := range names {
		if !pluginsinstall.ValidMarketplaceName(name) {
			continue
		}
		settings, _ := markets[name].(map[string]any)
		kind, _ := settings["source_type"].(string)
		source, _ := settings["source"].(string)
		if kind != "local" || !filepath.IsAbs(source) {
			continue
		}
		marketEntries = append(marketEntries, fmt.Sprintf("%q={source_type=%q,source=%q}", name, kind, source))
	}
	if len(marketEntries) > 0 {
		args = append(args, "-c", "marketplaces={"+strings.Join(marketEntries, ",")+"}")
	}
	plugins, _ := d.RuntimeConfig["plugins"].(map[string]any)
	ids := make([]string, 0, len(plugins))
	for id := range plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	pluginEntries := []string{}
	for _, id := range ids {
		name, market, ok := strings.Cut(id, "@")
		if !ok || !pluginspec.ValidPluginName(name) || !pluginsinstall.ValidMarketplaceName(market) {
			continue
		}
		settings, _ := plugins[id].(map[string]any)
		enabled, ok := settings["enabled"].(bool)
		if !ok {
			continue
		}
		pluginEntries = append(pluginEntries, fmt.Sprintf("%q={enabled=%t}", id, enabled))
	}
	if len(pluginEntries) > 0 {
		args = append(args, "-c", "plugins={"+strings.Join(pluginEntries, ",")+"}")
	}
	return args
}
func (d *codexDriver) launchEnv() []string {
	if d.CodexHome == "" {
		return d.env
	}
	env := make([]string, 0, len(d.env)+1)
	for _, e := range d.env {
		if !strings.HasPrefix(e, "CODEX_HOME=") {
			env = append(env, e)
		}
	}
	return append(env, "CODEX_HOME="+d.CodexHome)
}

func (w *pluginWorkshop) releaseCodex(sessionID string) {
	if w == nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(filepath.Dir(w.dir), "codex", "sessions", safeFileName(sessionID)))
}

func codexSnapshotResult(home string, requested []pluginspec.Entry) pluginsinstall.Result {
	raw, err := readCodexSnapshotMetadata(home)
	if err != nil {
		return failedCodexPlugins(requested)
	}
	var meta codexPluginSnapshot
	if json.Unmarshal(raw, &meta) != nil {
		return failedCodexPlugins(requested)
	}
	res := pluginsinstall.Result{Total: len(requested), Installed: len(meta.Entries), Entries: meta.Entries}
	for _, entry := range requested {
		found := false
		for _, installed := range meta.Entries {
			if installed == entry {
				found = true
				break
			}
		}
		if !found {
			res.Failed = append(res.Failed, safePluginName(entry.Plugin))
		}
	}
	return res
}

// The installer must never refresh credentials or settings through a link to the login home.
func copyCodexInstallerAuth(source, home string) (func(), error) {
	cleanup := func() { _ = os.Remove(filepath.Join(home, "auth.json")) }
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sourceRoot.Close() }()
	// Nonblocking/no-follow opens refuse a link or a FIFO substituted while the
	// daemon reads the session login. Every read stays anchored in that home.
	file, err := sourceRoot.OpenFile("auth.json", os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByDaemon(info) || info.Mode().Perm()&0077 != 0 || info.Size() > codexConfigMax {
		return nil, errors.New("codex login is not a private regular file within the size limit")
	}
	raw, err := io.ReadAll(io.LimitReader(file, codexConfigMax+1))
	if err != nil || len(raw) > codexConfigMax {
		return nil, errors.New("could not read codex installer login")
	}
	destination, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer func() { _ = destination.Close() }()
	copyFile, err := destination.OpenFile("auth.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, writeErr := copyFile.Write(raw)
	closeErr := copyFile.Close()
	if writeErr != nil || closeErr != nil {
		cleanup()
		return nil, errors.New("could not prepare codex installer login")
	}
	return cleanup, nil
}
