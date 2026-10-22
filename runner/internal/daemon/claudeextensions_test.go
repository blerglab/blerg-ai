package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if s, ok := v.(string); ok {
		raw = []byte(s)
	} else {
		var err error
		if raw, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixtureMarketplace writes a marketplace clone under dir and records it as known.
func fixtureMarketplace(t *testing.T, dir, name string, source map[string]string, plugins []any) {
	t.Helper()
	loc := filepath.Join(dir, "plugins", "marketplaces", name)
	writeJSONFile(t, filepath.Join(loc, ".claude-plugin", "marketplace.json"), map[string]any{"name": name, "description": "Fixture " + name, "plugins": plugins})
	known := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(dir, "plugins", "known_marketplaces.json")); err == nil {
		_ = json.Unmarshal(raw, &known)
	}
	known[name] = map[string]any{"source": source, "installLocation": loc}
	writeJSONFile(t, filepath.Join(dir, "plugins", "known_marketplaces.json"), known)
}

func TestReadClaudeCatalogFromMarketplaceDir(t *testing.T) {
	person := t.TempDir()
	workshop := t.TempDir()
	cwd := t.TempDir()
	fixtureMarketplace(t, person, "fixture-market", map[string]string{"source": "github", "repo": "example-org/fixture-plugins"}, []any{
		map[string]any{"name": "release-notes", "description": "Writes release notes.\nTwo lines\u202e.", "version": "1.2.0", "author": map[string]any{"name": "Example Org"}, "category": "docs"},
		map[string]any{"name": "lint-helper", "description": strings.Repeat("x", 1000), "author": "Solo Dev"},
		map[string]any{"name": "../escape", "description": "bad name"},
		map[string]any{"name": "Upper", "description": "bad name"},
		map[string]any{"name": 7},
		map[string]any{"name": "project-only"},
		map[string]any{"name": "blerg-loaded"},
	})
	// A local marketplace (no GitHub source): listed, but with no source to install from.
	fixtureMarketplace(t, person, "local-market", map[string]string{"source": "directory", "path": "/somewhere"}, []any{map[string]any{"name": "local-one"}})
	// The same name in the workshop: the person's wins. Another one only the workshop knows.
	fixtureMarketplace(t, workshop, "fixture-market", map[string]string{"source": "github", "repo": "example-org/other"}, []any{map[string]any{"name": "shadowed"}})
	fixtureMarketplace(t, workshop, "blerg-added", map[string]string{"source": "git", "url": "https://github.com/example-org/added.git"}, []any{map[string]any{"name": "added-one"}})

	writeJSONFile(t, filepath.Join(person, "plugins", "installed_plugins.json"), map[string]any{"version": 2, "plugins": map[string]any{
		"release-notes@fixture-market": []any{map[string]any{"scope": "user", "installPath": "/x"}},
		"lint-helper@fixture-market":   []any{map[string]any{"scope": "project", "projectPath": "/another/project"}},
		"project-only@fixture-market":  []any{map[string]any{"scope": "project", "projectPath": cwd}},
	}})
	writeJSONFile(t, filepath.Join(person, "settings.json"), map[string]any{"enabledPlugins": map[string]any{"release-notes@fixture-market": true, "project-only@fixture-market": true}})
	writeJSONFile(t, filepath.Join(cwd, ".claude", "settings.local.json"), map[string]any{"enabledPlugins": map[string]any{"project-only@fixture-market": false}})

	got := readClaudeCatalog(context.Background(), []string{person, workshop}, person, cwd,
		[]pluginspec.Entry{{Marketplace: "Example-Org/fixture-plugins", Plugin: "blerg-loaded"}})
	markets, plugins, _ := protocol.SanitizePluginCatalog(got.Marketplaces, got.Plugins)
	src := map[string]string{}
	for _, m := range markets {
		src[m.Name] = m.Source
	}
	if len(markets) != 3 || src["fixture-market"] != "example-org/fixture-plugins" || src["local-market"] != "" || src["blerg-added"] != "example-org/added" {
		t.Fatalf("marketplaces = %+v", markets)
	}
	by := map[string]protocol.CatalogPlugin{}
	for _, p := range plugins {
		by[p.Name] = p
	}
	if _, ok := by["shadowed"]; ok {
		t.Fatal("a marketplace the person knows was read again from the workshop")
	}
	for _, bad := range []string{"../escape", "Upper", ""} {
		if _, ok := by[bad]; ok {
			t.Fatalf("invalid plugin name %q listed", bad)
		}
	}
	rn := by["release-notes"]
	if rn.Marketplace != "fixture-market" || rn.Version != "1.2.0" || rn.Author != "Example Org" || rn.Category != "docs" || !rn.Installed || !rn.Enabled {
		t.Fatalf("release-notes = %+v", rn)
	}
	if strings.ContainsAny(rn.Description, "\n\u202e") || rn.Description != "Writes release notes. Two lines." {
		t.Fatalf("description not plain text: %q", rn.Description)
	}
	if lh := by["lint-helper"]; lh.Installed || lh.Author != "Solo Dev" || len([]rune(lh.Description)) != protocol.CatalogMaxDescription+1 {
		t.Fatalf("lint-helper = %+v (%d runes)", lh, len([]rune(lh.Description)))
	}
	// The workspace's own settings are not read (the session can write them): only the engine home's.
	if po := by["project-only"]; !po.Installed || !po.Enabled {
		t.Fatalf("project-only: the workspace's settings.local.json must not be read: %+v", po)
	}
	if bl := by["blerg-loaded"]; !bl.Installed || !bl.Enabled {
		t.Fatalf("a plugin Blerg loaded into the session: %+v", bl)
	}
	if _, ok := by["added-one"]; !ok {
		t.Fatal("the workshop's own marketplace is missing")
	}
}

