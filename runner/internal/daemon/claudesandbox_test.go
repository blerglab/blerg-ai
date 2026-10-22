package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// claudeSandboxFixture is a person's home whose Claude Code settings turn its sandbox on (with a
// setting of their own beside it), and an empty managed-settings directory.
func claudeSandboxFixture(t *testing.T, settings string) (home, settingsPath string) {
	t.Helper()
	home = t.TempDir()
	managed := t.TempDir()
	prev := claudeManagedSettingsDir
	claudeManagedSettingsDir = func() string { return managed }
	t.Cleanup(func() { claudeManagedSettingsDir = prev })
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	settingsPath = filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	// An old timestamp, so a rewrite would show even within the same second.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(settingsPath, old, old); err != nil {
		t.Fatal(err)
	}
	return home, settingsPath
}

func assertUntouched(t *testing.T, path, want string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != want || time.Since(info.ModTime()) < 30*time.Minute {
		t.Fatalf("%s was changed: %s", path, raw)
	}
}

var sandboxOnSettings = `{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true},"model":"opus"}`

func TestClaudeSandboxExceptionAllowsTheRunnersHostAndPort(t *testing.T) {
	home, settings := claudeSandboxFixture(t, sandboxOnSettings)
	check := func(env []string, want string) {
		t.Helper()
		got, note := claudeSandboxException(env, home, "")
		raw, _ := json.Marshal(got)
		if string(raw) != want || note != "" {
			t.Errorf("%v: exception = %s (note %q), want %s", env, raw, note, want)
		}
	}
	check([]string{"BLERG_RUNNER_SERVER_URL=ws://localhost:8083/ws/daemon", "BLERG_BOARD_URL=http://127.0.0.1:8082/board", "BLERG_RUNNER_SESSION_ID=s1"},
		`{"network":{"allowedDomains":["127.0.0.1:8082","localhost:8083"]}}`)
	// SERVER_HTTP, when the session has it, is what the CLI uses.
	check([]string{"BLERG_RUNNER_SERVER_URL=ws://localhost:8083/ws/daemon", "BLERG_RUNNER_SERVER_HTTP=http://[::1]:9000"},
		`{"network":{"allowedDomains":["[::1]:9000"]}}`)
	// An address without a port gets its scheme's: never a bare host (a bare localhost would open
	// every local port).
	check([]string{"BLERG_RUNNER_SERVER_URL=wss://runner.example.com/ws/daemon"}, `{"network":{"allowedDomains":["runner.example.com:443"]}}`)
	check([]string{"BLERG_RUNNER_SERVER_HTTP=http://localhost"}, `{"network":{"allowedDomains":["localhost:80"]}}`)
	check([]string{"BLERG_RUNNER_SERVER_URL=ws://127.0.0.1/ws"}, `{"network":{"allowedDomains":["127.0.0.1:80"]}}`)
	// No address: nothing is added.
	check([]string{"BLERG_RUNNER_SESSION_ID=s1"}, `null`)
	assertUntouched(t, settings, sandboxOnSettings)
}

func TestClaudeSandboxExceptionOnlyWhenTheSandboxIsOn(t *testing.T) {
	env := []string{"BLERG_RUNNER_SERVER_HTTP=http://localhost:8083"}
	for _, settings := range []string{`{}`, `{"sandbox":{"enabled":false}}`, `not json`} {
		home, _ := claudeSandboxFixture(t, settings)
		if got, note := claudeSandboxException(env, home, t.TempDir()); got != nil || note != "" {
			t.Errorf("%s: exception %v %q for a sandbox that is off", settings, got, note)
		}
	}
	// The project's settings turn it on too.
	home, _ := claudeSandboxFixture(t, `{}`)
	work := t.TempDir()
	writeFile(t, filepath.Join(work, ".claude", "settings.local.json"), `{"sandbox":{"enabled":true}}`)
	if got, _ := claudeSandboxException(env, home, work); got == nil {
		t.Error("a project that turns the sandbox on got no exception")
	}
}

// When managed settings allow only their own domains, the policy is not overridden: no exception
// at all (nothing excluded from the sandbox), and a note naming what to allow.
func TestClaudeSandboxExceptionWithManagedDomainsOnly(t *testing.T) {
	home, settings := claudeSandboxFixture(t, sandboxOnSettings)
	writeFile(t, filepath.Join(claudeManagedSettingsDir(), "managed-settings.json"), `{"sandbox":{"network":{"allowManagedDomainsOnly":true}}}`)
	got, note := claudeSandboxException([]string{"BLERG_RUNNER_SERVER_HTTP=http://localhost:8083"}, home, "")
	if got != nil {
		t.Fatalf("a managed-domains-only policy was overridden: %v", got)
	}
	if !strings.Contains(note, "localhost:8083") || !strings.Contains(note, "administrator") {
		t.Fatalf("note = %q", note)
	}
	if args := claudeTerminalSandboxArgs("s1", []string{"BLERG_RUNNER_SERVER_HTTP=http://localhost:8083"}, home, "", false); args != nil {
		t.Fatalf("terminal args = %q", args)
	}
	assertUntouched(t, settings, sandboxOnSettings)
}

