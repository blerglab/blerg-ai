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

func TestCodexPluginHomeSnapshotsOnlySelectedPluginWithoutChangingUserConfig(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "user-codex")
	cache := filepath.Join(base, "workshop")
	snapshot := filepath.Join(base, "session")
	for _, dir := range []string{original, filepath.Join(original, "sessions"), filepath.Join(cache, "plugins/cache/market/selected/1.0.0/skills/selected")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(original, "config.toml"), []byte("model = \"example-model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "auth.json"), []byte(`{"fixture":"authentication"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(cache, "plugins/cache/market/selected/1.0.0")
	if err := os.WriteFile(filepath.Join(plugin, "plugin.json"), []byte(`{"name":"selected"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(plugin, "skills/selected/SKILL.md")
	if err := os.WriteFile(skill, []byte("Original skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}}
	home, config, err := snapshotCodexPluginHome(snapshot, original, cache, entries, map[string]string{"selected@market": plugin})
	if err != nil {
		t.Fatal(err)
	}
	if home != snapshot || config["plugins"] == nil {
		t.Fatalf("home/config: %q %+v", home, config)
	}
	if err := os.WriteFile(skill, []byte("Updated workshop skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(snapshot, "plugins/cache/market/selected/1.0.0/skills/selected/SKILL.md"))
	if err != nil || string(got) != "Original skill" {
		t.Fatalf("snapshot mutated: %q %v", got, err)
	}
	configBytes, _ := os.ReadFile(filepath.Join(original, "config.toml"))
	if string(configBytes) != "model = \"example-model\"\n" {
		t.Fatal("user config changed")
	}
	// Native login refresh replaces auth.json atomically; the session follows the live path.
	if err := os.WriteFile(filepath.Join(original, "auth.json"), []byte(`{"fixture":"refreshed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(snapshot, "auth.json"))
	if string(got) != `{"fixture":"refreshed"}` {
		t.Fatal("session did not follow login refresh")
	}
	_, _, err = snapshotCodexPluginHome(snapshot, original, cache, entries, map[string]string{"selected@market": plugin})
	if err != nil {
		t.Fatalf("immutable session snapshot must survive resume: %v", err)
	}
}

func TestCodexPluginSandboxMountsPreserveFixedPathsAndRestrictedIsolation(t *testing.T) {
	args := sandboxRunArgs("fixture", "/workspace/project", "", nil, sandboxRunExtras{codexHome: "/private/session", codexSourceHome: "/private/login"})
	joined := strings.Join(args, " ")
	for _, want := range []string{"type=bind,src=/private/session,dst=/private/session", "type=bind,src=/private/login,dst=/private/login"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing native path mount %s", want)
		}
	}
	args = sandboxRunArgs("fixture", "/workspace/project", "", nil, sandboxRunExtras{codexHome: "/private/restricted", codexSourceHome: "/private/login", restrictedEngine: "codex"})
	if strings.Contains(strings.Join(args, " "), "/private/login") {
		t.Fatal("restricted sandbox mounted unrestricted user home")
	}
}

func TestCodexPluginLaunchConfigNeverExposesOtherRuntimeConfiguration(t *testing.T) {
	d := &codexDriver{RuntimeConfig: map[string]any{"plugins": map[string]any{"selected.with.dot@market.example": map[string]any{"enabled": true}}, "mcp_servers": map[string]any{"fixture": map[string]any{"token": "fixture-secret"}}}}
	args := strings.Join(d.launchArgs(), " ")
	if strings.Contains(args, "fixture-secret") || strings.Contains(args, "mcp_servers") {
		t.Fatal("runtime credentials exposed in argv")
	}
	if !strings.Contains(args, `"selected.with.dot@market.example"={enabled=true}`) {
		t.Fatal("native quoted plugin key changed its identity")
	}
	d.RestrictedRuntime = &codexRestrictedRuntime{Restricted: true}
	if len(d.launchArgs()) != 3 {
		t.Fatal("restricted runtime inherited plugin configuration")
	}
	d.RestrictedRuntime.Restricted = false
	if !strings.Contains(strings.Join(d.launchArgs(), " "), `"selected.with.dot@market.example"={enabled=true}`) {
		t.Fatal("interactive grant dropped selected plugin")
	}
}

func TestAgentHostCodexPluginLifecycleUsesPrivateHomeAndCleansUp(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	allow, _ := pluginspec.ParseAllowlist("example-org/plugins")
	w := newPluginWorkshop(filepath.Join(t.TempDir(), "plugins", "claude"), t.TempDir(), allow)
	source := filepath.Join(w.home, ".codex")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", source)
	if err := os.WriteFile(filepath.Join(source, "config.toml"), []byte("model=\"fixture\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(filepath.Dir(w.dir), "codex/workshop/plugins/cache/market/selected/1.0.0")
	w.run = func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 2 && args[1] == "marketplace" && args[2] == "list" {
			return `{"marketplaces":[{"name":"market","marketplaceSource":{"sourceType":"git","source":"example-org/plugins"}}]}`, nil
		}
		if len(args) > 1 && args[1] == "add" {
			if err := os.MkdirAll(installed, 0o700); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(installed, "plugin.json"), []byte(`{"name":"selected"}`), 0o600); err != nil {
				return "", err
			}
			raw, _ := json.Marshal(map[string]any{"pluginId": "selected@market", "name": "selected", "marketplaceName": "market", "installedPath": installed})
			return string(raw), nil
		}
		return "", nil
	}
	root := t.TempDir()
	host := NewAgentHost(&agentTestSender{}, AgentHostConfig{ReposRoot: root, HomeDir: w.home, Plugins: w})
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "codex-plugins", ProjectPath: root, Kind: "agent", Engine: "codex", Plugins: []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}}})
	sess := host.get("codex-plugins")
	if sess == nil {
		t.Fatal("native plugin session did not start")
	}
	driver, ok := sess.loop.(*codexDriver)
	if !ok || driver.CodexHome == "" || driver.CodexHome == source {
		t.Fatal("native plugin session did not get a private home")
	}
	nativeHome := driver.CodexHome
	if !sess.pluginHome || driver.RuntimeConfig["plugins"] == nil {
		t.Fatal("native plugin startup lost its selected configuration")
	}
	host.Kill("codex-plugins")
	if _, err := os.Stat(nativeHome); !os.IsNotExist(err) {
		t.Fatal("native plugin home not removed on Kill")
	}
	original, _ := os.ReadFile(filepath.Join(source, "config.toml"))
	if string(original) != "model=\"fixture\"\n" {
		t.Fatal("native plugin lifecycle changed user config")
	}
}

func TestCodexPluginMetadataCannotEscapeSnapshot(t *testing.T) {
	outside, snapshot := t.TempDir(), t.TempDir()
	fixture := filepath.Join(outside, "metadata.json")
	if err := os.WriteFile(fixture, []byte(`{"name":"selected"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.json", "blerg-plugins.json"} {
		if err := os.Symlink(fixture, filepath.Join(snapshot, name)); err != nil {
			t.Fatal(err)
		}
	}
	if codexPluginManifest(snapshot, "selected") {
		t.Fatal("a manifest outside the selected plugin cannot validate it")
	}
	if _, err := readCodexSnapshotMetadata(snapshot); err == nil {
		t.Fatal("snapshot metadata must not follow an escaping symlink")
	}
}

func TestCopyCodexPluginTreeRejectsLinksAndPreservesExecutableFiles(t *testing.T) {
	source, destination, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "hook"), []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyCodexPluginTree(source, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(destination, "hook"))
	if err != nil || info.Mode().Perm()&0100 == 0 {
		t.Fatalf("executable plugin hook lost its mode: %v, %v", info, err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := copyCodexPluginTree(source, t.TempDir()); err == nil {
		t.Fatal("plugin copies must reject symbolic links")
	}
}
