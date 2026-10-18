package daemon

import (
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestSelectedBoardCLIEnv(t *testing.T) {
	cfg := &protocol.MCPGatewayConfig{BaseURL: "https://runner.example.test", Servers: []protocol.MCPGatewayServer{{Name: "board", Token: "fixture-board-grant"}}}
	got := selectedBoardCLIEnv("fixture-board", cfg)
	want := []string{"BLERG_RUNNER_BOARD_ID=fixture-board", "BLERG_RUNNER_BOARD_TOKEN=fixture-board-grant", "BLERG_RUNNER_BOARD_GATEWAY_URL=https://runner.example.test/mcp-gw/board"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatal("selected board environment differs")
	}
	for _, tc := range []struct {
		board string
		cfg   *protocol.MCPGatewayConfig
	}{{"", cfg}, {"fixture-board", nil}, {"fixture-board", &protocol.MCPGatewayConfig{BaseURL: cfg.BaseURL, Servers: []protocol.MCPGatewayServer{{Name: "jira", Token: "fixture-token"}}}}} {
		if len(selectedBoardCLIEnv(tc.board, tc.cfg)) != 0 {
			t.Fatal("board credentials granted without a selected board and matching gateway")
		}
	}
}

func TestInheritedSelectedBoardCLIEnvRemoved(t *testing.T) {
	for _, key := range []string{"BLERG_RUNNER_BOARD_ID", "BLERG_RUNNER_BOARD_TOKEN", "BLERG_RUNNER_BOARD_GATEWAY_URL"} {
		t.Setenv(key, "inherited-fixture")
	}
	for _, kv := range buildSessionEnv(sessionEnvOpts{SessionID: "new-session"}) {
		if strings.Contains(kv, "inherited-fixture") {
			t.Fatal("inherited board credentials reached another session")
		}
	}
}

func TestSelectedBoardCLIEnvRejectsMalformedGrant(t *testing.T) {
	for _, cfg := range []*protocol.MCPGatewayConfig{
		{BaseURL: "https://user:pass@runner.example.test", Servers: []protocol.MCPGatewayServer{{Name: "board", Token: "fixture-token"}}},
		{BaseURL: "https://runner.example.test", Servers: []protocol.MCPGatewayServer{{Name: "board", Token: "bad\ntoken"}}},
		{BaseURL: "https://runner.example.test", Servers: []protocol.MCPGatewayServer{{Name: "board", Token: "fixture-one"}, {Name: "board", Token: "fixture-two"}}},
	} {
		if len(selectedBoardCLIEnv("fixture-board", cfg)) != 0 {
			t.Fatal("malformed grant reached child environment")
		}
	}
}

func TestSelectedBoardCLIEnvReachesSandbox(t *testing.T) {
	cfg := &protocol.MCPGatewayConfig{BaseURL: "https://runner.example.test", Servers: []protocol.MCPGatewayServer{{Name: "board", Token: "fixture-token"}}}
	env := selectedBoardCLIEnv("fixture-board", cfg)
	args := sandboxEnvArgs(env)
	if len(args) != 2*len(env) {
		t.Fatal("selected-board variables did not reach sandbox")
	}
	for i, kv := range env {
		if args[2*i] != "-e" || args[2*i+1] != kv {
			t.Fatal("sandbox changed selected-board variables")
		}
	}
}
