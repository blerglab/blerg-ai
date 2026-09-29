package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// BashEnv must give the shell exactly the environment it was handed: a value
// present in env is visible, and a value in the host process env but absent
// from env (the daemon master token, say) is not.
func TestBashEnvUsesGivenEnvironmentOnly(t *testing.T) {
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "host-only-secret")
	env := []string{"PATH=" + os.Getenv("PATH"), "SESSION_VISIBLE=yes"}
	tool := BashEnv(t.TempDir(), env)
	in, _ := json.Marshal(map[string]any{"command": `echo "vis=$SESSION_VISIBLE tok=$BLERG_RUNNER_DAEMON_TOKEN"`})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "vis=yes") {
		t.Fatalf("given env not visible: %q", out)
	}
	if strings.Contains(out, "host-only-secret") {
		t.Fatalf("host env leaked into the shell: %q", out)
	}

	// nil keeps Bash's inherit-the-host behaviour for other callers.
	inherit := Bash(t.TempDir())
	out, err = inherit.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("run (inherit): %v", err)
	}
	if !strings.Contains(out, "host-only-secret") {
		t.Fatalf("Bash(nil env) should inherit the host env; output = %q", out)
	}
}
