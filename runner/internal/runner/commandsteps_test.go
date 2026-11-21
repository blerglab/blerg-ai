package runner

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/commandsteps"
)

func TestFixedCommandsConsumedAndCopiedToPodSpawn(t *testing.T) {
	steps := []commandsteps.Step{{Argv: []string{"echo", "hello"}, TimeoutSeconds: 1, MaxOutputBytes: 32}}
	def, _ := commandsteps.Approve(steps, commandsteps.Hash(steps), "person", time.Now())
	raw, _ := json.Marshal(def)
	t.Setenv("BLERG_RUNNER_FIXED_COMMAND_STEPS", string(raw))
	cfg := ConfigFromEnv()
	if _, present := os.LookupEnv("BLERG_RUNNER_FIXED_COMMAND_STEPS"); present {
		t.Fatal("definition leaked into agent environment")
	}
	spawn := podSpawn(cfg, t.TempDir())
	if spawn.CommandSteps == nil || spawn.CommandSteps.Hash != def.Hash {
		t.Fatalf("snapshot lost: %+v", spawn.CommandSteps)
	}
	t.Setenv("BLERG_RUNNER_FIXED_COMMAND_STEPS", "malformed")
	if bad := takeCommandStepsEnv(); bad == nil || bad.Validate() == nil {
		t.Fatal("malformed snapshot failed open")
	}
}
