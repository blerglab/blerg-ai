package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tmux seeds its server-global environment from the FIRST new-session only;
// later panes inherit the server's env, not the cmd.Env of their own
// new-session. So per-session vars must ride inside the pane command. The
// argv builder wraps the inner command in `env KEY=VAL … <inner>` carrying
// every BLERG_RUNNER_* entry — never the master token, never anything else.
func TestTmuxInnerWithEnvCarriesSessionVarsOnly(t *testing.T) {
	env := []string{
		"HOME=/home/dev",
		"BLERG_RUNNER_DAEMON_TOKEN=master-must-not-appear",
		"BLERG_RUNNER_SESSION_ID=s1",
		"BLERG_RUNNER_SESSION_TOKEN=tok-1",
		"BLERG_RUNNER_SERVER_HTTP=http://srv",
		"ANTHROPIC_API_KEY=sk-ant",
		"BLERG_BOARD_URL=http://board",
	}
	inner := []string{"claude", "--model", "opus"}
	got := tmuxInnerWithEnv(inner, env)

	if got[0] != "env" {
		t.Fatalf("argv[0] = %q, want env: %v", got[0], got)
	}
	joined := strings.Join(got, "\x00")
	for _, want := range []string{
		"BLERG_RUNNER_SESSION_ID=s1",
		"BLERG_RUNNER_SESSION_TOKEN=tok-1",
		"BLERG_RUNNER_SERVER_HTTP=http://srv",
	} {
		if !hasEntry(got, want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	for _, bad := range []string{"master-must-not-appear", "HOME=", "ANTHROPIC_API_KEY", "BLERG_BOARD_URL"} {
		if strings.Contains(joined, bad) {
			t.Errorf("%q leaked into the pane argv: %v", bad, got)
		}
	}
	if tail := got[len(got)-len(inner):]; strings.Join(tail, "\x00") != strings.Join(inner, "\x00") {
		t.Errorf("inner command not preserved: %v", got)
	}

	// A literal argv (tmux execvp's a multi-argument pane command): a hostile
	// value stays one argument, verbatim — no quoting, no shell to break out of.
	q := tmuxInnerWithEnv([]string{"sh"}, []string{"BLERG_RUNNER_X=it's; rm -rf /"})
	if len(q) != 3 || q[1] != "BLERG_RUNNER_X=it's; rm -rf /" {
		t.Errorf("hostile value argv = %q", q)
	}

	// No session vars at all: the wrapper degrades to a bare `env <inner>`,
	// still ≥ 2 arguments so tmux never falls back to `sh -c`.
	if bare := tmuxInnerWithEnv(inner, []string{"HOME=/x"}); len(bare) != len(inner)+1 {
		t.Errorf("bare wrapper = %v", bare)
	}
}

// The sandbox forwards env into `docker run` with -e: only BLERG_RUNNER_*
// entries, and never the daemon master token even if a caller passed it.
func TestSandboxEnvArgsForwardsSessionTokenNeverMaster(t *testing.T) {
	args := sandboxEnvArgs([]string{
		"HOME=/home/dev",
		"BLERG_RUNNER_DAEMON_TOKEN=master",
		"BLERG_RUNNER_SESSION_TOKEN=tok-1",
		"BLERG_RUNNER_SESSION_ID=s1",
		"ANTHROPIC_API_KEY=sk",
	})
	want := []string{"-e", "BLERG_RUNNER_SESSION_TOKEN=tok-1", "-e", "BLERG_RUNNER_SESSION_ID=s1"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("sandboxEnvArgs = %v, want %v", args, want)
	}
}

// Real tmux: two sessions on a private server, each with its own token, and a
// server whose global env already carries a master token (as one started by
// an old daemon or the user's shell would). Each pane must see exactly its own
// BLERG_RUNNER_SESSION_TOKEN / SESSION_ID and no master token.
func TestCreateTmuxSessionDeliversPerSessionEnv(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	var rb [4]byte
	_, _ = rand.Read(rb[:])
	sock := "blerg-test-" + hex.EncodeToString(rb[:])
	restore := setTmuxSocketArgs([]string{"-L", sock})
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", sock, "kill-server").Run()
		restore()
	})
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "ambient-master")

	// A pre-existing server with the master token in its global env.
	if err := exec.Command("tmux", "-L", sock, "new-session", "-d", "-s", "seed").Run(); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	if err := exec.Command("tmux", "-L", sock, "set-environment", "-g", "BLERG_RUNNER_DAEMON_TOKEN", "master-in-tmux").Run(); err != nil {
		t.Fatalf("seed global env: %v", err)
	}
	if err := exec.Command("tmux", "-L", sock, "set-environment", "-g", "BLERG_RUNNER_SESSION_TOKEN", "stale-token").Run(); err != nil {
		t.Fatalf("seed global env: %v", err)
	}
	scrubTmuxGlobalEnv() // what the daemon does at connect

	dir := t.TempDir()
	type want struct{ id, tok string }
	cases := []want{{"envtest-a-" + sock, "tok-a"}, {"envtest-b-" + sock, "tok-b"}}
	for _, c := range cases {
		out := filepath.Join(dir, c.id+".env")
		// Raw argv: tmux execvp's it, sh receives the script as one argument.
		inner := []string{"sh", "-c", "printenv > " + out + "; sleep 60"}
		env := sanitizedEnviron("BLERG_RUNNER_SESSION_ID="+c.id, "BLERG_RUNNER_SESSION_TOKEN="+c.tok)
		if err := createTmuxSession(c.id, dir, inner, env); err != nil {
			t.Fatalf("createTmuxSession %s: %v", c.id, err)
		}
	}
	for _, c := range cases {
		got := waitForEnvFile(t, filepath.Join(dir, c.id+".env"))
		if got["BLERG_RUNNER_SESSION_TOKEN"] != c.tok || got["BLERG_RUNNER_SESSION_ID"] != c.id {
			t.Errorf("%s: pane env token=%q id=%q, want %q/%q", c.id, got["BLERG_RUNNER_SESSION_TOKEN"], got["BLERG_RUNNER_SESSION_ID"], c.tok, c.id)
		}
		if v, ok := got["BLERG_RUNNER_DAEMON_TOKEN"]; ok {
			t.Errorf("%s: master token visible in pane: %q", c.id, v)
		}
	}
}

func waitForEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			env := map[string]string{}
			for _, line := range strings.Split(string(data), "\n") {
				if k, v, ok := strings.Cut(line, "="); ok {
					env[k] = v
				}
			}
			return env
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never wrote %s: %v", path, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
