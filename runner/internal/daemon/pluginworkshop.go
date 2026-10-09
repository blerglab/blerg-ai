package daemon

// The plugin workshop: where a workstation daemon installs the account's
// always-on plugins (blerg-core Settings, delivered as SpawnSession.Plugins)
// WITHOUT touching the person's own ~/.claude.
//
// `claude plugin ...` honours CLAUDE_CONFIG_DIR for everything it writes
// (settings, known marketplaces, the plugin cache), so the daemon installs
// into <state dir>/plugins/claude. Each session then gets its own SNAPSHOT of
// the verified plugin directories (hardlinks, or a copy) under
// <workshop>/sessions/<session id>, and Claude Code is started with one
// `--plugin-dir=<snapshot>/<marketplace>/<plugin>/<version>` per plugin:
// loaded for that process only, nothing installed or enabled for the person,
// and immune to the cache being updated or rebuilt by a later launch while the
// session runs. A Local sandbox session gets its snapshot bind-mounted
// read-only (sandbox.go). The snapshot is removed when the session ends, and
// every leftover one is swept when the daemon starts (agent-kind sessions are
// not recovered across a daemon restart).
//
// Safety: the allow-list is the control (pluginsinstall re-checks it); the
// install children get an allow-listed environment and never a token; every
// path handed to --plugin-dir is verified to be a plugin directory inside
// the cache before it is snapshotted. --plugin-dir is NOT suppressed by the
// restricted-session hardening flags (verified on Claude Code 2.1.284), so
// AgentHost.spawn drops the list for any restricted or granted session before
// it gets here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/pluginsinstall"
)

const (
	// pluginRefreshInterval bounds how often marketplaces and installed
	// plugins are updated: a plugin's new version arrives on a later launch
	// without a reinstall, and a warm launch costs seconds, not a clone. The
	// same interval caps how often a broken workshop is rebuilt.
	pluginRefreshInterval = time.Hour
	pluginRefreshStamp    = ".refreshed"
	pluginRepairStamp     = ".repaired"
	// pluginPrepareBudget bounds one prepare: the daemon starts sessions one
	// at a time, so a launch must not hold the others for the installer's
	// worst case several times over.
	pluginPrepareBudget = 5 * time.Minute
	// pluginWorkshopDir is the workshop's path under the daemon state dir.
	pluginWorkshopDir = "plugins/claude"
	// pluginSessionsDir holds the per-session snapshots, under the workshop.
	pluginSessionsDir = "sessions"
	// installedPluginsVersion is the only installed_plugins.json shape read;
	// anything else fails closed.
	installedPluginsVersion = 2
)

// pluginWorkshop is one daemon's plugin install area. nil means "no
// workshop" (no state dir): sessions start without plugins.
type pluginWorkshop struct {
	dir   string // CLAUDE_CONFIG_DIR of the install children
	bin   string
	allow pluginspec.Allowlist
	home  string // HOME of the install children (the real home: git config and credential helpers)

	refreshEvery time.Duration
	run          pluginsinstall.Runner // nil = exec
	now          func() time.Time

	// mu serialises prepare: two sessions starting at once must not race the
	// CLI over one config dir. Per process; two daemons sharing a state dir
	// may still race each other, which DAEMON.md says.
	mu sync.Mutex
}

// pluginsCapability is what the daemon's hello says about SpawnSession.Plugins:
// honoured only with the claude CLI on the daemon's PATH (the workshop installs
// with it) and a state dir to keep the workshop in.
func pluginsCapability() bool { return claudeOnPath() && DaemonStateDir() != "" }

// NewPluginWorkshop builds the workshop under stateDir ("" → nil), sweeping
// the snapshots of sessions a previous daemon process left behind. The
// allow-list is read from BLERG_RUNNER_PLUGIN_MARKETPLACES, as the pod does.
func NewPluginWorkshop(stateDir string) *pluginWorkshop {
	if stateDir == "" {
		return nil
	}
	allow, dropped := pluginspec.ParseAllowlist(os.Getenv("BLERG_RUNNER_PLUGIN_MARKETPLACES"))
	for _, d := range dropped {
		log.Printf("daemon: plugins: ignoring invalid BLERG_RUNNER_PLUGIN_MARKETPLACES entry %q", d)
	}
	home, _ := os.UserHomeDir()
	w := newPluginWorkshop(filepath.Join(stateDir, pluginWorkshopDir), home, allow)
	if err := os.RemoveAll(w.sessionsDir()); err != nil { //nolint:gosec // G703: stateDir is the daemon's own state directory, not caller input
		log.Printf("daemon: plugins: sweeping old session snapshots: %v", err)
	}
	return w
}

