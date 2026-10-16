package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestCodexRestrictedHostWiresDynamicFilesAndCleansRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux confinement required")
	}
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_DYNAMIC", "1")
	envDump := filepath.Join(t.TempDir(), "child-env.json")
	t.Setenv("FAKE_CODEX_ENV_DUMP", envDump)
	auth := t.TempDir()
	if err := os.WriteFile(filepath.Join(auth, "auth.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", auth)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "sample.txt"), []byte("restricted-file-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	const grantToken = "test-only-gateway-secret"
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer gateway.Close()
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: work, HomeDir: t.TempDir()})
	defer host.Kill("restricted")
	host.Spawn(protocol.SpawnSession{SessionID: "restricted", Kind: "agent", Engine: "codex", ProjectPath: work, RestrictTools: true, InitialPrompt: "Read sample.txt", MCPGateway: &protocol.MCPGatewayConfig{BaseURL: gateway.URL, Servers: []protocol.MCPGatewayServer{{Name: "docs", Token: grantToken}}}})
	sender.waitForKind(t, "turn_done")
	host.mu.Lock()
	session := host.sessions["restricted"]
	var driver *codexDriver
	if session != nil {
		driver, _ = session.loop.(*codexDriver)
	}
	host.mu.Unlock()
	if driver == nil || driver.RestrictedRuntime == nil {
		t.Fatal("restricted boundary was not installed")
	}
	if session.mcp != nil {
		t.Fatal("Codex received a native MCP config containing grant credentials")
	}
	r := driver.RestrictedRuntime
	home := r.Home
	raw, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatal("child environment was not recorded")
	}
	if strings.Contains(string(raw), grantToken) {
		t.Fatal("MCP grant secret reached child environment")
	}
	var env map[string]string
	if json.Unmarshal(raw, &env) != nil || env["CODEX_HOME"] != home {
		t.Fatal("child did not use isolated policy home")
	}
	sender.mu.Lock()
	found := false
	for _, event := range sender.msgs {
		ev, ok := event.(protocol.AgentEvent)
		if !ok || ev.Kind != "tool_result" {
			continue
		}
		payload, _ := json.Marshal(ev.Payload)
		if strings.Contains(string(payload), "restricted-file-marker") {
			found = true
		}
	}
	sender.mu.Unlock()
	if !found {
		t.Fatal("dynamic file request was not executed by the rooted boundary")
	}
	host.Kill("restricted")
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("private policy home survived session termination")
	}
	if _, err := r.files.root.ReadFile("sample.txt"); err == nil {
		t.Fatal("workspace root remained open after termination")
	}
}
func TestCodexRestrictedHostRefusesUnverifiedCLI(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_VERSION", "codex-cli 0.154.0")
	work := t.TempDir()
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: work, HomeDir: t.TempDir()})
	host.Spawn(protocol.SpawnSession{SessionID: "wrong-version", Kind: "agent", Engine: "codex", ProjectPath: work, RestrictTools: true})
	if reason := sender.waitForSessionError(t); !strings.Contains(reason, codexRestrictedVersion) {
		t.Fatal("unverified runtime was not refused with required version")
	}
	if len(host.ActiveIDs()) != 0 {
		t.Fatal("unverified runtime was left active")
	}
}
