package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const wsOfficial = "anthropics/claude-plugins-official"

func wsEntry(p string) pluginspec.Entry { return pluginspec.Entry{Marketplace: wsOfficial, Plugin: p} }

// fakeWorkshopCLI is a Runner standing in for `claude plugin ...`: it records
// every argv and, on install, writes the cache directory and the
// installed_plugins.json record the real CLI would.
type fakeWorkshopCLI struct {
	t    *testing.T
	w    *pluginWorkshop
	mu   sync.Mutex
	args [][]string
	// failInstall names plugins whose install exits 1; noPluginJSON names
	// plugins installed without a manifest; escape points a plugin's
	// installPath outside the cache.
	failInstall, noPluginJSON, escape map[string]bool
	version                           int  // installed_plugins.json version (0 = 2)
	failMarketplace                   bool // `marketplace add` exits 1
	busy                              atomic.Int32
	overlap                           atomic.Bool
}

func (f *fakeWorkshopCLI) run(_ context.Context, _ string, args ...string) (string, error) {
	if f.busy.Add(1) > 1 {
		f.overlap.Store(true)
	}
	defer f.busy.Add(-1)
	time.Sleep(2 * time.Millisecond)
	f.mu.Lock()
	f.args = append(f.args, slices.Clone(args))
	f.mu.Unlock()
	switch {
	case len(args) >= 3 && args[1] == "marketplace" && args[2] == "list":
		return `[{"name":"claude-plugins-official","source":"github","repo":"` + wsOfficial + `"}]`, nil
	case len(args) >= 3 && args[1] == "marketplace" && args[2] == "add":
		if f.failMarketplace {
			return "fatal: could not read from remote", os.ErrPermission
		}
		known := filepath.Join(f.w.dir, "plugins", "known_marketplaces.json")
		_ = os.MkdirAll(filepath.Dir(known), 0o755)
		_ = os.WriteFile(known, []byte(`{"claude-plugins-official":{"source":{"source":"github","repo":"`+wsOfficial+`"}}}`), 0o644)
		return "", nil
	case len(args) >= 3 && args[1] == "install":
		plugin, _, _ := strings.Cut(args[2], "@")
		if f.failInstall[plugin] {
			return "boom", os.ErrPermission
		}
		dir := filepath.Join(f.w.cacheDir(), "claude-plugins-official", plugin, "1.0.0")
		if f.escape[plugin] {
			dir = filepath.Join(f.w.dir, "elsewhere", plugin)
		}
		if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if !f.noPluginJSON[plugin] {
			_ = os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"`+plugin+`"}`), 0o644)
		}
		f.record(args[2], dir)
		return "", nil
	}
	return "", nil
}

// record adds (or replaces) key's user-scope entry in installed_plugins.json.
func (f *fakeWorkshopCLI) record(key, dir string) {
	p := filepath.Join(f.w.dir, "plugins", "installed_plugins.json")
	file := struct {
		Version int                          `json:"version"`
		Plugins map[string][]installedRecord `json:"plugins"`
	}{Version: 2, Plugins: map[string][]installedRecord{}}
	if raw, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(raw, &file)
	}
	if f.version != 0 {
		file.Version = f.version
	}
	file.Plugins[key] = []installedRecord{{Scope: "user", InstallPath: dir}}
	raw, _ := json.Marshal(file)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeWorkshopCLI) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.args))
	for i, a := range f.args {
		out[i] = strings.Join(a, " ")
	}
	return out
}

func newTestWorkshop(t *testing.T) (*pluginWorkshop, *fakeWorkshopCLI) {
	t.Helper()
	allow, _ := pluginspec.ParseAllowlist("")
	w := newPluginWorkshop(filepath.Join(t.TempDir(), "state", "plugins", "claude"), t.TempDir(), allow)
	f := &fakeWorkshopCLI{t: t, w: w}
	w.run = f.run
	return w, f
}

func TestWorkshopPrepareReturnsVerifiedDirsForRequestedEntriesOnly(t *testing.T) {
	w, f := newTestWorkshop(t)
	// Something another account installed earlier is in the file but not in this list.
	f.record("leftover@claude-plugins-official", filepath.Join(w.cacheDir(), "claude-plugins-official", "leftover", "9"))

	dirs, res := w.prepare(context.Background(), "sess-1", []pluginspec.Entry{wsEntry("superpowers"), wsEntry("frontend-design")})
	// The session loads from its own snapshot, not the shared cache.
	snap := w.sessionDir("sess-1")
	want := []string{
		filepath.Join(snap, "claude-plugins-official", "superpowers", "1.0.0"),
		filepath.Join(snap, "claude-plugins-official", "frontend-design", "1.0.0"),
	}
	if !slices.Equal(dirs, want) {
		t.Fatalf("dirs = %v, want %v", dirs, want)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("snapshot lacks the plugin manifest: %v", err)
	}
	// Rebuilding the cache does not touch the snapshot; release removes it.
	if err := os.RemoveAll(w.cacheDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("snapshot shared fate with the cache: %v", err)
	}
	w.release("sess-1")
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Fatalf("snapshot not released: %v", err)
	}
	if res.Detail() != "2 of 2 installed" {
		t.Fatalf("detail = %q", res.Detail())
	}
	calls := f.calls()
	for _, c := range []string{
		"plugin marketplace add " + wsOfficial,
		"plugin marketplace list --json",
		"plugin install superpowers@claude-plugins-official --scope user",
	} {
		if !slices.Contains(calls, c) {
			t.Errorf("missing CLI call %q in %v", c, calls)
		}
	}
	// A first run has nothing to refresh (no marketplace known yet): no stamp,
	// so the next launch refreshes.
	if _, err := os.Stat(filepath.Join(w.dir, pluginRefreshStamp)); err == nil {
		t.Error("refresh stamp written although nothing was refreshed")
	}
	// The install children see the workshop, not the person's config, and git never prompts.
	env := strings.Join(w.options().Env, "\n")
	for _, want := range []string{"CLAUDE_CONFIG_DIR=" + w.dir, "HOME=" + w.home, "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(env, want) {
			t.Errorf("child env lacks %s", want)
		}
	}
	if fi, err := os.Stat(w.dir); err != nil || fi.Mode().Perm()&0o055 != 0o055 {
		t.Errorf("workshop dir must be world-readable for the sandbox mount: %v %v", fi.Mode(), err)
	}
}

func TestWorkshopFailedInstallIsReportedWithoutRebuilding(t *testing.T) {
	w, f := newTestWorkshop(t)
	f.failInstall = map[string]bool{"frontend-design": true}
	dirs, res := w.prepare(context.Background(), "s", []pluginspec.Entry{wsEntry("superpowers"), wsEntry("frontend-design")})
	if len(dirs) != 1 || !strings.HasSuffix(dirs[0], filepath.Join("superpowers", "1.0.0")) {
		t.Fatalf("dirs = %v", dirs)
	}
	if res.Detail() != "1 of 2 installed — frontend-design failed" {
		t.Fatalf("detail = %q", res.Detail())
	}
	// One plugin that will not install is not a broken workshop: no rebuild.
	n := 0
	for _, c := range f.calls() {
		if c == "plugin marketplace add "+wsOfficial {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("marketplace add ran %d times, want 1 (no rebuild): %v", n, f.calls())
	}
}

// A marketplace that cannot be added is a broken workshop: it is rebuilt and
// the run repeated once — and not again within the interval.
func TestWorkshopRebuildsOnceOnABrokenMarketplace(t *testing.T) {
	w, f := newTestWorkshop(t)
	f.failMarketplace = true
	_, res := w.prepare(context.Background(), "s", []pluginspec.Entry{wsEntry("superpowers")})
	if res.Installed != 0 {
		t.Fatalf("res = %+v", res)
	}
	count := func() int {
		n := 0
		for _, c := range f.calls() {
			if c == "plugin marketplace add "+wsOfficial {
				n++
			}
		}
		return n
	}
	if count() != 2 {
		t.Fatalf("marketplace add ran %d times, want 2 (one rebuild): %v", count(), f.calls())
	}
	if _, err := os.Stat(filepath.Join(w.dir, pluginRepairStamp)); err != nil {
		t.Fatal("repair stamp not written")
	}
	w.prepare(context.Background(), "s2", []pluginspec.Entry{wsEntry("superpowers")})
	if count() != 3 {
		t.Fatalf("a second rebuild ran inside the interval: %d adds", count())
	}
}

func TestWorkshopRefusesUnverifiedInstallPaths(t *testing.T) {
	w, f := newTestWorkshop(t)
	f.escape = map[string]bool{"a": true}
	f.noPluginJSON = map[string]bool{"b": true}
	dirs, res := w.prepare(context.Background(), "s", []pluginspec.Entry{wsEntry("a"), wsEntry("b"), wsEntry("c")})
	if len(dirs) != 1 || !strings.HasSuffix(dirs[0], filepath.Join("c", "1.0.0")) {
		t.Fatalf("dirs = %v", dirs)
	}
	if res.Installed != 1 || !slices.Equal(res.Failed, []string{"a", "b"}) {
		t.Fatalf("res = %+v", res)
	}
}

func TestWorkshopFailsClosedOnUnknownInstalledFileVersion(t *testing.T) {
	w, f := newTestWorkshop(t)
	f.version = 3
	dirs, res := w.prepare(context.Background(), "s", []pluginspec.Entry{wsEntry("superpowers")})
	if len(dirs) != 0 || res.Installed != 0 || len(res.Failed) != 1 {
		t.Fatalf("dirs = %v res = %+v", dirs, res)
	}
}

func TestWorkshopRefreshesAtMostOncePerInterval(t *testing.T) {
	w, f := newTestWorkshop(t)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	entries := []pluginspec.Entry{wsEntry("superpowers")}
	w.prepare(context.Background(), "a", entries) // first run: marketplace unknown, nothing to update
	w.prepare(context.Background(), "b", entries) // marketplace now known: refreshed and stamped
	w.prepare(context.Background(), "c", entries) // within the hour: no further update
	hasUpdate := func() bool {
		for _, c := range f.calls() {
			if strings.HasPrefix(c, "plugin update ") || strings.HasPrefix(c, "plugin marketplace update ") {
				return true
			}
		}
		return false
	}
	count := func(prefix string) int {
		n := 0
		for _, c := range f.calls() {
			if strings.HasPrefix(c, prefix) {
				n++
			}
		}
		return n
	}
	if !hasUpdate() || count("plugin update superpowers@") != 1 {
		t.Fatalf("expected exactly one refresh once the marketplace is known: %v", f.calls())
	}
	now = now.Add(2 * time.Hour)
	w.prepare(context.Background(), "d", entries)
	if count("plugin marketplace update claude-plugins-official") != 2 || count("plugin update superpowers@claude-plugins-official") != 2 {
		t.Fatalf("no refresh after the interval: %v", f.calls())
	}
}

func TestWorkshopSerialisesConcurrentPrepares(t *testing.T) {
	w, f := newTestWorkshop(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.prepare(context.Background(), "s", []pluginspec.Entry{wsEntry("superpowers")})
		}()
	}
	wg.Wait()
	if f.overlap.Load() {
		t.Fatal("two prepares drove the CLI at the same time")
	}
}

func TestTranslatePluginDirs(t *testing.T) {
	cache := "/home/u/.blerg-runner-daemon/plugins/claude/sessions/s1"
	got := translatePluginDirs([]string{cache + "/mp/sp/1.0.0", "/etc/passwd", cache}, cache)
	if !slices.Equal(got, []string{sandboxPluginPath + "/mp/sp/1.0.0"}) {
		t.Fatalf("got %v", got)
	}
	// A snapshot reached through a symlinked state dir (macOS /var, a linked
	// $HOME) still translates: both sides are resolved first.
	resolved := t.TempDir()
	if err := os.MkdirAll(filepath.Join(resolved, "mp", "sp", "2"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(resolved, link); err != nil {
		t.Skip("symlinks not supported")
	}
	got = translatePluginDirs([]string{filepath.Join(resolved, "mp", "sp", "2")}, link)
	if !slices.Equal(got, []string{sandboxPluginPath + "/mp/sp/2"}) {
		t.Fatalf("symlinked snapshot: got %v", got)
	}
}

// A workshop under a symlinked state dir hands out loadable snapshot paths.
func TestWorkshopUnderASymlinkedStateDir(t *testing.T) {
	resolved := t.TempDir()
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(resolved, link); err != nil {
		t.Skip("symlinks not supported")
	}
	allow, _ := pluginspec.ParseAllowlist("")
	w := newPluginWorkshop(filepath.Join(link, "plugins", "claude"), t.TempDir(), allow)
	f := &fakeWorkshopCLI{t: t, w: w}
	w.run = f.run
	dirs, res := w.prepare(context.Background(), "s", []pluginspec.Entry{wsEntry("superpowers")})
	if res.Installed != 1 || len(dirs) != 1 {
		t.Fatalf("dirs=%v res=%+v", dirs, res)
	}
	if got := translatePluginDirs(dirs, w.sessionDir("s")); !slices.Equal(got, []string{sandboxPluginPath + "/claude-plugins-official/superpowers/1.0.0"}) {
		t.Fatalf("translated = %v", got)
	}
}

func TestPluginsCapabilityNeedsTheCLIAndAStateDir(t *testing.T) {
	prev := claudeOnPath
	t.Cleanup(func() { claudeOnPath = prev })
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	claudeOnPath = func() bool { return false }
	if pluginsCapability() {
		t.Fatal("no CLI, yet the hello would claim plugins")
	}
	claudeOnPath = func() bool { return true }
	if !pluginsCapability() {
		t.Fatal("CLI and state dir present, yet no capability")
	}
	t.Setenv(DaemonStateDirEnv, "")
	t.Setenv("HOME", "")
	if pluginsCapability() {
		t.Fatal("no state dir, yet the hello would claim plugins")
	}
}

func TestCCArgsCarryPluginDirsAsSingleTokens(t *testing.T) {
	dirs := []string{"/c/mp/a/1", "/c/mp/b/2"}
	for _, args := range [][]string{
		ccTurnArgs("do it", "claude-opus-5-5", "max", "", withPluginDirs(dirs)),
		ccSessionArgs("claude-opus-5-5", "max", "", withPluginDirs(dirs)),
	} {
		n := len(args)
		if args[n-2] != "--plugin-dir=/c/mp/a/1" || args[n-1] != "--plugin-dir=/c/mp/b/2" {
			t.Errorf("plugin flags missing or not last: %v", args)
		}
	}
	if args := ccTurnArgs("do it", "", "", "", withPluginDirs(nil)); slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--plugin-dir") }) {
		t.Errorf("no dirs still emitted a flag: %v", args)
	}
}

func TestSandboxRunArgsMountPluginCacheReadOnly(t *testing.T) {
	args := sandboxRunArgs("c", "/p", "", nil, sandboxRunExtras{pluginCache: "/state/plugins/claude/plugins/cache"})
	joined := " " + strings.Join(args, " ") + " "
	if !strings.Contains(joined, " --mount type=bind,src=/state/plugins/claude/plugins/cache,dst="+sandboxPluginPath+",readonly ") {
		t.Fatalf("plugin cache mount missing: %s", joined)
	}
	if strings.Contains(" "+strings.Join(sandboxRunArgs("c", "/p", "", nil, sandboxRunExtras{}), " ")+" ", sandboxPluginPath) {
		t.Fatal("a session without plugins got the mount")
	}
}

// A kill_session that lands while the plugins install must stop the spawn: no session, no engine, no
// leftover snapshot.
func TestAgentSpawnKilledDuringPluginInstallDoesNotStart(t *testing.T) {
	engineDir := fakeClaude(t)
	t.Setenv("PATH", engineDir+":"+os.Getenv("PATH"))
	claudeLog := filepath.Join(engineDir, "calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, _ := newTestWorkshop(t)
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true, Plugins: w,
		SpawnKilled: func(string) bool { return true }})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-killed", Repo: "proj", Kind: "agent", InitialPrompt: "hello",
		Plugins: []pluginspec.Entry{wsEntry("superpowers")},
	})
	if host.Has("s-killed") {
		t.Error("a killed spawn registered a session")
	}
	if _, err := os.Stat(claudeLog); err == nil {
		t.Error("the engine ran for a session that was killed")
	}
	if _, err := os.Stat(w.sessionDir("s-killed")); !os.IsNotExist(err) {
		t.Errorf("snapshot left behind: %v", err)
	}
}

// A host agent spawn with a plugin list installs into the workshop, reports the
// stage, and starts Claude with the dirs; a restricted one loads none even
// though the server sent the list.
func TestAgentSpawnLoadsPluginsUnlessRestricted(t *testing.T) {
	engineDir := fakeClaude(t)
	t.Setenv("PATH", engineDir+":"+os.Getenv("PATH"))
	claudeLog := filepath.Join(engineDir, "calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, _ := newTestWorkshop(t)
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true, Plugins: w})
	plugins := []pluginspec.Entry{wsEntry("superpowers")}

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-plug", Repo: "proj", Kind: "agent", InitialPrompt: "hello", Plugins: plugins,
	})
	sender.waitForKind(t, "turn_done")
	raw, _ := os.ReadFile(claudeLog)
	wantDir := filepath.Join(w.sessionDir("s-plug"), "claude-plugins-official", "superpowers", "1.0.0")
	if !strings.Contains(string(raw), "--plugin-dir="+wantDir) {
		t.Errorf("claude argv lacks the plugin dir: %s", raw)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("snapshot missing while the session runs: %v", err)
	}
	var stages []string
	for _, ev := range sender.agentEvents() {
		if ev.Kind == protocol.StartStageKind {
			var p protocol.StartStagePayload
			_ = json.Unmarshal(ev.Payload, &p)
			for _, s := range p.Stages {
				stages = append(stages, s.ID+":"+s.State+":"+s.Detail)
			}
		}
	}
	if !slices.Equal(stages, []string{"plugins:active:Installing 1 plugin(s)", "plugins:done:1 of 1 installed"}) {
		t.Errorf("stage reports = %v", stages)
	}
	host.Kill("s-plug")
	if _, err := os.Stat(w.sessionDir("s-plug")); !os.IsNotExist(err) {
		t.Errorf("snapshot not released on Kill: %v", err)
	}

	if err := os.Remove(claudeLog); err != nil {
		t.Fatal(err)
	}
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-plug-r", Repo: "proj", Kind: "agent", InitialPrompt: "hello",
		Plugins: plugins, RestrictTools: true,
	})
	sender.waitForKind(t, "turn_done")
	raw, _ = os.ReadFile(claudeLog)
	if strings.Contains(string(raw), "--plugin-dir") {
		t.Errorf("a restricted session loaded plugins: %s", raw)
	}
	host.Kill("s-plug-r")

	// A daemon with no workshop says so rather than letting the plan fold the
	// stage to "installed".
	sender2 := &agentTestSender{}
	host2 := NewAgentHost(sender2, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
	host2.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-plug-nw", Repo: "proj", Kind: "agent", InitialPrompt: "hello", Plugins: plugins,
	})
	sender2.waitForKind(t, "turn_done")
	var warned bool
	for _, ev := range sender2.agentEvents() {
		if ev.Kind == protocol.StartStageKind {
			var p protocol.StartStagePayload
			_ = json.Unmarshal(ev.Payload, &p)
			for _, st := range p.Stages {
				warned = warned || (st.ID == protocol.StagePlugins && st.State == protocol.StageStateWarning && strings.Contains(st.Detail, "no plugin workshop"))
			}
		}
	}
	if !warned {
		t.Error("a dropped plugin list was not reported as a warning stage")
	}
	host2.Kill("s-plug-nw")
}

// A sandboxed spawn mounts the cache read-only and translates the dirs.
func TestSandboxedAgentSpawnMountsPluginCache(t *testing.T) {
	engineDir := fakeClaude(t)
	w, _ := newTestWorkshop(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{Plugins: w})
	writeClaudeLogin(t, home)
	claudeLog := filepath.Join(engineDir, "claude-calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-sbx-plug", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello", Plugins: []pluginspec.Entry{wsEntry("superpowers")},
	})
	sender.waitForKind(t, "turn_done")
	calls := dockerCalls(t, logPath)
	if !strings.Contains(calls, "--mount "+pluginCacheMountArg(w.sessionDir("s-sbx-plug"))) {
		t.Errorf("plugin snapshot not mounted read-only:\n%s", calls)
	}
	raw, _ := os.ReadFile(claudeLog)
	if !strings.Contains(string(raw), "--plugin-dir="+sandboxPluginPath+"/claude-plugins-official/superpowers/1.0.0") {
		t.Errorf("claude argv lacks the translated plugin dir: %s", raw)
	}
	if strings.Contains(string(raw), w.dir) {
		t.Errorf("a host path leaked into the container argv: %s", raw)
	}
	host.Kill("s-sbx-plug")
}
