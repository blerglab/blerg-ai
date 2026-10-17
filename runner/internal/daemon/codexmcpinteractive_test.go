package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestCodexInteractiveHomePreservesLoginSettingsAndHistoryWithoutAmbientMCP(t *testing.T) {
	source := t.TempDir()
	work := t.TempDir()
	original := []byte(`model_provider="custom"
[model_providers.custom]
name="Private provider"
base_url="http://127.0.0.1:1234/v1"
experimental_http_headers={"X-Fixture"="private-test-value"}
[mcp_servers.ambient]
command="false"
[profiles.selected.mcp_servers.profileambient]
command="false"
[profiles.selected.plugins."ambient@market"]
enabled=true
[projects."/tmp"]
trust_level="trusted"
[plugins."ambient@market"]
enabled=true
`)
	if err := os.WriteFile(filepath.Join(source, "config.toml"), original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "AGENTS.md"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	home, err := prepareCodexInteractiveHome(context.Background(), source, work)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	got, err := os.ReadFile(filepath.Join(source, "config.toml"))
	if err != nil || string(got) != string(original) {
		t.Fatal("source settings changed")
	}
	for _, name := range []string{"auth.json", "sessions", "AGENTS.md"} {
		target, err := os.Readlink(filepath.Join(home, name))
		if err != nil || target != filepath.Join(source, name) {
			t.Fatalf("fixed live link missing: %s", name)
		}
	}
	body, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if toml.Unmarshal(body, &config) != nil {
		t.Fatal("invalid private TOML")
	}
	if _, ok := config["mcp_servers"]; ok {
		t.Fatal("ambient MCP retained")
	}
	if _, ok := config["plugins"]; ok {
		t.Fatal("ambient plugin retained")
	}
	profiles := config["profiles"].(map[string]any)
	if _, ok := profiles["selected"].(map[string]any)["mcp_servers"]; ok {
		t.Fatal("profile MCP retained")
	}
	if _, ok := profiles["selected"].(map[string]any)["plugins"]; ok {
		t.Fatal("profile ambient plugin retained")
	}
	provider := config["model_providers"].(map[string]any)["custom"].(map[string]any)
	if provider["experimental_http_headers"].(map[string]any)["X-Fixture"] != "private-test-value" {
		t.Fatal("provider configuration lost")
	}
	projects := config["projects"].(map[string]any)
	if len(projects) != 1 || projects[work].(map[string]any)["trust_level"] != "untrusted" {
		t.Fatal("project configuration trust leaked")
	}
	info, _ := os.Stat(filepath.Join(home, "config.toml"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("private settings permissions")
	}
}
func TestCodexInteractiveMalformedConfigDoesNotModifySource(t *testing.T) {
	source := t.TempDir()
	path := filepath.Join(source, "config.toml")
	original := []byte("[broken")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCodexInteractiveHome(context.Background(), source, t.TempDir()); err == nil {
		t.Fatal("malformed configuration silently ignored")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatal("source modified")
	}
}
func TestCodexAmbientMCPDisablesEveryResolvedServer(t *testing.T) {
	config := map[string]any{"model_reasoning_effort": "high"}
	if err := codexAmbientMCPOverrides(json.RawMessage(`{"config":{"mcp_servers":{"system":{"command":"fixture"},"profile":{"url":"http://127.0.0.1"}}}}`), config); err != nil {
		t.Fatal(err)
	}
	servers := config["mcp_servers"].(map[string]any)
	if len(servers) != 2 || config["model_reasoning_effort"] != "high" {
		t.Fatal("resolved config changed")
	}
	for _, v := range servers {
		if v.(map[string]any)["enabled"] != false {
			t.Fatal("MCP server not disabled")
		}
	}
}

func TestCodexEveryGrantedModeDisablesResolvedSystemMCPBeforeStartingThread(t *testing.T) {
	for _, restricted := range []bool{true, false} {
		name := "interactive"
		if restricted {
			name = "restricted"
		}
		t.Run(name, func(t *testing.T) {
			bin := fakeCodex(t)
			logPath := filepath.Join(bin, "calls.log")
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_LOG", logPath)
			t.Setenv("FAKE_CODEX_CONFIG", `{"mcp_servers":{"systemfixture":{"command":"false"}}}`)
			emitter := &collectEmitter{}
			driver := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: emitter})
			driver.RestrictedRuntime = &codexRestrictedRuntime{Restricted: restricted}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stopDriver := startTestCodexDriver(ctx, t, driver)
			defer stopDriver()
			driver.Enqueue("fixture", "chat")
			deadline := time.Now().Add(5 * time.Second)
			for !contains(emitter.kinds(), "turn_done") && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if !contains(emitter.kinds(), "turn_done") {
				t.Fatal("granted turn did not complete")
			}
			body, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			audited := false
			started := false
			for _, line := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(line, "config/read ") {
					audited = true
				}
				if strings.HasPrefix(line, "thread/start ") {
					if !audited {
						t.Fatal("thread started before ambient MCP audit")
					}
					var params struct {
						Config map[string]any `json:"config"`
					}
					if json.Unmarshal([]byte(strings.TrimPrefix(line, "thread/start ")), &params) != nil {
						t.Fatal("invalid thread params")
					}
					servers, ok := params.Config["mcp_servers"].(map[string]any)
					if !ok {
						t.Fatal("system MCP overrides absent")
					}
					server, ok := servers["systemfixture"].(map[string]any)
					if !ok || server["enabled"] != false {
						t.Fatal("system MCP was not disabled")
					}
					started = true
				}
			}
			if !started {
				t.Fatal("thread not started")
			}
		})
	}
}

func TestCodexPluginMCPSanitizerRejectsEscapingPluginDirectory(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	original := []byte(`{"mcpServers":{"ambient":{"command":"fixture"}}}`)
	path := filepath.Join(outside, "plugin.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "plugins")); err != nil {
		t.Fatal(err)
	}
	if err := sanitizeCodexPluginMCP(home); err == nil {
		t.Fatal("an escaping plugins directory must be rejected")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("sanitizer modified data outside its private home: %s, %v", got, err)
	}
}

func TestCodexInteractiveSanitizerHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	home, source := t.TempDir(), t.TempDir()
	if err := sanitizeCodexInteractiveHome(ctx, home, source, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation returned %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("canceled preparation wrote configuration: %v", err)
	}
}
