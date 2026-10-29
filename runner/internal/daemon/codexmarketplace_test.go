package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Extend the fake native app-server with the real CLI command shape. The
// installer gets no fixture flags or session credentials through its environment.
func codexMarketplaceHost(t *testing.T) (*AgentHost, *codexDriver, *agentTestSender, *collectEmitter, string) {
	t.Helper()
	bin := fakeCodex(t)
	script, err := os.ReadFile(filepath.Join(bin, "codex"))
	if err != nil {
		t.Fatal(err)
	}
	install := `
if sys.argv[1:3]==["plugin","add"]:
 assert sys.argv[3:]==["selected@market","--json"]
 assert not sys.stdin.isatty() and sys.stdin.read()==""
 assert "BLERG_RUNNER_SESSION_TOKEN" not in os.environ and "OPENAI_API_KEY" not in os.environ
 home=os.environ["CODEX_HOME"]
 assert ".native-install-" in home
 assert not os.path.islink(os.path.join(home,"auth.json"))
 with open(os.path.join(home,"auth.json")) as f: assert json.load(f)=={"native_fixture":True}
 with open(os.path.join(home,"config.toml"),"w") as f: f.write("installer_settings = true\n")
 path=os.path.join(home,"plugins","cache","market","selected","1.0.0")
 os.makedirs(path,exist_ok=True)
 with open(os.path.join(path,"plugin.json"),"w") as f: json.dump({"name":"selected"},f)
 print(json.dumps({"pluginId":"selected@market","name":"selected","marketplaceName":"market","installedPath":path}))
 sys.exit(0)
`
	script = []byte(strings.Replace(string(script), "if \"--version\" in sys.argv:", install+"\nif \"--version\" in sys.argv:", 1))
	if err := os.WriteFile(filepath.Join(bin, "codex"), script, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	// If prepare used the daemon's home instead of the session's, the fake CLI
	// would see this empty home and reject the missing native login.
	t.Setenv("CODEX_HOME", t.TempDir())
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "auth.json"), `{"native_fixture":true}`)
	writeTestFile(t, filepath.Join(source, "config.toml"), "model = \"fixture-original\"\n")
	allow, _ := pluginspec.ParseAllowlist("codex:market")
	w := newPluginWorkshop(filepath.Join(t.TempDir(), "plugins", "claude"), t.TempDir(), allow)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Env: append(sanitizedEnviron(), "CODEX_HOME="+source), Emitter: em})
	sender := &agentTestSender{}
	h := NewAgentHost(sender, AgentHostConfig{Plugins: w, RecordsDir: t.TempDir()})
	h.sessions["session"] = &agentSession{engine: "codex", loop: d, record: newRecordKeeper(h.cfg.RecordsDir, nil, AgentRecord{SessionID: "session", Repo: "repo", WorkDir: d.workDir})}
	t.Cleanup(func() {
		raw, _ := os.ReadFile(filepath.Join(source, "config.toml"))
		if string(raw) != "model = \"fixture-original\"\n" {
			t.Error("installer changed source config")
		}
		raw, _ = os.ReadFile(filepath.Join(source, "auth.json"))
		if string(raw) != `{"native_fixture":true}` {
			t.Error("installer changed source auth")
		}
		leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(w.dir), "codex", ".native-install-*"))
		if len(leftovers) > 0 {
			t.Error("installer retained private homes")
		}
	})
	return h, d, sender, em, source
}

