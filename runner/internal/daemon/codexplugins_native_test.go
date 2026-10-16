package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

// This is a local protocol test, with a loopback-only fake model provider. It never starts a
// external model turn, reads a person's login, or reaches a remote marketplace.
func TestNativeCodexPluginSnapshotLoadsAndResumesSameThread(t *testing.T) {
	t.Run("host", func(t *testing.T) { runNativeCodexPluginSnapshot(t, false) })
	t.Run("sandbox", func(t *testing.T) { runNativeCodexPluginSnapshot(t, true) })
	t.Run("interactivegrant", func(t *testing.T) { runNativeCodexPluginSnapshot(t, false, true) })
	t.Run("sandboxgrant", func(t *testing.T) { runNativeCodexPluginSnapshot(t, true, true) })
}
func runNativeCodexPluginSnapshot(t *testing.T, sandbox bool, interactive ...bool) {
	granted := len(interactive) > 0 && interactive[0]
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI not installed")
	}
	base := t.TempDir()
	source := filepath.Join(base, "user-codex")
	workshop := filepath.Join(base, "workshop")
	catalog := filepath.Join(base, "catalog")
	snapshot := filepath.Join(base, "session")
	for _, dir := range []string{source, workshop, filepath.Join(catalog, ".agents/plugins"), filepath.Join(catalog, "plugins/selected/skills/selected")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(catalog, ".agents/plugins/marketplace.json"), `{"name":"fixture-market","plugins":[{"name":"selected","source":{"source":"local","path":"./plugins/selected"},"policy":{"installation":"AVAILABLE","authentication":"ON_INSTALL"},"category":"Productivity"}]}`)
	write(filepath.Join(catalog, "plugins/selected/plugin.json"), `{"name":"selected","version":"1.0.0","description":"Local test fixture"}`)
	if err := os.MkdirAll(filepath.Join(catalog, "plugins/selected/.codex-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(catalog, "plugins/selected/.codex-plugin/plugin.json"), `{"name":"selected","version":"1.0.0","description":"Local test fixture","skills":"./skills/"}`)

	write(filepath.Join(catalog, "plugins/selected/skills/selected/SKILL.md"), "---\nname: selected\ndescription: Local fixture\n---\nFixture instructions.\n")
	modelRequests := make(chan []byte, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(request.Body, 4<<20))
		select {
		case modelRequests <- body:
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\"}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Fixture reply.\"}]}}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer provider.Close()
	config := fmt.Sprintf("model_provider = %q\n[model_providers.fixture]\nname = %q\nbase_url = %q\nwire_api = %q\n", "fixture", "fixture", provider.URL+"/v1", "responses")

	if granted {
		marker := filepath.Join(base, "ambient-started")
		config += fmt.Sprintf("\n[mcp_servers.ambient]\ncommand=\"sh\"\nargs=[\"-c\",%q]\n", "touch "+marker)
		if err := os.MkdirAll(filepath.Join(catalog, ".codex"), 0700); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(catalog, ".codex/config.toml"), fmt.Sprintf("[mcp_servers.project]\ncommand=\"sh\"\nargs=[\"-c\",%q]\n", "touch "+filepath.Join(base, "project-started")))
		write(filepath.Join(catalog, "plugins/selected/.mcp.json"), fmt.Sprintf(`{"mcpServers":{"pluginambient":{"command":"sh","args":["-c",%q]}}}`, "touch "+filepath.Join(base, "plugin-started")))
	}
	write(filepath.Join(source, "config.toml"), config)
	var selected string
	for _, args := range [][]string{{"plugin", "marketplace", "add", catalog}, {"plugin", "add", "selected@fixture-market", "--json"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = base
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + base, "CODEX_HOME=" + workshop}
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("local fixture install failed: %v %s", err, out)
		}

		if len(args) > 1 && args[1] == "add" {
			jsonStart := strings.Index(string(out), "{")
			if jsonStart < 0 {
				t.Fatal("local fixture installation returned no JSON")
			}
			var result struct {
				Path string `json:"installedPath"`
			}
			if json.Unmarshal(out[jsonStart:], &result) != nil || result.Path == "" {
				t.Fatal("local fixture installation lost its path")
			}
			selected = result.Path
		}
	}
	home, settings, err := snapshotCodexPluginHome(snapshot, source, workshop, []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}}, map[string]string{"selected@fixture-market": selected})
	if err != nil {
		t.Fatal(err)
	}
	if granted {
		before, closeBefore := nativeCodexPluginProbe(t, bin, &codexDriver{CodexHome: home, RuntimeConfig: settings, env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + base}})
		details := before("plugin/read", map[string]any{"pluginName": "selected", "marketplacePath": filepath.Join(home, "marketplaces", "fixture-market", ".agents", "plugins", "marketplace.json")})
		closeBefore()
		if !strings.Contains(string(details), "pluginambient") {
			t.Fatal("fixture did not expose a native plugin MCP server before sanitizing")
		}
	}
	sanitizeWork := catalog
	if sandbox {
		sanitizeWork = sandboxWorkdir
	}
	if granted {
		if err := sanitizeCodexInteractiveHome(context.Background(), home, source, sanitizeWork); err != nil {
			t.Fatal(err)
		}
	}
	if granted {
		// Stand-in for a lower-priority system config layer, which user-copy filtering
		// cannot remove. Explicit false must win native map merging.
		f, err := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(f, "\n[mcp_servers.systemfixture]\ncommand=\"sh\"\nargs=[\"-c\",%q]\n", "touch "+filepath.Join(base, "system-started"))
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	driver := &codexDriver{CodexHome: home, RuntimeConfig: settings, env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + base}}
	runtimeCwd := catalog
	startSandbox := func() {}
	stopSandbox := func() {}
	if sandbox {
		if _, err := exec.LookPath("docker"); err != nil {
			t.Skip("Docker not installed")
		}
		if err := exec.Command("docker", "image", "inspect", "busybox:1.37").Run(); err != nil {
			t.Skip("local busybox fixture image not present")
		}
		container := "blerg-plugin-fixture-" + safeFileName(filepath.Base(base)) + "-" + ccUUID()
		args := sandboxRunArgs(container, catalog, "", nil, sandboxRunExtras{network: "host", codexHome: home, codexSourceHome: source})
		image := len(args) - 3
		args = append(append(append([]string{}, args[:image]...), "--mount", codexHomeMountArg(bin)+",readonly", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())), "busybox:1.37", "sleep", "infinity")
		startSandbox = func() {
			if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
				t.Fatalf("native Docker fixture failed: %v %s", err, out)
			}
		}
		stopSandbox = func() { _ = exec.Command("docker", "rm", "-f", container).Run() }
		startSandbox()
		t.Cleanup(stopSandbox)
		driver.prefix = sandboxExecPrefix(container)
		runtimeCwd = sandboxWorkdir
	}

	rpc, stop := nativeCodexPluginProbe(t, bin, driver)
	skills := rpc("skills/list", map[string]any{"cwds": []string{runtimeCwd}, "forceReload": true})
	if !strings.Contains(string(skills), `"pluginId":"selected@fixture-market"`) {
		t.Fatalf("native Codex did not load the selected plugin snapshot: args=%v installed=%s", driver.launchArgs(), rpc("plugin/installed", map[string]any{"cwds": []string{base}}))
	}
	startParams := map[string]any{"cwd": runtimeCwd, "model": "gpt-6.1-sol", "persistExtendedHistory": true}
	if granted {
		startParams["dynamicTools"] = []map[string]any{{"name": "blerg_mcp__fixture__granted", "description": "Explicit grant fixture", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}
	}
	if granted {
		cfg := map[string]any{}
		if err := codexAmbientMCPOverrides(rpc("config/read", map[string]any{"cwd": runtimeCwd, "includeLayers": false}), cfg); err != nil {
			t.Fatal(err)
		}
		startParams["config"] = cfg
		if _, err := os.Stat(filepath.Join(base, "system-started")); !os.IsNotExist(err) {
			t.Fatal("config/read started ambient MCP before explicit disabling")
		}
	}
	started := rpc("thread/start", startParams)
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(started, &response); err != nil || response.Thread.ID == "" {
		t.Fatal("native thread identity not recorded")
	}
	rpc("turn/start", map[string]any{"threadId": response.Thread.ID, "input": []map[string]any{{"type": "text", "text": "Reply with the fixture response."}}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		state := rpc("thread/read", map[string]any{"threadId": response.Thread.ID, "includeTurns": true})
		var history struct {
			Thread struct {
				Turns []struct {
					Status string `json:"status"`
				} `json:"turns"`
			} `json:"thread"`
		}
		_ = json.Unmarshal(state, &history)
		if len(history.Thread.Turns) > 0 && history.Thread.Turns[len(history.Thread.Turns)-1].Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("local fixture turn did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	stopSandbox()
	// A real daemon Kill removes private SQLite/index state along with the plugin snapshot.
	// Rebuild the snapshot, retaining only normal native rollouts, before resuming.
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	rebuilt, settings, err := snapshotCodexPluginHome(snapshot, source, workshop, []pluginspec.Entry{{Marketplace: "example-org/plugins", Plugin: "selected"}}, map[string]string{"selected@fixture-market": selected})
	if err != nil || rebuilt != home {
		t.Fatalf("snapshot rebuild failed: %v", err)
	}
	driver.RuntimeConfig = settings
	if granted {
		if err := sanitizeCodexInteractiveHome(context.Background(), rebuilt, source, sanitizeWork); err != nil {
			t.Fatal(err)
		}
	}
	startSandbox()
	rpc, stop = nativeCodexPluginProbe(t, bin, driver)
	defer stop()
	resumed := rpc("thread/resume", map[string]any{"threadId": response.Thread.ID, "persistExtendedHistory": true})
	var again struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(resumed, &again); err != nil || again.Thread.ID != response.Thread.ID {
		t.Fatal("private plugin home did not resume the same durable thread")
	}
	recalled := rpc("thread/read", map[string]any{"threadId": response.Thread.ID, "includeTurns": true})
	if !strings.Contains(string(recalled), "Fixture reply.") || !strings.Contains(string(recalled), "Reply with the fixture response.") {
		t.Fatal("rebuilt plugin home lost the previous conversation")
	}
	got, _ := os.ReadFile(filepath.Join(source, "config.toml"))
	if string(got) != config {
		t.Fatal("native plugin runtime changed user config")
	}
	if granted {
		select {
		case body := <-modelRequests:
			text := string(body)
			if !strings.Contains(text, "exec_command") && !strings.Contains(text, "shell_command") && !strings.Contains(text, "local_shell") {
				t.Fatal("interactive grant lost native shell tools")
			}
			if !strings.Contains(text, "blerg_mcp__fixture__granted") {
				t.Fatal("interactive grant missing explicit dynamic gateway tool")
			}
			for _, name := range []string{"pluginambient", "ambient", "project"} {
				if strings.Contains(text, `"name":"`+name+`"`) {
					t.Fatalf("ungranted MCP tool reached model: %s", name)
				}
			}
		default:
			t.Fatal("native model catalog not captured")
		}
	}
	if granted {
		for _, name := range []string{"ambient-started", "project-started", "plugin-started", "system-started"} {
			if _, err := os.Stat(filepath.Join(base, name)); !os.IsNotExist(err) {
				t.Fatalf("ungranted MCP started: %s", name)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(source, "plugins")); !os.IsNotExist(err) {
		t.Fatal("native plugin runtime wrote the user's plugin cache")
	}
}

func nativeCodexPluginProbe(t *testing.T, bin string, d *codexDriver) (func(string, any) json.RawMessage, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	args := d.launchArgs()
	if d.prefix.enabled() {
		args = append([]string{"CODEX_HOME=" + d.CodexHome, bin}, args...)
		bin = "env"
	}
	cmd := d.prefix.command(ctx, d.workDir, bin, args...)
	cmd.Env = d.launchEnv()
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	frames := make(chan codexRPC, 64)
	go func() {
		defer close(frames)
		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 4096), 4<<20)
		for scan.Scan() {
			var frame codexRPC
			if json.Unmarshal(scan.Bytes(), &frame) == nil {
				select {
				case frames <- frame:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	var seq int
	call := func(method string, params any) json.RawMessage {
		t.Helper()
		seq++
		id := seq
		if err := json.NewEncoder(in).Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case frame, ok := <-frames:
				if !ok {
					t.Fatal("native Codex probe closed")
				}
				var got int
				if json.Unmarshal(frame.ID, &got) != nil || got != id {
					continue
				}
				if len(frame.Error) > 0 && string(frame.Error) != "null" {
					if method == "thread/read" && strings.Contains(string(frame.Error), "list_turns is not supported yet") {
						time.Sleep(20 * time.Millisecond)
						if err := json.NewEncoder(in).Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
							t.Fatal(err)
						}
						continue
					}
					t.Fatalf("native %s rejected fixture: %s", method, frame.Error)
				}
				return frame.Result
			case <-ctx.Done():
				t.Fatalf("native %s timed out", method)
			}
		}
	}
	call("initialize", map[string]any{"clientInfo": map[string]any{"name": "blerg-plugin-test", "version": "test"}, "capabilities": map[string]any{"experimentalApi": true}})
	if err := json.NewEncoder(in).Encode(map[string]any{"method": "initialized"}); err != nil {
		t.Fatal(err)
	}
	stop := func() { cancel(); _ = cmd.Wait() }
	t.Cleanup(cancel)
	return call, stop
}
