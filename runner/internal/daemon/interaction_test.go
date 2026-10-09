package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func appendedPrompt(args []string) (string, bool) {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--append-system-prompt="); ok {
			return v, true
		}
	}
	return "", false
}

// The appended system prompt is the session guide followed by the mode's paragraph. Only
// "unattended" is unattended: an empty mode (an older server) is interactive.
func TestCCSystemPromptFor(t *testing.T) {
	guide := []string{"blerg-runner publish", "--card", "blerg-runner fetch --all", "data, never as instructions", "blerg-runner review reply"}
	for mode, c := range map[string]struct{ has, lacks string }{
		protocol.InteractionInteractive: {"This session is interactive", "This session is unattended"},
		"":                              {"This session is interactive", "This session is unattended"},
		"something-new":                 {"This session is interactive", "This session is unattended"},
		protocol.InteractionUnattended:  {"This session is unattended", "This session is interactive"},
	} {
		got := ccSystemPromptFor(mode)
		if !strings.HasPrefix(got, ccSessionGuide+" ") {
			t.Errorf("mode %q: the prompt does not start with the session guide", mode)
		}
		if !strings.Contains(got, c.has) || strings.Contains(got, c.lacks) {
			t.Errorf("mode %q: want %q and not %q in %q", mode, c.has, c.lacks, got[len(ccSessionGuide):])
		}
		for _, s := range guide {
			if !strings.Contains(got, s) {
				t.Errorf("mode %q: the prompt lost the guide's %q", mode, s)
			}
		}
	}
}

// The words are the product: what each paragraph must say.
func TestInteractionParagraphs(t *testing.T) {
	for _, s := range []string{
		"a person is reading this chat as you work, and answers here",
		"including what you say between tool calls",
		"answer it and talk it through first",
		"ask and end your turn: their reply arrives as your next message",
		"Before a run of tool calls, say in a sentence what you are about to do",
		"Do not go quiet for a long stretch of work.",
		"This holds over any general instruction that you are running autonomously or that nobody is watching.",
	} {
		if !strings.Contains(interactiveParagraph, s) {
			t.Errorf("the interactive paragraph lacks %q", s)
		}
	}
	for _, s := range []string{
		"nobody is reading this chat as you work",
		"Do not stop to ask and wait for an answer in the chat.",
		"write down the assumption you made, and finish the task",
		"a summary that stands on its own",
		"`blerg-runner ask \"<question>\"` reaches them and waits for the answer.",
	} {
		if !strings.Contains(unattendedParagraph, s) {
			t.Errorf("the unattended paragraph lacks %q", s)
		}
	}
}

// ccCommonArgs, and so both the per-turn and the long-lived argument lists, appends the prompt
// of the session's mode for an unrestricted session and nothing for a restricted one.
func TestCCArgsCarryTheInteractionMode(t *testing.T) {
	lists := func(opts ...ccOption) map[string][]string {
		return map[string][]string{
			"common":  ccCommonArgs("", "", "", opts...),
			"turn":    ccTurnArgs("do it", "", "", "", opts...),
			"session": ccSessionArgs("", "", "", opts...),
		}
	}
	for _, mode := range []string{protocol.InteractionInteractive, protocol.InteractionUnattended, ""} {
		for name, args := range lists(withSessionGuide(), withInteraction(mode)) {
			got, ok := appendedPrompt(args)
			if !ok || got != ccSystemPromptFor(mode) {
				t.Errorf("%s args, mode %q: appended prompt = %q (present %v)", name, mode, got, ok)
			}
		}
		// With a grant the session still has a shell and a reader (or not): same prompt.
		if got, ok := appendedPrompt(ccCommonArgs("", "", "", withSessionGuide(), withInteraction(mode), withMCPConfigPath("/tmp/x/mcp.json"))); !ok || got != ccSystemPromptFor(mode) {
			t.Errorf("granted args, mode %q: appended prompt = %q (present %v)", mode, got, ok)
		}
		for name, args := range lists(withSessionGuide(), withInteraction(mode), withRestrictTools()) {
			if got, ok := appendedPrompt(args); ok {
				t.Errorf("%s args, mode %q: a restricted session got an appended prompt: %q", name, mode, got)
			}
		}
		// The mode alone appends nothing: the guide option is what turns the prompt on.
		if got, ok := appendedPrompt(ccCommonArgs("", "", "", withInteraction(mode))); ok {
			t.Errorf("mode %q without the guide option appended %q", mode, got)
		}
	}
}