func TestReadClaudeCatalogRefusesManifestsOutsideTheConfigDir(t *testing.T) {
	person := t.TempDir()
	outside := t.TempDir()
	writeJSONFile(t, filepath.Join(outside, ".claude-plugin", "marketplace.json"), map[string]any{"plugins": []any{map[string]any{"name": "outside"}}})
	// installLocation pointing elsewhere, and a clone that is a symlink out of the dir.
	writeJSONFile(t, filepath.Join(person, "plugins", "known_marketplaces.json"), map[string]any{
		"elsewhere": map[string]any{"source": map[string]string{"source": "github", "repo": "a/b"}, "installLocation": outside},
		"linked":    map[string]any{"source": map[string]string{"source": "github", "repo": "a/c"}},
	})
	if err := os.MkdirAll(filepath.Join(person, "plugins", "marketplaces"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(person, "plugins", "marketplaces", "linked")); err != nil {
		t.Fatal(err)
	}
	got := readClaudeCatalog(context.Background(), []string{person}, person, "", nil)
	if len(got.Marketplaces) != 0 || len(got.Plugins) != 0 {
		t.Fatalf("read a manifest outside the config dir: %+v", got)
	}
}

func TestReadClaudeCatalogBounds(t *testing.T) {
	person := t.TempDir()
	plugins := make([]any, 0, protocol.CatalogMaxPlugins+5)
	for i := range protocol.CatalogMaxPlugins + 5 {
		plugins = append(plugins, map[string]any{"name": fmt.Sprintf("p%d", i)})
	}
	fixtureMarketplace(t, person, "big", map[string]string{"source": "github", "repo": "a/big"}, plugins)
	got := readClaudeCatalog(context.Background(), []string{person}, person, "", nil)
	if !got.Truncated || len(got.Plugins) != protocol.CatalogMaxPlugins {
		t.Fatalf("truncated=%t plugins=%d", got.Truncated, len(got.Plugins))
	}
	// A manifest over the size cap is not read at all.
	huge := t.TempDir()
	loc := filepath.Join(huge, "plugins", "marketplaces", "huge")
	writeJSONFile(t, filepath.Join(huge, "plugins", "known_marketplaces.json"), map[string]any{"huge": map[string]any{"source": map[string]string{"source": "github", "repo": "a/huge"}, "installLocation": loc}})
	writeJSONFile(t, filepath.Join(loc, ".claude-plugin", "marketplace.json"), `{"plugins":[{"name":"x","description":"`+strings.Repeat("y", ccMarketplaceManifest)+`"}]}`)
	if got := readClaudeCatalog(context.Background(), []string{huge}, huge, "", nil); len(got.Marketplaces) != 0 {
		t.Fatalf("an oversize manifest was read: %d marketplaces", len(got.Marketplaces))
	}
}

func TestClaudeUserConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got := claudeUserConfigDir([]string{"CLAUDE_CONFIG_DIR=/cfg"}, "/home/x"); got != "/cfg" {
		t.Fatal(got)
	}
	if got := claudeUserConfigDir([]string{"CLAUDE_CONFIG_DIR=relative"}, "/home/x"); got != "/home/x/.claude" {
		t.Fatal(got)
	}
}

