package runner

import "testing"

// Every git command the pod runner runs in the workspace ignores the repository's own settings for the keys
// that make git execute something: a hook, an fsmonitor command, a credential helper. The session's agent
// owns the working tree, so anything it can write there must not be able to run code in the runner process.
func TestGitRunIgnoresRepoSettingsThatExecuteCode(t *testing.T) {
	dir := t.TempDir()
	if _, err := gitRun(dir, "init", "-q"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	// what a hijacked agent might have written into .git/config
	for _, kv := range [][2]string{{"core.hooksPath", "/tmp/evil-hooks"}, {"core.fsmonitor", "/tmp/evil-monitor"}, {"credential.helper", "!/tmp/evil"}} {
		if out, err := gitRun(dir, "config", "--local", kv[0], kv[1]); err != nil {
			t.Fatalf("set %s: %v: %s", kv[0], err, out)
		}
	}
	for key, want := range map[string]string{"core.hooksPath": "/dev/null", "core.fsmonitor": "false"} {
		got, err := gitRun(dir, "config", "--get", key)
		if err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q: the command line must beat the repository's setting", key, got, err, want)
		}
	}
}
