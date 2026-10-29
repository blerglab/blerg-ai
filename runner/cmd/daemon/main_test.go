package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
)

// TestSandboxNetworkSetting: unset means the compose default network, an
// explicit empty value disables joining any network, anything else is a name.
func TestSandboxNetworkSetting(t *testing.T) {
	t.Setenv("BLERG_RUNNER_SANDBOX_NETWORK", "")
	os.Unsetenv("BLERG_RUNNER_SANDBOX_NETWORK")
	if got := sandboxNetworkSetting(); got != "blerg-sandbox" {
		t.Errorf("unset: got %q, want %q", got, "blerg-sandbox")
	}
	t.Setenv("BLERG_RUNNER_SANDBOX_NETWORK", "")
	if got := sandboxNetworkSetting(); got != "" {
		t.Errorf("empty: got %q, want disabled", got)
	}
	t.Setenv("BLERG_RUNNER_SANDBOX_NETWORK", "my-agents")
	if got := sandboxNetworkSetting(); got != "my-agents" {
		t.Errorf("custom: got %q, want %q", got, "my-agents")
	}
	// Docker's special network modes (and anything that would parse as a
	// flag) would drop the sandbox's isolation: refused, treated as disabled.
	for _, bad := range []string{"host", "none", "bridge", "HOST", "container:abc", "container:", "-v", "--privileged"} {
		t.Setenv("BLERG_RUNNER_SANDBOX_NETWORK", bad)
		if got := sandboxNetworkSetting(); got != "" {
			t.Errorf("%q: got %q, want disabled", bad, got)
		}
	}
	// Ordinary names that merely resemble a special mode are kept.
	for _, ok := range []string{"blerg-sandbox", "my-bridge-net", "hostnet", "none-such", "containers"} {
		t.Setenv("BLERG_RUNNER_SANDBOX_NETWORK", ok)
		if got := sandboxNetworkSetting(); got != ok {
			t.Errorf("%q: got %q, want it kept", ok, got)
		}
	}
}

func TestPrependPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	bin := "/home/u/.local/bin"

	tests := []struct {
		name string
		list string
		dir  string
		want string
	}{
		{"empty list", "", bin, bin},
		{"empty dir is no-op", "/usr/bin", "", "/usr/bin"},
		{"prepends when missing", "/usr/bin" + sep + "/bin", bin, bin + sep + "/usr/bin" + sep + "/bin"},
		{"no change when already first", bin + sep + "/usr/bin", bin, bin + sep + "/usr/bin"},
		{"no change when already present later", "/usr/bin" + sep + bin, bin, "/usr/bin" + sep + bin},
		{"does not match prefix substring", "/usr/bin" + sep + bin + "/extra", bin, bin + sep + "/usr/bin" + sep + bin + "/extra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prependPath(tt.list, tt.dir); got != tt.want {
				t.Errorf("prependPath(%q, %q) = %q, want %q", tt.list, tt.dir, got, tt.want)
			}
		})
	}
}

// TestServerHTTPSetting: BLERG_RUNNER_SERVER_HTTP wins as it is; without it the
// base is derived from a deprecated BLERG_RUNNER_PREVIEW_URL ending in
// /api/preview, and from nothing else.
func TestServerHTTPSetting(t *testing.T) {
	for _, c := range []struct {
		name, serverHTTP, previewURL, want string
		derived                            bool
	}{
		{"neither set", "", "", "", false},
		{"server http only", "http://runner.example.test", "", "http://runner.example.test", false},
		{"server http wins", "http://runner.example.test", "http://other.example.test/api/preview", "http://runner.example.test", false},
		{"derived from the preview url", "", "http://runner.example.test/api/preview", "http://runner.example.test", true},
		{"trailing slash and spaces", "", " https://runner.example.test/base/api/preview/ ", "https://runner.example.test/base", true},
		{"not the preview endpoint", "", "http://runner.example.test/preview", "", false},
		{"only the suffix", "", "/api/preview", "", false},
	} {
		got, derived := serverHTTPSetting(c.serverHTTP, c.previewURL)
		if got != c.want || derived != c.derived {
			t.Errorf("%s: serverHTTPSetting(%q, %q) = %q, %v; want %q, %v",
				c.name, c.serverHTTP, c.previewURL, got, derived, c.want, c.derived)
		}
	}
}

