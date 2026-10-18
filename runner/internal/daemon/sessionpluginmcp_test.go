package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestSessionPluginRequestDynamicBridgeIsApprovalOnly(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/sessions/own-session/filing-tools" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("wrong fixed-session request")
		}
		var req struct {
			Params struct {
				Name      string
				Arguments map[string]any
			}
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Params.Name != "blerg_request_plugin" || req.Params.Arguments["name"] != "safe" || req.Params.Arguments["reason"] != "needed" {
			t.Error("tool/arguments changed")
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"approval pending"}],"isError":false}}`))
	}))
	defer server.Close()
	rt := &codexRestrictedRuntime{Restricted: true, filing: &sessionFilingTool{server: server.URL, session: "own-session", initialToken: "fixture-token", stateDir: t.TempDir()}, specs: sessionBuiltinSpecs()}
	result, err := rt.Call(t.Context(), "blerg_request_plugin", json.RawMessage(`{"name":"safe","reason":"needed"}`))
	if err != nil || result != "approval pending" || calls.Load() != 1 {
		t.Fatal("restricted agent cannot request human approval")
	}
	for _, bad := range []string{"Bash", "plugin/install", "blerg_request_plugin_extra", "http://foreign.example.test"} {
		if _, err := rt.Call(t.Context(), bad, json.RawMessage(`{}`)); err == nil {
			t.Fatal("unknown authority accepted")
		}
	}
	if _, err := rt.filing.CallNamed(t.Context(), "plugin/install", json.RawMessage(`{}`)); err == nil {
		t.Fatal("private transport accepted unknown tool")
	}
	if calls.Load() != 1 {
		t.Fatal("unknown tool reached network")
	}
	if len(rt.Specs()) != 2 {
		t.Fatal("unexpected built-in tools")
	}
}

func TestSessionPluginRequestRestrictedHostHasOnlyRootedDynamicTools(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	auth := t.TempDir()
	if err := os.WriteFile(filepath.Join(auth, "auth.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", auth)
	logPath := filepath.Join(t.TempDir(), "native.log")
	t.Setenv("FAKE_CODEX_LOG", logPath)
	work := t.TempDir()
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: work, HomeDir: t.TempDir(), ServerHTTP: "http://runner.example.test"})
	host.Spawn(protocol.SpawnSession{SessionID: "plugin-cron", Kind: "agent", Engine: "codex", ProjectPath: work, RestrictTools: true, SessionToken: "fixture-token", InitialPrompt: "inspect"})
	defer host.Kill("plugin-cron")
	sender.waitForKind(t, "turn_done")
	host.mu.Lock()
	sess := host.sessions["plugin-cron"]
	host.mu.Unlock()
	if sess == nil {
		t.Fatal("restricted session missing")
	}
	d, ok := sess.loop.(*codexDriver)
	if !ok || d.RestrictedRuntime == nil || !d.RestrictedRuntime.Restricted || sess.mcp != nil {
		t.Fatal("restricted boundary changed")
	}
	found := false
	for _, spec := range d.RestrictedRuntime.Specs() {
		if spec["name"] == "blerg_request_plugin" {
			found = true
		}
	}
	if !found {
		t.Fatal("unattended session lacks plugin approval tool")
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-token") {
		t.Fatal("credential reached native protocol")
	}
	foundNative := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "thread/start ") {
			continue
		}
		var p struct {
			Sandbox      string
			DynamicTools []struct{ Name string }
			Config       map[string]any
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "thread/start ")), &p) != nil {
			t.Fatal("invalid native thread config")
		}
		if p.Sandbox != "read-only" {
			t.Fatal("native sandbox widened")
		}
		if _, nativeMCP := p.Config["mcp_servers."+sessionFilingMCPName]; nativeMCP {
			t.Fatal("restricted runtime enabled native MCP")
		}
		for _, tool := range p.DynamicTools {
			if tool.Name == sessionPluginRequestToolName {
				foundNative = true
			}
		}
	}
	if !foundNative {
		t.Fatal("native protocol omitted approval tool")
	}
	// Refuse direct shell/installer use even though the approval-only tool exists.
	if _, err := d.RestrictedRuntime.Call(context.Background(), "Bash", json.RawMessage(`{}`)); err == nil {
		t.Fatal("shell enabled")
	}
}
