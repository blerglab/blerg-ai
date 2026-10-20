package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// boardViaServer is a board-only grant the server asked to be dialled through
// its own web address; its BaseURL is an in-cluster address no workstation can
// reach.
func boardViaServer() *protocol.MCPGatewayConfig {
	return &protocol.MCPGatewayConfig{
		BaseURL:   "http://gw.cluster.invalid:8090",
		ViaServer: true,
		Servers:   []protocol.MCPGatewayServer{{Name: "board", Token: "tok-board-secret"}},
	}
}

func TestGatewayServerBase(t *testing.T) {
	for _, tc := range []struct{ http, ws, want string }{
		{"https://runner.example.test/", "wss://other.example.test/ws/daemon", "https://runner.example.test"},
		{"", "wss://runner.example.test/ws/daemon", "https://runner.example.test"},
		{"", "ws://127.0.0.1:8083/ws/daemon", "http://127.0.0.1:8083"},
		{"  ", "", ""},
		{"", "tcp://runner.example.test", ""},
	} {
		if got := gatewayServerBase(tc.http, tc.ws); got != tc.want {
			t.Errorf("gatewayServerBase(%q, %q) = %q, want %q", tc.http, tc.ws, got, tc.want)
		}
	}
}

func TestViaServerGateway(t *testing.T) {
	// Not asked for: the grant is used as sent.
	plain := testGateway()
	if got, err := viaServerGateway(plain, "https://runner.example.test"); err != nil || got != plain {
		t.Fatalf("plain grant changed: %v %v", got, err)
	}
	// Asked for: a copy aimed at the daemon's own server address; the original is untouched.
	in := boardViaServer()
	got, err := viaServerGateway(in, "https://runner.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "https://runner.example.test" || !reflect.DeepEqual(got.Servers, in.Servers) {
		t.Errorf("via-server grant = %+v", got)
	}
	if in.BaseURL != "http://gw.cluster.invalid:8090" {
		t.Errorf("the original grant was modified: %q", in.BaseURL)
	}
	// Asked for, but the daemon knows no server address: refused, without a token in the reason.
	if _, err := viaServerGateway(in, ""); err == nil || strings.Contains(err.Error(), "tok-") {
		t.Errorf("no base: err = %v", err)
	}
}

func TestSandboxGatewayView(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://localhost:8083", "http://blerg-runner:8080"},
		{"http://127.0.0.1:8083/", "http://blerg-runner:8080"},
		{"http://localhost:8083/runner", "http://blerg-runner:8080/runner"},
		{"https://runner.example.test", "https://runner.example.test"},
		{"http://blerg-runner:8090", "http://blerg-runner:8090"},
	} {
		got := sandboxGatewayView(&protocol.MCPGatewayConfig{BaseURL: tc.in, Servers: testGateway().Servers})
		if got.BaseURL != tc.want {
			t.Errorf("sandbox view of %q = %q, want %q", tc.in, got.BaseURL, tc.want)
		}
	}
}

