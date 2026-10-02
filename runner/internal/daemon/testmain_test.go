package daemon

import (
	"os"
	"testing"
)

// The driver tests written before mid-turn steering drive the per-turn engine with a stub `claude` that
// ignores stdin, so the package runs with the kill switch on. The steering tests build their driver with
// newSteeringDriver, which turns the streaming engine on for that one driver.
func TestMain(m *testing.M) {
	_ = os.Setenv("BLERG_CLAUDE_STEERING", "0")
	os.Exit(m.Run())
}