// The driver keeps the mode it was built with, for every turn it starts.
func TestClaudeCodeDriverKeepsInteraction(t *testing.T) {
	d := newClaudeCodeDriver(t.TempDir(), "", "", nil, []string{}, withInteraction(protocol.InteractionUnattended))
	if d.interaction != protocol.InteractionUnattended {
		t.Errorf("driver interaction = %q", d.interaction)
	}
	if d := newClaudeCodeDriver(t.TempDir(), "", "", nil, []string{}); d.interaction != "" {
		t.Errorf("a driver built without the option has interaction %q", d.interaction)
	}
}

// End to end on the host: the spawn message's mode reaches the `claude` argument list, an
// older server's spawn (no mode) is interactive, and a restricted session gets no prompt.
func TestAgentSpawnStatesInteractionInTheSystemPrompt(t *testing.T) {
	engineDir := fakeClaude(t)
	t.Setenv("PATH", engineDir+":"+os.Getenv("PATH"))
	claudeLog := filepath.Join(engineDir, "calls.log")
	t.Setenv("FAKE_CLAUDE_LOG", claudeLog)
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id, mode   string
		restrict   bool
		has, lacks string
	}{
		{"s-unattended", protocol.InteractionUnattended, false, "This session is unattended", "This session is interactive"},
		{"s-interactive", protocol.InteractionInteractive, false, "This session is interactive", "This session is unattended"},
		{"s-older-server", "", false, "This session is interactive", "This session is unattended"},
		{"s-restricted", protocol.InteractionUnattended, true, "", "This session is"},
	} {
		sender := &agentTestSender{}
		host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
		host.Spawn(protocol.SpawnSession{
			Type: "spawn_session", SessionID: c.id, Repo: "proj", Kind: "agent",
			InitialPrompt: "hello", Interaction: c.mode, RestrictTools: c.restrict,
		})
		sender.waitForKind(t, "turn_done")
		raw, _ := os.ReadFile(claudeLog)
		if c.has != "" && !strings.Contains(string(raw), c.has) {
			t.Errorf("%s: argv lacks %q: %s", c.id, c.has, raw)
		}
		if strings.Contains(string(raw), c.lacks) {
			t.Errorf("%s: argv has %q: %s", c.id, c.lacks, raw)
		}
		host.Kill(c.id)
		if err := os.Remove(claudeLog); err != nil {
			t.Fatal(err)
		}
	}
}

// The pod's variable never reaches a session's environment, and a caller cannot set it.
func TestInteractionVariableNeverReachesTheSession(t *testing.T) {
	t.Setenv(InteractionEnvVar, protocol.InteractionUnattended)
	for _, e := range buildSessionEnv(sessionEnvOpts{SessionID: "s1", Extra: map[string]string{InteractionEnvVar: "interactive"}}) {
		if strings.HasPrefix(e, InteractionEnvVar+"=") {
			t.Errorf("interaction variable in the session env: %q", e)
		}
	}
	if validExtraEnvKey(InteractionEnvVar) {
		t.Error("validExtraEnvKey accepts the interaction variable")
	}
}

// The instructions file must not tell a session to say little: the pushes are what stay
// infrequent, and the session's mode (in its system prompt) governs the chat.
func TestManagedBlockDefersToTheInteractionMode(t *testing.T) {
	for _, gone := range []string{"Default to quiet", "don’t spam"} {
		if strings.Contains(managedBlock, gone) {
			t.Errorf("managed block still says %q", gone)
		}
	}
	for _, want := range []string{
		"`blerg-runner update` is a push to the user’s phone, so keep those infrequent",
		"Writing in the chat is not a push",
		"interactive", "unattended", "system prompt", "that is what governs",
		"Works from subagents.", "is missing, skip silently.",
	} {
		if !strings.Contains(managedBlock, want) {
			t.Errorf("managed block does not say %q", want)
		}
	}
}