// The board's gateway URL handed to the board CLI is rewritten for the sandbox
// network like the other host-side addresses.
func TestSandboxNetworkEnvRewritesBoardGatewayURL(t *testing.T) {
	got := sandboxNetworkEnv([]string{
		"BLERG_RUNNER_BOARD_GATEWAY_URL=http://localhost:8083/mcp-gw/board",
		"BLERG_RUNNER_SERVER_HTTP=http://localhost:8083",
	})
	want := []string{
		"BLERG_RUNNER_BOARD_GATEWAY_URL=http://blerg-runner:8080/mcp-gw/board",
		"BLERG_RUNNER_SERVER_HTTP=" + sandboxRunnerURL,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
	remote := []string{"BLERG_RUNNER_BOARD_GATEWAY_URL=https://runner.example.test/mcp-gw/board"}
	if got := sandboxNetworkEnv(remote); !reflect.DeepEqual(got, remote) {
		t.Errorf("a remote gateway URL was rewritten: %v", got)
	}
}

// A sandboxed session's config file is read inside the container: a loopback
// gateway address (the daemon's own server address on a desktop stack) must be
// the runner's address on the sandbox network there.
func TestMCPConfigFileSandboxUsesSandboxNetworkAddress(t *testing.T) {
	var stdins []string
	origRun, origStdin := dockerRun, dockerRunStdin
	dockerRun = func(args ...string) error { return nil }
	dockerRunStdin = func(stdin []byte, args ...string) error {
		stdins = append(stdins, string(stdin))
		return nil
	}
	t.Cleanup(func() { dockerRun, dockerRunStdin = origRun, origStdin })

	cfg := &protocol.MCPGatewayConfig{BaseURL: "http://localhost:8083", Servers: boardViaServer().Servers}
	f := newMCPConfigFile(cfg, sandboxExecPrefix("blerg-sandbox-s1"))
	if _, err := f.Ensure(); err != nil {
		t.Fatal(err)
	}
	if len(stdins) != 1 || !strings.Contains(stdins[0], `"url":"http://blerg-runner:8080/mcp-gw/board"`) {
		t.Fatalf("in-container config = %q", stdins)
	}
	if cfg.BaseURL != "http://localhost:8083" {
		t.Errorf("the session's grant was modified: %q", cfg.BaseURL)
	}
}

// The daemon's gate: the person's board alone, dialled through the server, may
// go to an unrestricted session on this machine's host; nothing else may.
func TestMCPGatewaySpawnProblemHostBoard(t *testing.T) {
	board := boardViaServer()
	notVia := boardViaServer()
	notVia.ViaServer = false
	twoServers := boardViaServer()
	twoServers.Servers = append(twoServers.Servers, protocol.MCPGatewayServer{Name: "calendar", Token: "tok-calendar"})
	notBoard := boardViaServer()
	notBoard.Servers[0].Name = "calendar"
	cases := []struct {
		name string
		msg  protocol.SpawnSession
		want bool // refused
	}{
		{"host board via server", protocol.SpawnSession{Kind: "agent", MCPGateway: board}, false},
		{"host board not via server", protocol.SpawnSession{Kind: "agent", MCPGateway: notVia}, true},
		{"host board and a connection", protocol.SpawnSession{Kind: "agent", MCPGateway: twoServers}, true},
		{"host connection via server", protocol.SpawnSession{Kind: "agent", MCPGateway: notBoard}, true},
		{"host board restricted", protocol.SpawnSession{Kind: "agent", RestrictTools: true, MCPGateway: board}, true},
		{"terminal board", protocol.SpawnSession{MCPGateway: board}, true},
		{"sandboxed board", protocol.SpawnSession{Kind: "agent", Sandbox: true, MCPGateway: board}, false},
	}
	for _, c := range cases {
		if got := mcpGatewaySpawnProblem(c.msg) != ""; got != c.want {
			t.Errorf("%s: refused = %v, want %v", c.name, got, c.want)
		}
	}
}

// End to end through the Manager: a session on this machine's host with board
// access gets an MCP config and a board CLI address aimed at the server the
// daemon reaches, never the server's in-cluster gateway address.
func TestManagerHostBoardGrantDialsThroughServer(t *testing.T) {
	engineDir := fakeClaude(t)
	t.Setenv("PATH", engineDir+":"+os.Getenv("PATH"))
	claudeLog := filepath.Join(engineDir, "calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
	t.Setenv("BLERG_CLAUDE_STEERING", "0")
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: reposRoot, ServerURL: "wss://runner.example.test/ws/daemon"})
	mgr.agents = host
	defer host.Kill("s-host-board")

	raw, err := json.Marshal(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-host-board", Repo: "proj", Kind: "agent",
		InitialPrompt: "hello", BoardID: "board-1", MCPGateway: boardViaServer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr.handleSpawnSession(raw)
	if !host.Has("s-host-board") {
		t.Fatalf("the host session with board access did not spawn: %v", sender.sessionErrors())
	}
	sender.waitForKind(t, "turn_done")

	logged, _ := os.ReadFile(claudeLog)
	line := strings.TrimSpace(string(logged))
	i := strings.Index(line, "--mcp-config=")
	if i < 0 {
		t.Fatalf("no --mcp-config in %s", line)
	}
	path := strings.Fields(line[i:])[0][len("--mcp-config="):]
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"url":"https://runner.example.test/mcp-gw/board"`) || strings.Contains(string(body), "cluster.invalid") {
		t.Errorf("config does not dial the gateway through the server: %s", body)
	}
}
