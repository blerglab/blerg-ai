package daemon

import (
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestChooseAgentDriver(t *testing.T) {
	host := agentDriverInputs{PreferClaudeCLI: true} // a desktop daemon
	with := func(f func(*agentDriverInputs)) agentDriverInputs { in := host; f(&in); return in }
	cases := []struct {
		name string
		in   agentDriverInputs
		want agentDriverKind
	}{
		{"host + claude on PATH (no key): the user's login", with(func(i *agentDriverInputs) { i.ClaudeOnPath = true }), driverClaudeCode},
		{"host + claude on PATH + key: still the login", with(func(i *agentDriverInputs) { i.ClaudeOnPath = true; i.HasAPIKey = true }), driverClaudeCode},
		{"host + engine=claude explicitly", with(func(i *agentDriverInputs) { i.Engine = "claude"; i.ClaudeOnPath = true }), driverClaudeCode},
		{"host + no claude + key: native loop", with(func(i *agentDriverInputs) { i.HasAPIKey = true }), driverNative},
		{"host + no claude + no key: refused", host, driverNone},
		{"sandbox: Claude Code in the container", with(func(i *agentDriverInputs) { i.Sandbox = true }), driverClaudeCode},
		{"sandbox beats a test provider", with(func(i *agentDriverInputs) { i.Sandbox = true; i.ProviderSet = true }), driverClaudeCode},
		{"cluster pod, subscription mode", agentDriverInputs{ClaudeCodeForced: true}, driverClaudeCode},
		{"cluster pod, API key mode (claude in the image)", agentDriverInputs{HasAPIKey: true, ClaudeOnPath: true}, driverNative},
		{"test provider on the host", with(func(i *agentDriverInputs) { i.ProviderSet = true; i.ClaudeOnPath = true }), driverNative},
		{"codex CLI", with(func(i *agentDriverInputs) { i.Engine = "codex" }), driverCLI},
		{"hermes CLI", with(func(i *agentDriverInputs) { i.Engine = "hermes" }), driverCLI},
		{"openclaw CLI", with(func(i *agentDriverInputs) { i.Engine = "openclaw" }), driverCLI},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, refusal := chooseAgentDriver(c.in)
			if got != c.want {
				t.Fatalf("driver = %v, want %v", got, c.want)
			}
			if (got == driverNone) != (refusal != "") {
				t.Fatalf("refusal %q for driver %v", refusal, got)
			}
		})
	}
	_, refusal := chooseAgentDriver(host)
	if !strings.Contains(refusal, "claude") || !strings.Contains(refusal, "ANTHROPIC_API_KEY") {
		t.Errorf("refusal must name both fixes: %q", refusal)
	}
}

// The owner's bug: a desktop Claude agent session with no API key was
// refused ("native agent sessions need ANTHROPIC_API_KEY") even with Claude
// Code installed and logged in. It now runs the claude CLI, and the engine
// preflight (and so recovery) agrees.
func TestHostClaudeAgentSessionUsesTheClaudeCLI(t *testing.T) {
	prev := claudeOnPath
	claudeOnPath = func() bool { return true }
	t.Cleanup(func() { claudeOnPath = prev })

	h := NewAgentHost(&agentTestSender{}, AgentHostConfig{PreferClaudeCLI: true, ReposRoot: t.TempDir(), HomeDir: t.TempDir()})
	msg := protocol.SpawnSession{SessionID: "s1", Repo: "proj", Kind: "agent", Model: "claude-opus-5", Effort: "high"}
	if kind, refusal := h.driverFor(msg); kind != driverClaudeCode {
		t.Fatalf("driver = %v (%s), want the Claude Code driver", kind, refusal)
	}
	if got := h.engineBinary(msg); got != "claude" {
		t.Fatalf("preflight binary = %q, want claude", got)
	}
	// model/effort still reach the CLI.
	args := strings.Join(ccTurnArgs("hi", "claude-opus-5", "high", ""), " ")
	if !strings.Contains(args, "--model claude-opus-5") || !strings.Contains(args, "high") {
		t.Fatalf("turn args %q lack model/effort", args)
	}

	claudeOnPath = func() bool { return false }
	if kind, refusal := h.driverFor(msg); kind != driverNone || !strings.Contains(refusal, "ANTHROPIC_API_KEY") {
		t.Fatalf("no claude, no key: driver = %v, refusal %q", kind, refusal)
	}
}

// With neither the CLI nor a key the spawn is refused with the actionable
// reason, not the old "select another engine" text.
func TestHostClaudeAgentSpawnRefusalNamesBothFixes(t *testing.T) {
	prev := claudeOnPath
	claudeOnPath = func() bool { return false }
	t.Cleanup(func() { claudeOnPath = prev })

	sender := &agentTestSender{}
	reposRoot := t.TempDir()
	h := NewAgentHost(sender, AgentHostConfig{PreferClaudeCLI: true, ReposRoot: reposRoot, HomeDir: t.TempDir()})
	if err := mkdirAllT(reposRoot + "/proj"); err != nil {
		t.Fatal(err)
	}
	h.Spawn(protocol.SpawnSession{SessionID: "s1", Repo: "proj", Kind: "agent"})
	sender.mu.Lock()
	defer sender.mu.Unlock()
	for _, m := range sender.msgs {
		if st, ok := m.(protocol.SessionStateChanged); ok && st.Status == "error" && st.Message != nil {
			if strings.Contains(*st.Message, "install Claude Code") && strings.Contains(*st.Message, "ANTHROPIC_API_KEY") {
				return
			}
			t.Fatalf("refusal = %q", *st.Message)
		}
	}
	t.Fatal("no refusal sent")
}
