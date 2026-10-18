package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestSessionFilingMCPConfigPreservesGrantAndPrivateFile(t *testing.T) {
	for _, grant := range []*protocol.MCPGatewayConfig{nil, {BaseURL: "https://runner.example.test", Servers: []protocol.MCPGatewayServer{{Name: "board", Token: "fixture-grant"}}}} {
		f := newMCPConfigFile(grant, nil)
		f.sessionFiling = true
		t.Cleanup(f.Remove)
		path, err := f.Ensure()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Servers map[string]map[string]any `json:"mcpServers"`
		}
		if json.Unmarshal(raw, &cfg) != nil || cfg.Servers[sessionFilingMCPName]["command"] != "blerg-runner" {
			t.Fatal("missing trusted stdio session server")
		}
		want := 1
		if grant != nil {
			want = 2
			if cfg.Servers["board"]["url"] != "https://runner.example.test/mcp-gw/board" {
				t.Fatal("gateway changed")
			}
		}
		if len(cfg.Servers) != want {
			t.Fatal("extra MCP authority")
		}
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatal("session MCP config not private")
		}
	}
}

func TestSessionFilingOnlyKeepsAmbientMCPWithoutWeakeningRestrictedOrGrant(t *testing.T) {
	for _, mode := range []string{"plain", "grant", "restricted"} {
		options := []ccOption{withMCPConfigPath("/tmp/fixture-private/mcp.json")}
		if mode == "plain" || mode == "restricted" {
			options = append(options, func(o *ccOptions) { o.MCPAmbient = true })
		}
		if mode == "restricted" {
			options = append(options, withRestrictTools())
		}
		for _, args := range [][]string{ccTurnArgs("work", "", "", "", options...), ccSessionArgs("", "", "", options...)} {
			text := strings.Join(args, "\n")
			if !strings.Contains(text, "--mcp-config=") {
				t.Fatal("session server omitted")
			}
			if strings.Contains(text, "--strict-mcp-config") != (mode != "plain") {
				t.Fatalf("MCP isolation changed for %s", mode)
			}
			if mode == "restricted" && (!strings.Contains(text, "--tools="+ccGrantBuiltinTools) || !strings.Contains(text, "--setting-sources=")) {
				t.Fatal("restricted builtins were widened")
			}
		}
	}
	native := codexSessionFilingStdio()
	raw, _ := json.Marshal(native)
	if native["command"] != "blerg-runner" || strings.Contains(string(raw), "fixture-secret") {
		t.Fatal("native self-server config includes credentials")
	}
}

func TestSessionFilingDynamicToolRefreshesTokenAndHasNoOtherAuthority(t *testing.T) {
	state := t.TempDir()
	session := "session-fixture"
	var auth []string
	var authMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/session-fixture/filing-tools" {
			t.Error("wrong self-service target")
		}
		authMu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		count := len(auth)
		authMu.Unlock()
		if count == 1 {
			if err := writeSessionTokenFile(state, session, "fixture-refreshed"); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"pending approval"}],"isError":false}}`))
	}))
	defer server.Close()
	tool := &sessionFilingTool{server: server.URL, session: session, initialToken: "fixture-initial", stateDir: state}
	runtime := &codexRestrictedRuntime{Restricted: true, filing: tool, specs: []map[string]any{sessionFilingSpec()}}
	for range 2 {
		text, err := runtime.Call(t.Context(), sessionFilingToolName, json.RawMessage(`{"action":"list_boards"}`))
		if err != nil || !strings.Contains(text, "pending approval") {
			t.Fatal("self-service request failed")
		}
	}
	authMu.Lock()
	observed := strings.Join(auth, ",")
	authMu.Unlock()
	if observed != "Bearer fixture-initial,Bearer fixture-refreshed" {
		t.Fatal("session token refresh not observed")
	}
	if _, err := runtime.Call(t.Context(), "Bash", json.RawMessage(`{}`)); err == nil {
		t.Fatal("session filing enabled shell")
	}
	if len(runtime.Specs()) != 1 || runtime.Specs()[0]["name"] != sessionFilingToolName {
		t.Fatal("extra dynamic tools")
	}
}

func TestSessionFilingDynamicToolRefusesRedirectWithoutSendingToken(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(302)
	}))
	defer server.Close()
	tool := &sessionFilingTool{server: server.URL, session: "fixture", initialToken: "fixture-secret", stateDir: t.TempDir()}
	text, err := tool.Call(t.Context(), json.RawMessage(`{}`))
	if err == nil || calls != 0 || strings.Contains(text, "fixture-secret") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("redirect leaked session authority")
	}
}

func TestSessionFilingSandboxUsesConfiguredHTTPWithoutAmbientWebsocket(t *testing.T) {
	env := sandboxNetworkEnv([]string{"BLERG_RUNNER_SERVER_HTTP=http://127.0.0.1:8083", "BLERG_RUNNER_SESSION_ID=fixture", "BLERG_RUNNER_SESSION_TOKEN=fixture-token"})
	if !hasEntry(env, "BLERG_RUNNER_SERVER_HTTP="+sandboxRunnerURL) || hasEntry(env, "BLERG_RUNNER_SERVER_HTTP=http://127.0.0.1:8083") {
		t.Fatal("sandbox session tools cannot reach the configured desktop runner")
	}
}

func TestSessionFilingPlainCodexSpawnUsesNativeConfigWithoutReplacingUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	user := filepath.Join(home, ".codex")
	if err := os.MkdirAll(user, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("[mcp_servers.ambient]\ncommand=\"fixture-user-tool\"\n")
	if err := os.WriteFile(filepath.Join(user, "config.toml"), original, 0600); err != nil {
		t.Fatal(err)
	}
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKE_CODEX_LOG", logPath)
	repos := t.TempDir()
	if err := os.Mkdir(filepath.Join(repos, "proj"), 0700); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: repos, HomeDir: home, ServerHTTP: "http://runner.example.test"})
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "filing-plain", Repo: "proj", Kind: "agent", Engine: "codex", SessionToken: "fixture-token", InitialPrompt: "work"})
	t.Cleanup(func() { host.Kill("filing-plain") })
	sender.waitForKind(t, "turn_done")
	sess := host.get("filing-plain")
	if sess == nil {
		t.Fatal("session absent")
	}
	driver, ok := sess.loop.(*codexDriver)
	if !ok || driver.RestrictedRuntime != nil {
		t.Fatal("self-service filing isolated an ordinary session")
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "thread/start ") {
			continue
		}
		var p struct {
			Config map[string]any `json:"config"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "thread/start ")), &p) != nil {
			t.Fatal("invalid native thread fixture")
		}
		if p.Config["mcp_servers."+sessionFilingMCPName] == nil {
			t.Fatal("native thread omitted the self-service server")
		}
		if _, ok := p.Config["mcp_servers"]; ok {
			t.Fatal("plain self-service filing replaced ambient MCP")
		}
		found = true
	}
	if !found {
		t.Fatal("native thread was not started")
	}
	current, err := os.ReadFile(filepath.Join(user, "config.toml"))
	if err != nil || !bytes.Equal(original, current) {
		t.Fatal("user MCP configuration changed")
	}
}

