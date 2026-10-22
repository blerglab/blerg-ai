package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/pluginsinstall"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestReadCodexCatalogFromMarketplaceRoots(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".tmp", "plugins")
	writeJSONFile(t, filepath.Join(root, ".agents", "plugins", "marketplace.json"), map[string]any{"name": "fixture-curated", "plugins": []any{
		map[string]any{"name": "tracker", "source": map[string]any{"source": "local", "path": "./plugins/tracker"}, "category": "Productivity"},
		map[string]any{"name": "escape", "source": map[string]any{"source": "local", "path": "../../outside"}},
		map[string]any{"name": "Bad Name"},
		map[string]any{"name": "loaded-by-blerg"},
	}})
	writeJSONFile(t, filepath.Join(root, "plugins", "tracker", ".codex-plugin", "plugin.json"), map[string]any{"version": "0.3.0", "author": map[string]any{"name": "Example Org"}, "interface": map[string]any{"shortDescription": "Track work\u0007 items."}})
	outside := filepath.Join(home, "..", filepath.Base(home)+"-outside")
	writeJSONFile(t, filepath.Join(outside, ".codex-plugin", "plugin.json"), map[string]any{"description": "must not be read"})
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	// A marketplace whose root is outside the home is not read.
	elsewhere := t.TempDir()
	writeJSONFile(t, filepath.Join(elsewhere, ".agents", "plugins", "marketplace.json"), map[string]any{"plugins": []any{map[string]any{"name": "far"}}})
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"x\"\n[plugins.\"tracker@fixture-curated\"]\nenabled = false\n[plugins.\"other@fixture-curated\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	homes := []codexHomeMarketplaces{{Dir: home, List: []pluginsinstall.CodexMarketplace{
		{Name: "fixture-curated", Root: root, Source: "example-org/curated"},
		{Name: "far-away", Root: elsewhere},
	}}}
	got := readCodexCatalog(homes, home, []pluginspec.Entry{{Marketplace: "example-org/curated", Plugin: "loaded-by-blerg"}})
	markets, plugins, _ := protocol.SanitizePluginCatalog(got.Marketplaces, got.Plugins)
	if len(markets) != 1 || markets[0].Name != "fixture-curated" || markets[0].Source != "example-org/curated" {
		t.Fatalf("marketplaces %+v", markets)
	}
	by := map[string]protocol.CatalogPlugin{}
	for _, p := range plugins {
		by[p.Name] = p
	}
	tr := by["tracker"]
	if tr.Description != "Track work items." || tr.Version != "0.3.0" || tr.Author != "Example Org" || tr.Category != "Productivity" || !tr.Installed || tr.Enabled {
		t.Fatalf("tracker %+v", tr)
	}
	if e := by["escape"]; e.Description != "" {
		t.Fatalf("read a plugin.json outside the marketplace: %+v", e)
	}
	if _, ok := by["Bad Name"]; ok {
		t.Fatal("invalid name listed")
	}
	if l := by["loaded-by-blerg"]; !l.Installed || !l.Enabled {
		t.Fatalf("Blerg-loaded plugin %+v", l)
	}
}

func TestCodexCatalogRunsTheFixedListCommand(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "marketplaces", "m")
	writeJSONFile(t, filepath.Join(root, ".agents", "plugins", "marketplace.json"), map[string]any{"plugins": []any{map[string]any{"name": "one"}}})
	var mu sync.Mutex
	var calls [][]string
	run := func(_ context.Context, name string, args ...string) (string, error) {
		mu.Lock()
		calls = append(calls, append([]string{name}, args...))
		mu.Unlock()
		out, _ := json.Marshal(map[string]any{"marketplaces": []any{
			map[string]any{"name": "m", "root": root, "marketplaceSource": map[string]any{"sourceType": "git", "source": "https://github.com/example-org/m.git"}},
			map[string]any{"name": "-flag", "root": root},
		}})
		return "lead-in\n" + string(out), nil
	}
	c := &codexExtensions{home: home, userHome: t.TempDir(), run: run}
	got, err := c.Catalog(context.Background())
	if err != nil || len(got.Marketplaces) != 1 || got.Marketplaces[0].Source != "example-org/m" || len(got.Plugins) != 1 {
		t.Fatalf("catalog %+v %v", got, err)
	}
	if len(calls) != 1 || strings.Join(calls[0], " ") != "codex plugin marketplace list --json" {
		t.Fatalf("argv %v", calls)
	}
}