// signInFixture runs the provider's MCPSignIn against a scripted Claude Code process: the status
// answer, then (when asked) the mcp_authenticate answer.
func signInFixture(t *testing.T, server, status, authReply string) (extSignIn, []string, error) {
	t.Helper()
	d, p := claudeMCPFixture(t)
	ext := &claudeExtensions{d: d}
	type res struct {
		out extSignIn
		err error
	}
	done := make(chan res, 1)
	go func() { o, e := ext.MCPSignIn(context.Background(), server); done <- res{o, e} }()
	var subtypes []string
	for {
		select {
		case r := <-done:
			return r.out, subtypes, r.err
		case line := <-p.writeCh:
			var req struct {
				ID      string `json:"request_id"`
				Request struct {
					Subtype    string `json:"subtype"`
					ServerName string `json:"serverName"`
				} `json:"request"`
			}
			if err := json.Unmarshal(line, &req); err != nil {
				t.Fatal(err)
			}
			subtypes = append(subtypes, req.Request.Subtype)
			switch req.Request.Subtype {
			case "mcp_status":
				claudeMCPResponse(d, p, req.ID, status)
			case "mcp_authenticate":
				if req.Request.ServerName != server {
					t.Fatalf("authenticate for %q", req.Request.ServerName)
				}
				if authReply == "" {
					d.onLine(p, []byte(`{"type":"control_response","response":{"subtype":"error","request_id":"`+req.ID+`","error":"fixture-private detail"}}`))
				} else {
					claudeMCPResponse(d, p, req.ID, authReply)
				}
			}
		case <-time.After(15 * time.Second):
			t.Fatal("sign-in did not finish")
		}
	}
}