func newPluginWorkshop(dir, home string, allow pluginspec.Allowlist) *pluginWorkshop {
	return &pluginWorkshop{dir: dir, bin: "claude", allow: allow, home: home,
		refreshEvery: pluginRefreshInterval, now: time.Now}
}

// cacheDir is the plugin cache the CLI installs into.
func (w *pluginWorkshop) cacheDir() string { return filepath.Join(w.dir, "plugins", "cache") }

// sessionsDir holds one snapshot directory per live session.
func (w *pluginWorkshop) sessionsDir() string { return filepath.Join(w.dir, pluginSessionsDir) }

// sessionDir is sessionID's snapshot: what its Claude Code loads from, and
// what its sandbox mounts. sessionID is a UUID the server minted; it is still
// reduced to a safe file name.
func (w *pluginWorkshop) sessionDir(sessionID string) string {
	return filepath.Join(w.sessionsDir(), safeFileName(sessionID))
}

func (w *pluginWorkshop) options() pluginsinstall.Options {
	extra := append([]string{"CLAUDE_CONFIG_DIR=" + w.dir}, gitSafetyEnv()...)
	return pluginsinstall.Options{
		Bin:       w.bin,
		Env:       pluginsinstall.ChildEnv(w.home, extra...),
		Allow:     w.allow,
		Secrets:   []string{os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"), os.Getenv("BLERG_RUNNER_DAEMON_TOKEN")},
		Run:       w.run,
		LogPrefix: "daemon: plugins",
	}
}

// prepare installs entries into the workshop, snapshots the verified plugin
// directories for sessionID and returns the snapshot paths to load, in list
// order, with the install result for the start panel. Never returns a
// directory for an entry that was not requested, that did not install, or
// that does not verify (see verifiedInstallPath): such an entry counts as
// failed. When the failure looks like a broken workshop — a marketplace that
// cannot be added or listed, an install path that does not verify — the
// workshop (a cache) is rebuilt and the run repeated once, at most once per
// refresh interval: a daemon restart or a timeout can leave a half-written
// marketplace that `marketplace add` would otherwise trust for ever. One
// plugin that simply fails to install is not that, and is just reported.
func (w *pluginWorkshop) prepare(ctx context.Context, sessionID string, entries []pluginspec.Entry) ([]string, pluginsinstall.Result) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, pluginPrepareBudget)
	defer cancel()
	dirs, res, corrupt := w.prepareOnce(ctx, entries, true)
	if corrupt && ctx.Err() == nil && w.stampDue(pluginRepairStamp) {
		log.Printf("daemon: plugins: the plugin workshop looks broken (%s); rebuilding it and retrying once", res.Detail())
		// Only the cache and marketplace state go; live sessions load from
		// their own snapshots, which stay.
		for _, p := range []string{filepath.Join(w.dir, "plugins"), filepath.Join(w.dir, "settings.json"), filepath.Join(w.dir, pluginRefreshStamp)} {
			if err := os.RemoveAll(p); err != nil {
				log.Printf("daemon: plugins: could not remove %s: %v", p, err)
			}
		}
		w.stamp(pluginRepairStamp)
		dirs, res, _ = w.prepareOnce(ctx, entries, false)
	}
	if len(dirs) == 0 {
		return nil, res
	}
	snap, err := w.snapshot(sessionID, dirs)
	if err != nil {
		log.Printf("daemon: plugins: session %s: snapshot: %v", sessionID, err)
		res.Installed = 0
		res.Failed = nil
		for _, e := range res.Entries {
			res.Failed = append(res.Failed, e.Plugin)
		}
		res.Entries = nil
		return nil, res
	}
	return snap, res
}

