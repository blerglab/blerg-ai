package daemon

import (
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// selectedBoardCLIEnv exposes only the selected board's existing gateway grant.
// It never conveys an upstream credential or bypasses gateway tool/call budgets.
// The same environment travels into sandboxed subprocesses via sandboxEnvArgs.
func selectedBoardCLIEnv(boardID string, cfg *protocol.MCPGatewayConfig) []string {
	if boardID == "" || cfg == nil || ValidateMCPGateway(cfg) != nil {
		return nil
	}
	for _, server := range cfg.Servers {
		if server.Name == "board" {
			return []string{"BLERG_RUNNER_BOARD_ID=" + boardID, "BLERG_RUNNER_BOARD_TOKEN=" + server.Token, "BLERG_RUNNER_BOARD_GATEWAY_URL=" + strings.TrimRight(cfg.BaseURL, "/") + "/mcp-gw/board"}
		}
	}
	return nil
}