func TestClaudeMCPSignInStartsTheEnginesOAuth(t *testing.T) {
	status := `{"mcpServers":[{"name":"tracker","status":"needs-auth","scope":"user"},{"name":"claude.ai Gmail","status":"needs-auth","scope":"claudeai"},{"name":"ok","status":"connected","scope":"user"}]}`
	out, subs, err := signInFixture(t, "tracker", status, `{"authUrl":"https://auth.example/authorize?state=s","requiresUserAction":true,"callbackExpected":true,"redirectScheme":"localhost","callbackPort":43210,"state":"s"}`)
	if err != nil || out.AuthURL != "https://auth.example/authorize?state=s" || !out.RequiresUserAction || !out.CallbackExpected {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if strings.Join(subs, ",") != "mcp_status,mcp_authenticate" {
		t.Fatalf("control requests %v", subs)
	}
	// A claude.ai connector is connected on claude.ai; a connected or unknown server needs nothing.
	for server, want := range map[string]extCode{"claude.ai Gmail": extConnector, "ok": extNotFound, "missing": extNotFound} {
		_, subs, err := signInFixture(t, server, status, "")
		if !errors.Is(err, want) || len(subs) != 1 {
			t.Fatalf("%s: err=%v requests=%v", server, err, subs)
		}
	}
	// The engine refusing is "refused", and its text is not passed on.
	if _, _, err := signInFixture(t, "tracker", status, ""); !errors.Is(err, extRefused) {
		t.Fatalf("refusal: %v", err)
	}
}

func TestMCPSignInResultRefusesNonHTTPSURLs(t *testing.T) {
	for _, u := range []string{"http://auth.example/a", "javascript:alert(1)", "https://user:pw@auth.example/", "https:///nohost", "https://auth.example/a b"} {
		if protocol.ValidAuthURL(u) {
			t.Fatalf("%q accepted", u)
		}
	}
	if !protocol.ValidAuthURL("https://auth.example/authorize?client_id=x&state=y") {
		t.Fatal("a plain https URL refused")
	}
}

func TestMCPTrackerWatchReadsAfterSettling(t *testing.T) {
	tr, src, em := mcpTestTracker(t, 0)
	src.set("needs-auth") // a final state: the inventory settles and reads stop
	waitFor(t, func() bool { return mcpLatest(em).Settled })
	src.mu.Lock()
	before := src.calls
	src.mu.Unlock()
	src.set("connected") // the person signed in; without a watch nobody would see it
	time.Sleep(100 * time.Millisecond)
	if got := mcpLatest(em); len(got.Servers) != 1 || got.Servers[0].Status != "needs-auth" {
		t.Fatalf("a settled tracker kept reading: %+v", got)
	}
	src.mu.Lock()
	if src.calls != before {
		t.Fatalf("reads after settling: %d → %d", before, src.calls)
	}
	src.mu.Unlock()
	tr.watch(time.Second)
	waitFor(t, func() bool {
		got := mcpLatest(em)
		return len(got.Servers) == 1 && got.Servers[0].Status == "connected"
	})
}

// I2: a FIFO where the catalog reads a file (the engine home's settings, or a manifest) is refused
// at once, never opened for a blocking read.
func TestCatalogReadsNeverBlockOnAFIFO(t *testing.T) {
	person := t.TempDir()
	cwd := t.TempDir()
	fixtureMarketplace(t, person, "fixture-market", map[string]string{"source": "github", "repo": "a/b"}, []any{map[string]any{"name": "one"}})
	for _, p := range []string{
		filepath.Join(person, "settings.json"),
		filepath.Join(person, "plugins", "installed_plugins.json"),
		filepath.Join(cwd, ".claude", "settings.local.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A FIFO behind a symlink too.
	if err := os.Symlink(filepath.Join(person, "settings.json"), filepath.Join(person, "linked.json")); err != nil {
		t.Fatal(err)
	}
	done := make(chan extCatalog, 1)
	go func() { done <- readClaudeCatalog(context.Background(), []string{person}, person, cwd, nil) }()
	select {
	case got := <-done:
		if len(got.Plugins) != 1 {
			t.Fatalf("catalog %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the catalog read blocked on a FIFO")
	}
	for _, p := range []string{filepath.Join(person, "settings.json"), filepath.Join(person, "linked.json")} {
		start := time.Now()
		if _, err := readCatalogFile(p, 1<<20); err == nil || time.Since(start) > time.Second {
			t.Fatalf("%s: err=%v after %v", p, err, time.Since(start))
		}
	}
}

// I2: a request stuck in a call that cannot be cancelled frees its slot at its deadline.
func TestExtSlotIsFreedAtTheDeadline(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	stuck := make(chan struct{})
	defer close(stuck)
	_, done := withSlotDeadline(sem, 50*time.Millisecond, func() int { <-stuck; return 1 })
	if done {
		t.Fatal("a stuck request reported done")
	}
	select {
	case sem <- struct{}{}:
	default:
		t.Fatal("the slot is still held after the deadline")
	}
	<-sem
	sem <- struct{}{}
	if res, done := withSlotDeadline(sem, time.Second, func() int { return 7 }); !done || res != 7 || len(sem) != 0 {
		t.Fatalf("res=%d done=%t held=%d", res, done, len(sem))
	}
}

// I1: Blerg starts a sign-in only for a server from the person's own user settings.
func TestClaudeMCPSignInOnlyForUserScope(t *testing.T) {
	status := `{"mcpServers":[{"name":"proj","status":"needs-auth","scope":"project"},{"name":"loc","status":"needs-auth","scope":"local"},{"name":"dyn","status":"needs-auth","scope":"dynamic"},{"name":"plug","status":"needs-auth","scope":"plugin"},{"name":"odd","status":"needs-auth","scope":"weird"},{"name":"mine","status":"needs-auth","scope":"user"}]}`
	for _, server := range []string{"proj", "loc", "dyn", "plug", "odd"} {
		_, subs, err := signInFixture(t, server, status, "")
		if !errors.Is(err, extUntrusted) || strings.Join(subs, ",") != "mcp_status" {
			t.Fatalf("%s: err=%v requests=%v", server, err, subs)
		}
	}
	out, _, err := signInFixture(t, "mine", status, `{"authUrl":"https://auth.example/a","requiresUserAction":true,"callbackExpected":true}`)
	if err != nil || out.Source != "user" || out.AuthURL != "https://auth.example/a" {
		t.Fatalf("user scope: %+v %v", out, err)
	}
}

// M5: one sign-in per server per session; a started one is reused, a failed one is not.
func TestSignInTableOnePerServer(t *testing.T) {
	tab := &signInTable{m: map[string]*signInEntry{}}
	now := time.Now()
	release, cached, ok := tab.begin("s\x00a", now)
	if !ok || cached != nil {
		t.Fatal("first begin")
	}
	if _, _, ok := tab.begin("s\x00a", now); ok {
		t.Fatal("a second sign-in started while the first was starting")
	}
	if _, cached, ok := tab.begin("s\x00b", now); !ok || cached != nil {
		t.Fatal("another server is independent")
	}
	release(protocol.MCPSignInResult{OK: true, AuthURL: "https://auth.example/a"})
	if _, cached, ok := tab.begin("s\x00a", now.Add(time.Minute)); !ok || cached == nil || cached.AuthURL != "https://auth.example/a" {
		t.Fatal("a recent sign-in was not reused")
	}
	if _, cached, ok := tab.begin("s\x00a", time.Now().Add(signInReuse+time.Second)); !ok || cached != nil {
		t.Fatal("an expired sign-in was reused")
	}
	r2, _, _ := tab.begin("s\x00c", now)
	r2(protocol.MCPSignInResult{ErrorCode: "refused"})
	if _, cached, ok := tab.begin("s\x00c", now); !ok || cached != nil {
		t.Fatal("a failed sign-in was cached")
	}
}

// M1: the frame stays under the server's cap whatever the catalog holds.
func TestCatalogEncodedBudget(t *testing.T) {
	plugins := make([]protocol.CatalogPlugin, protocol.CatalogMaxPlugins)
	for i := range plugins {
		plugins[i] = protocol.CatalogPlugin{Name: fmt.Sprintf("p%d", i), Marketplace: "m",
			Description: strings.Repeat("<", protocol.CatalogMaxDescription), Version: strings.Repeat("&", protocol.CatalogMaxShort),
			Author: strings.Repeat(">", protocol.CatalogMaxShort), Category: strings.Repeat("<", protocol.CatalogMaxShort)}
	}
	kept, cut := protocol.BudgetPluginCatalog(plugins, protocol.CatalogEncodedBudget)
	raw, _ := json.Marshal(protocol.PluginCatalogResult{Type: "plugin_catalog_result", RequestID: strings.Repeat("r", 128), SessionID: strings.Repeat("s", 128), OK: true,
		Marketplaces: make([]protocol.CatalogMarketplace, protocol.CatalogMaxMarketplaces), Plugins: kept, Truncated: true})
	if !cut || len(kept) == 0 || len(kept) >= len(plugins) || len(raw) >= protocol.CatalogResultMaxFrame {
		t.Fatalf("cut=%t kept=%d frame=%d", cut, len(kept), len(raw))
	}
}

// R3: a spawn's custom variables can never move an engine's home.
func TestExtraEnvCannotMoveEngineHomes(t *testing.T) {
	for _, k := range []string{"HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME"} {
		if validExtraEnvKey(k) {
			t.Fatalf("%s accepted", k)
		}
	}
	if !validExtraEnvKey("MY_PROJECT_SETTING") {
		t.Fatal("an ordinary variable refused")
	}
}

// R5: an engine-home settings file symlinked by a dotfile manager is still read.
func TestClaudeHomeSettingsSymlinkIsFollowed(t *testing.T) {
	person := t.TempDir()
	dots := t.TempDir()
	writeJSONFile(t, filepath.Join(dots, "settings.json"), map[string]any{"enabledPlugins": map[string]any{"a@m": true}})
	writeJSONFile(t, filepath.Join(person, "plugins", "installed_plugins.json"), map[string]any{"version": 2, "plugins": map[string]any{"a@m": []any{map[string]any{"scope": "user"}}}})
	if err := os.Symlink(filepath.Join(dots, "settings.json"), filepath.Join(person, "settings.json")); err != nil {
		t.Fatal(err)
	}
	installed, enabled := claudeInstallState(person, "")
	if !installed["a@m"] || !enabled["a@m"] {
		t.Fatalf("installed=%v enabled=%v", installed, enabled)
	}
}