func TestCodexMarketplaceLiveInstallAndRecovery(t *testing.T) {
	for _, boundary := range []string{"success", "initialize", "thread/resume"} {
		t.Run(boundary, func(t *testing.T) {
			if boundary != "success" {
				t.Setenv("FAKE_CODEX_REJECT_PLUGIN", boundary)
			}
			nativeLog := filepath.Join(t.TempDir(), "native.log")
			t.Setenv("FAKE_CODEX_LOG", nativeLog)
			h, d, sender, em, _ := codexMarketplaceHost(t)
			stop := startTestCodexDriver(context.Background(), t, d)
			defer stop()
			d.Enqueue("before", "chat")
			waitUntil(t, "first turn", func() bool { return countKind(em, "turn_done") == 1 })
			entry := pluginspec.Entry{Marketplace: "codex:market", Plugin: "selected"}
			h.ApplySessionPlugins(protocol.ApplySessionPlugins{SessionID: "session", RequestID: "native", Plugins: []pluginspec.Entry{entry}})
			state := "applied"
			if boundary != "success" {
				state = "failed"
			}
			waitCodexPluginResult(t, sender, "native", state)
			d.Enqueue("after", "chat")
			waitUntil(t, "next turn", func() bool { return countKind(em, "turn_done") == 2 })
			rawLog, _ := os.ReadFile(nativeLog)
			if !strings.Contains(string(rawLog), "selected@market") || strings.Count(string(rawLog), "thread/start ") != 1 || !strings.Contains(string(rawLog), `"threadId": "cx-thread-1"`) {
				t.Fatal("native replacement did not preserve the thread")
			}
			records := listAgentRecords(h.cfg.RecordsDir)
			if len(records) != 1 || records[0].PluginRequests["native"].State != state {
				t.Fatal("native result not durable")
			}
			if boundary == "success" {
				if len(records[0].Plugins) != 1 || records[0].Plugins[0] != entry {
					t.Fatal("native selection not persisted")
				}
				d.mu.Lock()
				home := d.CodexHome
				d.mu.Unlock()
				if !codexPluginManifest(filepath.Join(home, "plugins/cache/market/selected/1.0.0"), "selected") {
					t.Fatal("native cache not snapshotted")
				}
				h.ApplySessionPlugins(protocol.ApplySessionPlugins{SessionID: "session", RequestID: "remove-native", Remove: []pluginspec.Entry{entry}})
				waitCodexPluginResult(t, sender, "remove-native", "applied")
			} else if len(records[0].Plugins) != 0 {
				t.Fatal("failed replacement changed selection")
			}
		})
	}
}

func TestCodexMarketplaceStartupAndPolicy(t *testing.T) {
	h, d, sender, _, source := codexMarketplaceHost(t)
	entry := pluginspec.Entry{Marketplace: "codex:market", Plugin: "selected"}
	// Launch-time preparation uses the same native installer and snapshot as a
	// new account session; the sandbox gets this completed snapshot mounted.
	home, config, res := h.cfg.Plugins.prepareCodex(t.Context(), "next-session", []pluginspec.Entry{entry}, source)
	defer h.cfg.Plugins.releaseCodex("next-session")
	if res.Installed != 1 || home == "" || config["plugins"] == nil {
		t.Fatalf("startup native install: %+v", res)
	}
	// A revoked allow-list must prevent even a previously approved request.
	h.cfg.Plugins.allow, _ = pluginspec.ParseAllowlist("other/plugins")
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{SessionID: "session", RequestID: "denied", Plugins: []pluginspec.Entry{entry}})
	pluginResultFor(t, sender, "denied", "failed")
	d.mu.Lock()
	queued := len(d.pendingPlugins)
	d.mu.Unlock()
	if queued != 0 {
		t.Fatal("disallowed native install queued")
	}
}

func TestCodexMarketplaceRemoteCatalogAndSanitization(t *testing.T) {
	raw := []byte(`{"marketplaces":[{"name":"market","path":null,"plugins":[{"name":"selected","source":{"type":"remote"},"interface":{"shortDescription":"Native catalog plugin"}},{"name":"denied","source":{"type":"remote"},"availability":"DISABLED_BY_ADMIN"},{"name":"local","source":{"type":"local"}}]},{"name":"path-market","path":"/untrusted","plugins":[{"name":"hidden","source":{"type":"remote"}}]}]}`)
	entry := pluginspec.Entry{Marketplace: "codex:market", Plugin: "selected"}
	out := appendCodexRemoteCatalog(extCatalog{}, raw, []pluginspec.Entry{entry})
	markets, plugins, _ := protocol.SanitizePluginCatalog(out.Marketplaces, out.Plugins)
	if len(markets) != 1 || markets[0].Source != "codex:market" || len(plugins) != 2 || !plugins[0].Managed || !plugins[0].Enabled || plugins[1].InstallReason == "" {
		t.Fatalf("native catalog %+v %+v", markets, plugins)
	}
	// Read the native catalog through a fake app-server, not a browser payload.
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	a := &codexApp{d: d, ctx: t.Context(), pending: map[int]chan codexRPC{}, done: make(chan struct{})}
	a.in = &pluginInventoryWriter{app: a, reply: codexRPC{Result: json.RawMessage(raw)}}
	d.app = a
	c := &codexExtensions{d: d, loaded: []pluginspec.Entry{entry}}
	got, err := c.Catalog(t.Context())
	if err != nil || len(got.Marketplaces) != 1 || got.Marketplaces[0].Source != "codex:market" {
		t.Fatalf("native API catalog %+v %v", got, err)
	}
}