// prepareOnce is one pass: refresh (when due and allowed), install, verify.
// corrupt is true when the failure pattern is a broken workshop rather than
// one plugin that would not install.
func (w *pluginWorkshop) prepareOnce(ctx context.Context, entries []pluginspec.Entry, mayRefresh bool) ([]string, pluginsinstall.Result, bool) {
	// 0755: snapshots are mounted into sandbox containers and must be
	// readable there; the workshop holds no secrets (settings.json carries none).
	if err := os.MkdirAll(w.dir, 0o755); err != nil { //nolint:gosec // G301: snapshots are mounted into sandbox containers and must be readable there
		res := pluginsinstall.Result{Total: len(entries)}
		for _, e := range entries {
			res.Failed = append(res.Failed, safePluginName(e.Plugin))
		}
		log.Printf("daemon: plugins: %s: %v", w.dir, err)
		return nil, res, false
	}
	opts := w.options()
	if mayRefresh && w.stampDue(pluginRefreshStamp) {
		if names := w.knownMarketplaces(); len(names) > 0 {
			pluginsinstall.Update(ctx, opts, names, entries)
			w.stamp(pluginRefreshStamp)
		}
	}
	res := pluginsinstall.Install(ctx, opts, entries)
	corrupt := res.MarketplaceFailed
	if len(res.Entries) == 0 {
		res.Entries = nil
		return nil, res, corrupt
	}
	installed, err := w.readInstalled()
	if err != nil {
		log.Printf("daemon: plugins: %v", err)
		corrupt = true
	}
	var dirs []string
	var stillInstalled []pluginspec.Entry
	for _, e := range res.Entries {
		name := res.Names[strings.ToLower(e.Marketplace)]
		p, verr := w.verifiedInstallPath(installed, e.Plugin+"@"+name)
		if verr != nil {
			log.Printf("daemon: plugins: %s@%s: %v", e.Plugin, name, verr)
			res.Installed--
			res.Failed = append(res.Failed, e.Plugin)
			corrupt = true
			continue
		}
		dirs = append(dirs, p)
		stillInstalled = append(stillInstalled, e)
	}
	res.Entries = stillInstalled
	return dirs, res, corrupt
}

// snapshot copies the verified plugin directories into sessionID's own
// directory (hardlinks where the filesystem allows, a copy otherwise) and
// returns the paths there, in order. A previous snapshot of the same session
// is replaced.
func (w *pluginWorkshop) snapshot(sessionID string, dirs []string) ([]string, error) {
	dst := w.sessionDir(sessionID)
	if err := os.RemoveAll(dst); err != nil {
		return nil, err
	}
	cache, err := filepath.EvalSymlinks(w.cacheDir())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range dirs {
		rel, err := filepath.Rel(cache, d)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			_ = os.RemoveAll(dst)
			return nil, fmt.Errorf("%q is not inside the plugin cache", d)
		}
		target := filepath.Join(dst, rel)
		if err := linkTree(d, target); err != nil {
			_ = os.RemoveAll(dst)
			return nil, err
		}
		out = append(out, target)
	}
	return out, nil
}

// release removes sessionID's snapshot. Safe to call for a session that has none.
func (w *pluginWorkshop) release(sessionID string) {
	if w == nil {
		return
	}
	if err := os.RemoveAll(w.sessionDir(sessionID)); err != nil {
		log.Printf("daemon: plugins: session %s: removing snapshot: %v", sessionID, err)
	}
}