// The agent session is told, in its transcript, when its managed policy keeps it from Blerg.
func TestAgentSessionIsToldWhenManagedPolicyBlocksTheRunner(t *testing.T) {
	engineDir := fakeClaude(t)
	t.Setenv("PATH", engineDir+":"+os.Getenv("PATH"))
	claudeLog := filepath.Join(engineDir, "calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
	t.Setenv("BLERG_RUNNER_SERVER_URL", "ws://localhost:8083/ws/daemon")
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", "")
	home, _ := claudeSandboxFixture(t, sandboxOnSettings)
	writeFile(t, filepath.Join(claudeManagedSettingsDir(), "managed-settings.json"), `{"sandbox":{"network":{"allowManagedDomainsOnly":true}}}`)
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: home, ClaudeCode: true})
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-managed", Repo: "proj", Kind: "agent", InitialPrompt: "hello"})
	sender.waitForKind(t, "turn_done")
	host.Kill("s-managed")
	if !sender.hasErrorContaining("allow only managed domains") {
		t.Fatal("the session was not told its managed policy blocks the runner")
	}
	raw, _ := os.ReadFile(claudeLog)
	if strings.Contains(string(raw), `"sandbox"`) || strings.Contains(string(raw), "excludedCommands") {
		t.Fatalf("the session's settings override the managed policy: %s", raw)
	}
}

// A sandboxed Claude agent session's own --settings carry the exception beside the progress hook,
// and the person's settings file is untouched; with the sandbox off the settings are as before.
func TestAgentSessionSettingsCarryTheSandboxException(t *testing.T) {
	for _, on := range []bool{true, false} {
		engineDir := fakeClaude(t)
		t.Setenv("PATH", engineDir+":"+os.Getenv("PATH"))
		claudeLog := filepath.Join(engineDir, "calls.log")
		t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
		t.Setenv("BLERG_RUNNER_SERVER_URL", "ws://localhost:8083/ws/daemon")
		t.Setenv("BLERG_RUNNER_SERVER_HTTP", "")
		settingsBody := `{}`
		if on {
			settingsBody = sandboxOnSettings
		}
		home, settings := claudeSandboxFixture(t, settingsBody)
		reposRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
			t.Fatal(err)
		}
		sender := &agentTestSender{}
		host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: home, ClaudeCode: true})
		host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-cc-sandbox", Repo: "proj", Kind: "agent", InitialPrompt: "hello"})
		sender.waitForKind(t, "turn_done")
		host.Kill("s-cc-sandbox")
		raw, err := os.ReadFile(claudeLog)
		if err != nil {
			t.Fatal(err)
		}
		var sessionSettings map[string]any
		i := strings.Index(string(raw), "--settings=")
		if i < 0 {
			t.Fatalf("on=%v: no session settings: %s", on, raw)
		}
		if err := json.NewDecoder(strings.NewReader(string(raw)[i+len("--settings="):])).Decode(&sessionSettings); err != nil {
			t.Fatalf("session settings do not parse: %v", err)
		}
		if sessionSettings["hooks"] == nil {
			t.Fatalf("on=%v: the progress hook is gone: %v", on, sessionSettings)
		}
		got, _ := json.Marshal(sessionSettings["sandbox"])
		want := `{"network":{"allowedDomains":["localhost:8083"]}}`
		if !on {
			want = "null"
		}
		if string(got) != want {
			t.Fatalf("on=%v: session sandbox settings = %s, want %s", on, got, want)
		}
		assertUntouched(t, settings, settingsBody)
	}
}

func TestTerminalSandboxArgs(t *testing.T) {
	home, settings := claudeSandboxFixture(t, sandboxOnSettings)
	env := []string{"BLERG_RUNNER_SERVER_HTTP=http://localhost:8083"}
	got := claudeTerminalSandboxArgs("s1", env, home, "", false)
	if len(got) != 2 || got[0] != "--settings" || got[1] != `{"sandbox":{"network":{"allowedDomains":["localhost:8083"]}}}` {
		t.Fatalf("terminal args = %q", got)
	}
	if got := claudeTerminalSandboxArgs("s1", env, home, "", true); got != nil {
		t.Fatalf("a Docker-sandboxed terminal got %q", got)
	}
	assertUntouched(t, settings, sandboxOnSettings)
}

// hasErrorContaining reports whether an error event in the session's transcript says text.
func (s *agentTestSender) hasErrorContaining(text string) bool {
	for _, ev := range s.agentEvents() {
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), `"error"`) && strings.Contains(string(raw), text) {
			return true
		}
	}
	return false
}
