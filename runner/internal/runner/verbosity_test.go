package runner

import (
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
)

func TestTakeVerbosityEnv(t *testing.T) {
	for _, value := range []string{"narrated", "standard", "terse", "unknown", ""} {
		t.Setenv(daemon.VerbosityEnvVar, value)
		want := value
		if value == "unknown" || value == "" {
			want = "standard"
		}
		if got := takeVerbosityEnv(); got != want {
			t.Errorf("%q: %q, want %q", value, got, want)
		}
		if _, present := os.LookupEnv(daemon.VerbosityEnvVar); present {
			t.Fatal("verbosity environment was not consumed")
		}
	}
}
