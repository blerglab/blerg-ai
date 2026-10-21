package daemon

// Session preparation runs an enabled Claude plugin's SessionStart hook only
// where its output is used and the session is not confined: the native API
// loop on the host, unrestricted. Every other engine × confinement pair must
// start without the hook ever running. Synthetic plugin and engines only.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// plantStartupHook writes a synthetic plugin into homeDir's plugin cache,
// enables it for workDir, and returns the file its SessionStart hook creates.
func plantStartupHook(t *testing.T, homeDir, workDir string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "hook-ran")
	pluginDir := filepath.Join(homeDir, ".claude", "plugins", "cache", "mp", "startup-marker", "1.0.0")
	if err := os.MkdirAll(filepath.Join(pluginDir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo ran >> '` + marker + `'; echo startup-hook-context"}]}]}}`
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks", "hooks.json"), []byte(hooks), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".claude", "settings.json"), []byte(`{"enabledPlugins":["startup-marker"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return marker
}

// waitStartedOrRefused blocks until the session's first turn finishes or the
// spawn is refused, and reports which.
func (s *agentTestSender) waitStartedOrRefused(t *testing.T) (started bool, refusal string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if errs := s.sessionErrors(); len(errs) > 0 {
			return false, errs[0]
		}
		for _, ev := range s.agentEvents() {
			if ev.Kind == "turn_done" {
				return true, ""
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session neither finished a turn nor was refused; kinds %v", s.kinds())
	return false, ""
}

func TestSessionStartHooksRunOnlyForHostNativeSessions(t *testing.T) {
	type mode int
	const (
		host mode = iota
		restricted
		sandboxed
	)
	cases := []struct {
		name        string
		engine      string // "" with a provider: the native loop; "claude-code": the Claude Code driver
		mode        mode
		wantStarted bool
		wantHook    bool
	}{
		{"claude-code/host", "claude-code", host, true, false},
		{"claude-code/restricted", "claude-code", restricted, true, false},
		{"claude-code/sandboxed", "claude-code", sandboxed, true, false},
		{"codex/host", "codex", host, true, false},
		{"codex/restricted", "codex", restricted, true, false},
		{"codex/sandboxed", "codex", sandboxed, true, false},
		{"native/host", "", host, true, true},
		// Refused before anything is prepared: the native loop has no restricted mode.
		{"native/restricted", "", restricted, false, false},
		// A sandbox never runs the native loop (its tools act on host paths): Claude Code runs in
		// the container instead, and preparing it runs no host hook.
		{"native/sandboxed", "", sandboxed, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.engine == "codex" && tc.mode == restricted && runtime.GOOS != "linux" {
				t.Skip("Linux confinement required")
			}
			cfg := AgentHostConfig{}
			if tc.engine == "claude-code" {
				cfg.ClaudeCode = true
			}
			if tc.engine == "" {
				cfg.Provider = &scriptedAgentProvider{}
			}
			engineDirs := fakeClaude(t) + ":" + fakeCodex(t)
			h, sender, _, home := sandboxHost(t, engineDirs, cfg)
			t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(t.TempDir(), "claude-calls.log"))
			t.Setenv("FAKE_CODEX_LOG", filepath.Join(t.TempDir(), "codex-calls.log"))
			if tc.mode == sandboxed {
				writeClaudeLogin(t, home)
			}
			if tc.engine == "codex" && tc.mode == restricted {
				auth := t.TempDir()
				if err := os.WriteFile(filepath.Join(auth, "auth.json"), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CODEX_HOME", auth)
			}
			workDir := filepath.Join(h.cfg.ReposRoot, "proj")
			marker := plantStartupHook(t, h.cfg.HomeDir, workDir)
			if tc.mode == sandboxed {
				// A sandbox works in a clone of the repository: the planted hook is committed so
				// the clone has it too.
				initChildRepo(t, workDir)
			}

			msg := protocol.SpawnSession{
				Type: "spawn_session", SessionID: "s-" + strings.ReplaceAll(tc.name, "/", "-"),
				Repo: "proj", Kind: "agent", InitialPrompt: "hello",
				RestrictTools: tc.mode == restricted, Sandbox: tc.mode == sandboxed,
			}
			if tc.engine == "codex" {
				msg.Engine = "codex"
			}
			h.Spawn(msg)
			t.Cleanup(func() { h.Kill(msg.SessionID) })

			started, refusal := sender.waitStartedOrRefused(t)
			if started != tc.wantStarted {
				t.Fatalf("started = %v (refusal %q), want %v", started, refusal, tc.wantStarted)
			}
			_, err := os.Stat(marker)
			if ran := err == nil; ran != tc.wantHook {
				t.Fatalf("SessionStart hook ran = %v, want %v", ran, tc.wantHook)
			}
		})
	}
}

// The native loop's own backstop: a confined session's configuration runs no
// hook, an unconfined one carries the hook's context in its system prompt.
func TestNativeLoopConfigRunsHooksOnlyWhenUnconfined(t *testing.T) {
	h, _, workDir := newTestHost(t, &scriptedAgentProvider{}, "")
	marker := plantStartupHook(t, h.cfg.HomeDir, workDir)
	in := nativeLoopInputs{workDir: workDir, model: "claude-sonnet-5", confined: true}
	msg := protocol.SpawnSession{SessionID: "s-cfg", Repo: "proj", Kind: "agent"}

	cfg := h.nativeLoopConfig(msg, in)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a confined session's configuration ran the SessionStart hook")
	}
	if strings.Contains(cfg.System, "startup-hook-context") {
		t.Fatal("a confined session's prompt carries hook context")
	}

	in.confined = false
	cfg = h.nativeLoopConfig(msg, in)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("an unconfined native session's configuration did not run the SessionStart hook")
	}
	if !strings.Contains(cfg.System, "startup-hook-context") {
		t.Fatal("the hook's context is missing from the native prompt")
	}
}
