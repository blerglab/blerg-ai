package runner

import (
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"testing"
)

func TestSelectedBoardSurvivesPodSpawn(t *testing.T) {
	t.Setenv("BLERG_RUNNER_BOARD_ID", "board-7")
	t.Setenv(daemon.MCPGatewayEnvVar, `{"base_url":"http://gateway.internal:9090","servers":[{"name":"board","token":"fixture-gateway-token"}]}`)
	cfg := ConfigFromEnv()
	spawn := podSpawn(cfg, "/workspace/app")
	if spawn.BoardID != "board-7" || spawn.MCPGateway == nil || spawn.RestrictTools {
		t.Fatal("pod spawn lost selected board, gateway, or normal coding tools")
	}
}

func TestPodSpawnCarriesSessionMessagingToken(t *testing.T) {
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "fixture-session-token")
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "fixture-runtime-token")
	spawn := podSpawn(ConfigFromEnv(), "/workspace/app")
	if spawn.SessionToken != "fixture-session-token" {
		t.Fatal("pod spawn dropped session messaging token")
	}
	if spawn.SessionToken == "fixture-runtime-token" {
		t.Fatal("runtime credential reached agent")
	}
}