// linkTree recreates src under dst: directories are made, regular files are
// hard-linked (or copied when linking fails), symlinks are recreated as they
// are, anything else is skipped. src is a verified plugin directory.
func linkTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755) //nolint:gosec // G301: read by the sandbox; the workshop holds no secrets
		case d.Type()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target) //nolint:gosec // G122: src is the verified plugin directory the daemon just wrote
		case d.Type().IsRegular():
			if err := os.Link(p, target); err == nil { //nolint:gosec // G122: same tree
				return nil
			}
			return copyFile(p, target)
		}
		return nil
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // G304: src comes from the walk over the verified plugin directory
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode().Perm()|0o444) //nolint:gosec // G304: dst is under the daemon's own snapshot directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// knownMarketplaces maps source → name for the marketplaces already added,
// for the refresh (a marketplace not yet added has nothing to update).
func (w *pluginWorkshop) knownMarketplaces() map[string]string {
	raw, err := os.ReadFile(filepath.Join(w.dir, "plugins", "known_marketplaces.json"))
	if err != nil {
		return nil
	}
	var known map[string]struct {
		Source struct {
			Source string `json:"source"`
			Repo   string `json:"repo"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &known) != nil {
		return nil
	}
	out := map[string]string{}
	for name, m := range known {
		if m.Source.Source == "github" && m.Source.Repo != "" && pluginsinstall.ValidMarketplaceName(name) {
			out[strings.ToLower(m.Source.Repo)] = name
		}
	}
	return out
}

// stampDue reports whether the stamp file name is missing or older than the
// refresh interval.
func (w *pluginWorkshop) stampDue(name string) bool {
	fi, err := os.Stat(filepath.Join(w.dir, name))
	if err != nil {
		return true
	}
	return w.now().Sub(fi.ModTime()) >= w.refreshEvery
}

func (w *pluginWorkshop) stamp(name string) {
	p := filepath.Join(w.dir, name)
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		return
	}
	_ = os.Chtimes(p, w.now(), w.now())
}

// installedRecord is one scope of one plugin in installed_plugins.json.
type installedRecord struct {
	Scope       string `json:"scope"`
	InstallPath string `json:"installPath"`
}

// readInstalled parses <dir>/plugins/installed_plugins.json, failing closed on
// any shape but version 2: {"version":2,"plugins":{"<plugin>@<marketplace>":[{...}]}}.
func (w *pluginWorkshop) readInstalled() (map[string][]installedRecord, error) {
	raw, err := os.ReadFile(filepath.Join(w.dir, "plugins", "installed_plugins.json"))
	if err != nil {
		return nil, fmt.Errorf("installed_plugins.json: %w", err)
	}
	var f struct {
		Version int                          `json:"version"`
		Plugins map[string][]installedRecord `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("installed_plugins.json: %w", err)
	}
	if f.Version != installedPluginsVersion {
		return nil, fmt.Errorf("installed_plugins.json: unsupported version %d (want %d)", f.Version, installedPluginsVersion)
	}
	return f.Plugins, nil
}

var errNotInstalled = errors.New("not recorded as installed")

// verifiedInstallPath is the user-scope install path of key, verified to be
// a plugin directory (.claude-plugin/plugin.json) that resolves, symlinks
// followed on both sides, inside the workshop's cache; the resolved path is
// returned. Anything else is an error: a path from a file the CLI wrote is
// still a path the daemon is about to hand to an engine, and the cache is the
// only place it may point.
func (w *pluginWorkshop) verifiedInstallPath(installed map[string][]installedRecord, key string) (string, error) {
	var p string
	for _, r := range installed[key] {
		if r.Scope == "user" && r.InstallPath != "" {
			p = r.InstallPath
			break
		}
	}
	if p == "" {
		return "", errNotInstalled
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("install path %q is not absolute", p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("install path: %w", err)
	}
	cache, err := filepath.EvalSymlinks(w.cacheDir())
	if err != nil {
		return "", fmt.Errorf("plugin cache: %w", err)
	}
	rel, err := filepath.Rel(cache, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("install path %q is outside the plugin cache", p)
	}
	if fi, err := os.Stat(filepath.Join(resolved, ".claude-plugin", "plugin.json")); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("install path %q has no .claude-plugin/plugin.json", p)
	}
	return resolved, nil
}

// translatePluginDirs maps snapshot paths to where a sandbox container sees
// the mounted snapshot (sandboxPluginPath). Both sides are resolved through
// symlinks (a state dir under a symlinked home, macOS's /var) before the
// comparison; a path not under the snapshot is dropped — it would not exist
// in the container.
func translatePluginDirs(dirs []string, snapshotDir string) []string {
	base := filepath.Clean(snapshotDir)
	if r, err := filepath.EvalSymlinks(base); err == nil {
		base = r
	}
	var out []string
	for _, d := range dirs {
		d = filepath.Clean(d)
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r
		}
		rel, err := filepath.Rel(base, d)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		out = append(out, sandboxPluginPath+"/"+filepath.ToSlash(rel))
	}
	return out
}

// safePluginName keeps an invalid name out of an event.
func safePluginName(s string) string {
	if pluginspec.ValidPluginName(s) {
		return s
	}
	return "(invalid entry)"
}

// safeFileName reduces s to letters, digits, '-' and '_' for use as one path
// component (never empty).
func safeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}