func TestCodexMarketplaceFailedInstallKeepsPreviousHome(t *testing.T) {
	h, d, sender, em, _ := codexMarketplaceHost(t)
	// Make the fake native CLI fail after opening its disposable login, before
	// returning install metadata. Its diagnostics must not enter owner results.
	bin := strings.Split(os.Getenv("PATH"), ":")[0]
	path := filepath.Join(bin, "codex")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), ` path=os.path.join(home,"plugins","cache","market","selected","1.0.0")`, ` print("private native install failure");sys.exit(3)`, 1))
	if err := os.WriteFile(path, script, 0755); err != nil {
		t.Fatal(err)
	}
	oldHome := t.TempDir()
	d.CodexHome = oldHome
	stop := startTestCodexDriver(t.Context(), t, d)
	defer stop()
	d.Enqueue("before", "chat")
	waitUntil(t, "first turn", func() bool { return countKind(em, "turn_done") == 1 })
	h.ApplySessionPlugins(protocol.ApplySessionPlugins{SessionID: "session", RequestID: "bad-install", Plugins: []pluginspec.Entry{{Marketplace: "codex:market", Plugin: "selected"}}})
	waitCodexPluginResult(t, sender, "bad-install", "failed")
	d.mu.Lock()
	sameHome := d.CodexHome == oldHome
	d.mu.Unlock()
	if !sameHome {
		t.Fatal("failed install replaced the live home")
	}
	d.Enqueue("after", "chat")
	waitUntil(t, "turn after failed install", func() bool { return countKind(em, "turn_done") == 2 })
	records := listAgentRecords(h.cfg.RecordsDir)
	if len(records) != 1 || strings.Contains(records[0].PluginRequests["bad-install"].Error, "private native") {
		t.Fatal("private install diagnostics leaked")
	}
}

func TestCodexMarketplaceNewHostSessionLoadsSavedSelection(t *testing.T) {
	h, _, sender, _, source := codexMarketplaceHost(t)
	t.Setenv("CODEX_HOME", source)
	root := t.TempDir()
	h.cfg.ReposRoot = root
	h.cfg.HomeDir = h.cfg.Plugins.home
	entry := pluginspec.Entry{Marketplace: "codex:market", Plugin: "selected"}
	h.Spawn(protocol.SpawnSession{SessionID: "next-account-session", ProjectPath: root, Kind: "agent", Engine: "codex", InitialPrompt: "Use the saved selection", Plugins: []pluginspec.Entry{entry}})
	defer h.Kill("next-account-session")
	sender.waitForKind(t, "turn_done")
	sess := h.get("next-account-session")
	if sess == nil || len(sess.pluginSpecs) != 1 || sess.pluginSpecs[0] != entry {
		t.Fatal("new session lost saved native selection")
	}
	driver, ok := sess.loop.(*codexDriver)
	if !ok || driver.CodexHome == "" || driver.CodexHome == source {
		t.Fatal("new native plugin session has no private home")
	}
	if !codexPluginManifest(filepath.Join(driver.CodexHome, "plugins/cache/market/selected/1.0.0"), "selected") {
		t.Fatal("new session did not load native cache")
	}
}

func TestCodexMarketplaceInstallerRefusesUnsafeLoginFiles(t *testing.T) {
	for _, mode := range []string{"symlink", "public"} {
		t.Run(mode, func(t *testing.T) {
			source, home := t.TempDir(), t.TempDir()
			auth := filepath.Join(source, "auth.json")
			if mode == "symlink" {
				target := filepath.Join(t.TempDir(), "login.json")
				writeTestFile(t, target, `{"native_fixture":true}`)
				if err := os.Symlink(target, auth); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(auth, []byte(`{"native_fixture":true}`), 0644); err != nil {
				t.Fatal(err)
			}
			if cleanup, err := copyCodexInstallerAuth(source, home); err == nil {
				cleanup()
				t.Fatal("unsafe native login accepted")
			}
			if _, err := os.Lstat(filepath.Join(home, "auth.json")); !os.IsNotExist(err) {
				t.Fatal("unsafe auth was copied")
			}
		})
	}
}