// `status --open-work` lists, from the recovery records, the sessions a restart would cut off;
// it prints nothing when there are none, and an argument it does not know never starts a daemon.
func TestStatusOpenWork(t *testing.T) {
	state := t.TempDir()
	t.Setenv(daemon.DaemonStateDirEnv, state)
	t.Setenv("BLERG_RUNNER_SESSION_ID", "")
	run := func(args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		code := runCommand(args, &out, &errOut)
		return out.String(), errOut.String(), code
	}
	if out, _, code := run("status", "--open-work"); code != 0 || out != "" {
		t.Fatalf("no records: %q (exit %d)", out, code)
	}

	dir := daemon.AgentRecordsDir(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(rec map[string]any) {
		t.Helper()
		rec["version"] = 1
		data, _ := json.Marshal(rec)
		if err := os.WriteFile(filepath.Join(dir, rec["session_id"].(string)+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"session_id": "idle", "repo": "proj", "work_dir": "/w"})
	write(map[string]any{"session_id": "busy", "repo": "proj", "work_dir": "/w", "turn_active": true,
		"open_tasks": []map[string]any{
			{"task_id": "bu90kf38e", "call_id": "toolu_sh", "kind": "bash", "description": "Wait for\x1b[2J the\tdeploy\u0007"},
			{"task_id": "a5ef", "call_id": "toolu_bg", "kind": "agent", "description": "Fix the parser"},
		},
		"open_subagents": []map[string]any{{"child_id": "toolu_bg", "agent_type": "general-purpose", "summary": "Fix the parser", "agent_id": "a5ef"}},
	})
	tmp := filepath.Join(dir, "busy.json.1.1.tmp")
	if err := os.WriteFile(tmp, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := run("status", "--open-work")
	want := "session busy (proj): a turn is running, 1 background task, 1 subagent\n" +
		"  - background bash task bu90kf38e: Wait for [2J the deploy\n" + // no control character reaches the terminal
		"  - subagent general-purpose: Fix the parser\n"
	if code != 0 || out != want {
		t.Fatalf("open work (exit %d):\n got  %q\n want %q", code, out, want)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Error("the report removed a file of the running daemon's")
	}

	// Asked from inside the busy session (an agent updating the daemon): its own turn is the one
	// asking; its background work is still listed. A session with only its turn running is left out.
	t.Setenv("BLERG_RUNNER_SESSION_ID", "busy")
	out, _, _ = run("status", "--open-work")
	if !strings.HasPrefix(out, "session busy (proj): 1 background task, 1 subagent\n") {
		t.Errorf("from inside the session: %q", out)
	}
	write(map[string]any{"session_id": "busy", "repo": "proj", "work_dir": "/w", "turn_active": true})
	if out, _, _ = run("status", "--open-work"); out != "" {
		t.Errorf("only the asking session's own turn: %q", out)
	}
	t.Setenv("BLERG_RUNNER_SESSION_ID", "")

	for _, args := range [][]string{{"status"}, {"--open-work"}, {"run"}, {"status", "--open-work", "x"}} {
		out, errOut, code := run(args...)
		if code != 2 || out != "" || !strings.Contains(errOut, "Usage:") {
			t.Errorf("%v: exit %d, out %q, err %q", args, code, out, errOut)
		}
	}
	if out, _, code := run("--help"); code != 0 || !strings.Contains(out, "status --open-work") {
		t.Errorf("--help: exit %d, %q", code, out)
	}
}

func TestClaudeUsageSetting(t *testing.T) {
	for v, want := range map[string]bool{"": true, "1": true, "true": true, "on": true, "0": false, "false": false, "OFF": false, "no": false} {
		if got := claudeUsageSetting(v); got != want {
			t.Errorf("claudeUsageSetting(%q) = %v, want %v", v, got, want)
		}
	}
}

// Removed receipt commands must reject attempts to certify a session call, without
// provisioning, connecting, locking records or writing state.
func TestUpdateReceiptCommandsRejected(t *testing.T) {
	state := t.TempDir()
	t.Setenv("BLERG_RUNNER_DAEMON_STATE_DIR", state)
	t.Setenv("BLERG_RUNNER_SESSION_ID", "untrusted-claim")
	for _, args := range [][]string{{"note-update", "previous-binary"}, {"note-update-failed"}, {"protected-check"}, {"protected-supervisor", "state"}, {"protected-update", "state", "binary"}, {"protected-setup"}} {
		var out, errOut bytes.Buffer
		if code := runCommand(args, &out, &errOut); code != 2 {
			t.Fatalf("%v: exit %d, want unknown command: %s", args, code, errOut.String())
		}
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unattributed receipt command wrote daemon state: %v", entries)
	}
}
