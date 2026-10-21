package daemon

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

// The driver tests written before mid-turn steering drive the per-turn engine with a stub `claude` that
// ignores stdin, so the package runs with the kill switch on. The steering tests build their driver with
// newSteeringDriver, which turns the streaming engine on for that one driver.
func TestMain(m *testing.M) {
	_ = os.Setenv("BLERG_CLAUDE_STEERING", "0")
	// No test starts a CLI to read its --help: the fake would take it for a session. Tests of the
	// flag probe replace this themselves.
	ccHelpText = func(context.Context, sandboxExec, string, []string) ([]byte, error) {
		return nil, errors.New("no --help probe in tests")
	}
	// The marker key stays in memory: no test writes the daemon's real state directory.
	markerKeyDir = func() string { return "" }
	os.Exit(testsupport.Run(m))
}