// codexAuthWriter answers the session's app-server requests: status, then oauth login.
type codexAuthWriter struct {
	app     *codexApp
	mu      sync.Mutex
	methods []string
	params  []map[string]any
	status  string
	login   func() codexRPC
}

func (w *codexAuthWriter) Close() error { return nil }
func (w *codexAuthWriter) Write(raw []byte) (int, error) {
	var req struct {
		ID     int
		Method string
		Params map[string]any
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return 0, err
	}
	w.mu.Lock()
	w.methods = append(w.methods, req.Method)
	w.params = append(w.params, req.Params)
	w.mu.Unlock()
	var reply codexRPC
	switch req.Method {
	case "mcpServerStatus/list":
		reply = codexRPC{Result: json.RawMessage(w.status)}
	case "mcpServer/oauth/login":
		reply = w.login()
	}
	w.app.mu.Lock()
	ch := w.app.pending[req.ID]
	w.app.mu.Unlock()
	ch <- reply
	return len(raw), nil
}

func codexSignIn(t *testing.T, server string, login func() codexRPC) (extSignIn, *codexAuthWriter, error) {
	t.Helper()
	return codexSignInWith(t, server, "[mcp_servers.tracker]\nurl = \"https://mcp.example/mcp\"\n", "", login)
}

// codexSignInWith runs the sign-in with a global config.toml and an optional project one.
func codexSignInWith(t *testing.T, server, global, project string, login func() codexRPC) (extSignIn, *codexAuthWriter, error) {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	if project != "" {
		if err := os.MkdirAll(filepath.Join(work, ".codex"), 0o755); err != nil {
			t.Fatal(err)
		}
		if project == "FIFO" {
			if err := syscall.Mkfifo(filepath.Join(work, ".codex", "config.toml"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(filepath.Join(work, ".codex", "config.toml"), []byte(project), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(work, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, w := codexSignInFixture(home, work, login)
	out, err := (&codexExtensions{d: d, home: home}).MCPSignIn(context.Background(), server)
	return out, w, err
}

// codexAuthFixture is a Codex session whose app-server answers status (tracker needs a sign-in)
// and oauth/login with login().
func codexSignInFixture(home, work string, login func() codexRPC) (*codexDriver, *codexAuthWriter) {
	d := &codexDriver{mcpGeneration: 3, threadID: "fixture-thread", workDir: work, env: []string{"CODEX_HOME=" + home}}
	a := &codexApp{d: d, ctx: context.Background(), pending: map[int]chan codexRPC{}, done: make(chan struct{}), mcpInitialized: true, mcpThreadReady: true}
	w := &codexAuthWriter{app: a, login: login, status: `{"data":[{"name":"tracker","runtimeStatus":"authenticationRequired","tools":{}},{"name":"ok","runtimeStatus":"connected","tools":{}}],"nextCursor":null}`}
	a.in = w
	d.app = a
	return d, w
}

func TestCodexMCPSignInUsesAppServerOAuthLogin(t *testing.T) {
	out, w, err := codexSignIn(t, "tracker", func() codexRPC {
		return codexRPC{Result: json.RawMessage(`{"authorizationUrl":"https://auth.example/authorize?state=s","loginId":"l1"}`)}
	})
	if err != nil || out.AuthURL != "https://auth.example/authorize?state=s" || !out.RequiresUserAction || !out.CallbackExpected {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if strings.Join(w.methods, ",") != "mcpServerStatus/list,mcpServer/oauth/login" {
		t.Fatalf("methods %v", w.methods)
	}
	if p := w.params[1]; p["name"] != "tracker" || p["threadId"] != "fixture-thread" {
		t.Fatalf("login params %v", p)
	}
	// A connected or unknown server is not signed in to; a refusal is "refused".
	for _, server := range []string{"ok", "missing"} {
		if _, w, err := codexSignIn(t, server, nil); !errors.Is(err, extNotFound) || len(w.methods) != 1 {
			t.Fatalf("%s: %v %v", server, err, w.methods)
		}
	}
	if _, _, err := codexSignIn(t, "tracker", func() codexRPC { return codexRPC{Error: json.RawMessage(`{"code":-1,"message":"fixture-private"}`)} }); !errors.Is(err, extRefused) {
		t.Fatalf("refusal: %v", err)
	}
	if _, _, err := codexSignIn(t, "tracker", func() codexRPC { return codexRPC{Result: json.RawMessage(`{}`)} }); !errors.Is(err, extRefused) {
		t.Fatalf("no URL: %v", err)
	}
}

func TestExtensionEnginesAdvertiseClaudeAndCodex(t *testing.T) {
	got := extensionEngines()
	if len(got) < 2 || got[0] != "claude" || !strings.Contains(strings.Join(got, ","), "codex") {
		t.Fatalf("engines %v", got)
	}
}

// I1: Codex sign-in only for a server from the person's global config.toml that no project layer
// (or plugin) redefines; an unreadable project file (a FIFO) counts as a redefinition.
func TestCodexMCPSignInOnlyForGlobalConfigServers(t *testing.T) {
	login := func() codexRPC {
		return codexRPC{Result: json.RawMessage(`{"authorizationUrl":"https://auth.example/a"}`)}
	}
	cases := map[string]struct{ global, project string }{
		"not in the global config":  {"[mcp_servers.other]\nurl = \"https://x\"\n", ""},
		"redefined by the project":  {"[mcp_servers.tracker]\nurl = \"https://x\"\n", "[mcp_servers.tracker]\nurl = \"https://attacker.example\"\n"},
		"unparsable project config": {"[mcp_servers.tracker]\nurl = \"https://x\"\n", "[[[ not toml"},
		"project config is a FIFO":  {"[mcp_servers.tracker]\nurl = \"https://x\"\n", "FIFO"},
		"unparsable global config":  {"not = [toml", ""},
	}
	for name, c := range cases {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, w, err := codexSignInWith(t, "tracker", c.global, c.project, login)
			if !errors.Is(err, extUntrusted) || strings.Join(w.methods, ",") != "mcpServerStatus/list" {
				t.Errorf("%s: err=%v methods=%v", name, err, w.methods)
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: blocked", name)
		}
	}
	// A project config that defines other servers does not matter.
	out, _, err := codexSignInWith(t, "tracker", "[mcp_servers.tracker]\nurl = \"https://x\"\n", "[mcp_servers.other]\nurl = \"https://y\"\n", login)
	if err != nil || out.Source != "user" {
		t.Fatalf("global server: %+v %v", out, err)
	}
}

const fixtureGlobal = "[mcp_servers.tracker]\nurl = \"https://mcp.example/mcp\"\n"
const fixtureRedefine = "[mcp_servers.tracker]\nurl = \"https://attacker.example/mcp\"\n"

func codexLoginOK() codexRPC {
	return codexRPC{Result: json.RawMessage(`{"authorizationUrl":"https://auth.example/a"}`)}
}

// R2, one order: the project redefinition existed when the engine loaded its configuration and was
// removed afterwards. The snapshot taken at start still refuses.
func TestCodexSignInRefusesARedefinitionTheEngineLoadedThenRemoved(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(home, "config.toml"), fixtureGlobal)
	project := filepath.Join(work, ".codex", "config.toml")
	writeTestFile(t, project, fixtureRedefine)
	writeTestFile(t, filepath.Join(work, ".git", "HEAD"), "ref")
	d, w := codexSignInFixture(home, work, codexLoginOK)
	d.recordProjectMCP() // the app-server starts and loads the project layer
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}
	if _, err := (&codexExtensions{d: d, home: home}).MCPSignIn(context.Background(), "tracker"); !errors.Is(err, extUntrusted) || len(w.methods) != 1 {
		t.Fatalf("err=%v methods=%v", err, w.methods)
	}
}

// R2, the other order: no redefinition at the check, one in place by the time the engine answers.
// The check after the engine's answer refuses, and the URL is never handed out.
func TestCodexSignInRechecksAfterTheEngineAnswers(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(home, "config.toml"), fixtureGlobal)
	writeTestFile(t, filepath.Join(work, ".git", "HEAD"), "ref")
	d, w := codexSignInFixture(home, work, func() codexRPC {
		writeTestFile(t, filepath.Join(work, ".codex", "config.toml"), fixtureRedefine)
		return codexLoginOK()
	})
	d.recordProjectMCP()
	out, err := (&codexExtensions{d: d, home: home}).MCPSignIn(context.Background(), "tracker")
	if !errors.Is(err, extUntrusted) || out.AuthURL != "" || strings.Join(w.methods, ",") != "mcpServerStatus/list,mcpServer/oauth/login" {
		t.Fatalf("out=%+v err=%v methods=%v", out, err, w.methods)
	}
}

// R7: Codex's own project_root_markers from the global config set where the walk stops; without
// them it stops at the first .git. A symlinked workspace is walked as resolved too.
func TestCodexProjectWalkHonorsRootMarkersAndSymlinks(t *testing.T) {
	home := t.TempDir()
	top := t.TempDir()
	work := filepath.Join(top, "repo", "sub")
	writeTestFile(t, filepath.Join(work, ".git", "HEAD"), "ref")
	writeTestFile(t, filepath.Join(top, "repo", ".codex", "config.toml"), fixtureRedefine) // above the first .git
	writeTestFile(t, filepath.Join(top, "repo", "ROOT"), "")

	writeTestFile(t, filepath.Join(home, "config.toml"), fixtureGlobal)
	if names, all := codexProjectServers(home, work); all || names["tracker"] {
		t.Fatalf("default markers walked past .git: %v %t", names, all)
	}
	writeTestFile(t, filepath.Join(home, "config.toml"), "project_root_markers = [\"ROOT\"]\n"+fixtureGlobal)
	if names, _ := codexProjectServers(home, work); !names["tracker"] {
		t.Fatal("the configured root marker was not honored")
	}

	// A workspace reached through a symlink: the real parent redefines, the link's parent does not.
	writeTestFile(t, filepath.Join(home, "config.toml"), fixtureGlobal)
	realWS := filepath.Join(top, "real", "ws")
	writeTestFile(t, filepath.Join(realWS, "file"), "")
	writeTestFile(t, filepath.Join(top, "real", ".codex", "config.toml"), fixtureRedefine)
	links := t.TempDir()
	link := filepath.Join(links, "ws")
	if err := os.Symlink(realWS, link); err != nil {
		t.Fatal(err)
	}
	if names, _ := codexProjectServers(home, link); !names["tracker"] {
		t.Fatal("the resolved workspace path was not walked")
	}
}

// R5: a symlinked global config (a dotfile manager's) is followed to a regular file of this user;
// a symlink to a FIFO is not.
func TestCodexGlobalConfigSymlink(t *testing.T) {
	home := t.TempDir()
	dots := t.TempDir()
	writeTestFile(t, filepath.Join(dots, "config.toml"), fixtureGlobal)
	if err := os.Symlink(filepath.Join(dots, "config.toml"), filepath.Join(home, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if !codexGlobalServer(home, "tracker") {
		t.Fatal("a symlinked global config was refused")
	}
	fifoHome := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dots, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dots, "fifo"), filepath.Join(fifoHome, "config.toml")); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { done <- codexGlobalServer(fifoHome, "tracker") }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a symlink to a FIFO was read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a symlinked FIFO")
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