func TestSessionFilingDynamicToolPreservesErrorAndRedactsCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Denied fixture-token"}],"isError":true}}`))
	}))
	defer server.Close()
	tool := &sessionFilingTool{server: server.URL, session: "fixture", initialToken: "fixture-token", stateDir: t.TempDir()}
	text, err := tool.Call(t.Context(), json.RawMessage(`{}`))
	if err == nil || strings.Contains(text, "fixture-token") || !strings.Contains(text, "Denied") {
		t.Fatal("tool refusal or credential redaction was lost")
	}
}

func TestSessionFilingDynamicOwnedChildUsesOnlyItsLiveTurn(t *testing.T) {
	for _, tool := range []string{sessionFilingToolName, sessionPluginRequestToolName} {
		t.Run(tool, func(t *testing.T) { testSessionBuiltinOwnedChild(t, tool) })
	}
}
func testSessionBuiltinOwnedChild(t *testing.T, builtin string) {
	for _, mode := range []string{"owned", "foreign", "stale", "ended", "restricted", "old-app", "closed-app", "root-interrupted", "interrupted", "gateway-tool"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"pending approval"}]}}`))
			}))
			defer server.Close()
			a, _ := codexPlanFixture()
			a.in = nopCodexWriter{}
			a.d.RestrictedRuntime = &codexRestrictedRuntime{filing: &sessionFilingTool{server: server.URL, session: "fixture", initialToken: "fixture-token", stateDir: t.TempDir()}}
			a.d.children = map[string]*codexChild{"owned-child": {callID: "child-run"}}
			a.childTurns = map[string]string{"owned-child": "child-turn"}
			thread, turn, tool := "owned-child", "child-turn", builtin
			switch mode {
			case "foreign":
				thread = "foreign-child"
			case "stale":
				turn = "prior-turn"
			case "ended":
				a.d.children[thread].done = true
			case "restricted":
				a.d.RestrictedRuntime.Restricted = true
			case "old-app":
				a.d.app = &codexApp{}
			case "closed-app":
				close(a.done)
			case "root-interrupted":
				a.interruptRequested = true
			case "interrupted":
				a.interruptedChildren = map[string]string{thread: turn}
			case "gateway-tool":
				tool = "blerg_mcp__board__write"
			}
			arguments := map[string]string{"action": "propose", "board": "Other", "title": "Fixture", "body": "Details"}
			if builtin == sessionPluginRequestToolName {
				arguments = map[string]string{"name": "safe", "reason": "needed"}
			}
			params, _ := json.Marshal(map[string]any{"threadId": thread, "turnId": turn, "tool": tool, "arguments": arguments})
			a.dynamicTool(t.Context(), codexRPC{ID: json.RawMessage(`1`), Method: "item/tool/call", Params: params})
			want := 0
			if mode == "owned" {
				want = 1
			}
			if int(calls.Load()) != want {
				t.Fatalf("child mode %s reached filing %d times, want %d", mode, calls.Load(), want)
			}
		})
	}
}
