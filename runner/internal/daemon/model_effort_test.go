package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A registered engine declares its own model/effort flags on its EngineSpec;
// one without an EffortArgs hook ignores effort entirely.
func TestBuildTerminalCommandUsesTheSpecsEffortHook(t *testing.T) {
	t.Cleanup(models.RegisterEngineRules("fakeeng", models.EngineRules{Efforts: []string{"ultra"}}))
	spec := EngineSpec{ID: "fakeeng", Binary: "fakeeng", ModelFlag: "-m",
		EffortArgs: func(e string) []string { return []string{"--reasoning=" + e} }}
	got := buildTerminalCommand(spec, "vendor/model-1", "ultra", false, true)
	if want := []string{"fakeeng", "-m", "vendor/model-1", "--reasoning=ultra"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Not in the engine's allowlist: dropped.
	if got := buildTerminalCommand(spec, "", "max", false, true); len(got) != 1 {
		t.Errorf("an effort outside the engine's list reached argv: %v", got)
	}
	// OpenClaw has no hook: effort never appears.
	spec = engineRegistry["openclaw"]
	spec.ID = "fakeeng" // rules that would accept "ultra": only the missing hook drops it
	got = buildTerminalCommand(spec, "", "ultra", false, true)
	if want := []string{"openclaw", "chat"}; !slices.Equal(got, want) {
		t.Errorf("openclaw got %v, want %v", got, want)
	}
}

// Codex and Hermes render effort through their own flags in a tmux terminal,
// sandboxed or not.
func TestTerminalCommandCodexAndHermesEffort(t *testing.T) {
	for _, sandboxed := range []bool{true, false} {
		got := buildTerminalCommand(engineRegistry["codex"], "gpt-5.6-terra", "ultra", false, sandboxed)
		if want := []string{"--model", "gpt-5.6-terra", "-c", "model_reasoning_effort=ultra"}; !slices.Equal(got[1:], want) {
			t.Errorf("codex (sandboxed=%v) got %v, want …%v", sandboxed, got, want)
		}
		got = buildTerminalCommand(engineRegistry["hermes"], "qwen3-30b", "minimal", true, sandboxed)
		if want := []string{"-m", "qwen3-30b", "--reasoning", "minimal", "--yolo"}; !slices.Equal(got[1:], want) {
			t.Errorf("hermes (sandboxed=%v) got %v, want …%v", sandboxed, got, want)
		}
	}
	// No effort = no flag (Hermes "Auto", Codex's own default).
	if got := buildTerminalCommand(engineRegistry["hermes"], "qwen3-30b", "", false, true); slices.Contains(got, "--reasoning") {
		t.Errorf("hermes without effort got %v", got)
	}
	// Hostile or foreign values never reach argv, flag and all.
	for _, tc := range []struct{ engine, effort string }{
		{"codex", "auto"}, {"codex", "high model=\"x\""}, {"codex", "high\n-c sandbox=off"},
		{"hermes", "auto"}, {"hermes", "--yolo"}, {"hermes", "max; id"},
	} {
		got := buildTerminalCommand(engineRegistry[tc.engine], "", tc.effort, false, true)
		if len(got) != 1 {
			t.Errorf("%s effort %q reached argv: %v", tc.engine, tc.effort, got)
		}
	}
}

// An agent-kind Claude turn gets --model <id> --effort <level>.
func TestCCTurnArgsModelAndEffort(t *testing.T) {
	got := ccTurnArgs("do it", "claude-opus-5-5", "max", "")
	want := []string{"-p", "do it", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions", "--model", "claude-opus-5-5", "--effort", "max"}
	if !slices.Equal(got, want) {
		t.Errorf("args = %v\nwant %v", got, want)
	}
	got = ccTurnArgs("next", "sonnet", "", "cc-1")
	want = []string{"-p", "next", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions", "--model", "sonnet", "--resume", "cc-1"}
	if !slices.Equal(got, want) {
		t.Errorf("no-effort args = %v\nwant %v", got, want)
	}
}

func TestCCTurnArgsDropsHostileValues(t *testing.T) {
	got := ccTurnArgs("x", "sonnet --permission-mode bypass", "high; id", "")
	for _, a := range got {
		if strings.Contains(a, "bypass") || strings.Contains(a, "; id") {
			t.Fatalf("hostile value reached argv: %v", got)
		}
	}
	if slices.Contains(got, "--model") || slices.Contains(got, "--effort") {
		t.Errorf("an invalid model/effort must be dropped with its flag: %v", got)
	}
}

// A UI/command effort switch is validated, applied from the next turn, and
// reported as model_changed; an invalid one is ignored with an error event.
func TestClaudeCodeDriverSetModelValidates(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-sonnet-5", "high", em, nil)

	d.SetModel("", "xhigh", "ui")
	d.SetModel("claude-opus-5-5", "", "command")
	d.SetModel("opus; rm -rf /", "", "command")
	d.SetModel("", "ultra", "ui")

	d.mu.Lock()
	model, effort := d.model, d.effort
	d.mu.Unlock()
	if model != "claude-opus-5-5" || effort != "xhigh" {
		t.Fatalf("model %q effort %q", model, effort)
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	var changes []agent.ModelChangedPayload
	errs := 0
	for _, ev := range em.events {
		switch ev.Kind {
		case "model_changed":
			changes = append(changes, ev.Payload.(agent.ModelChangedPayload))
		case "error":
			errs++
		}
	}
	if len(changes) != 2 || changes[0].Effort != "xhigh" || changes[1].Model != "claude-opus-5-5" || changes[1].Effort != "xhigh" {
		t.Errorf("model_changed events = %+v", changes)
	}
	if errs != 2 {
		t.Errorf("want 2 error events for the invalid values, got %d", errs)
	}
}

// The launch effort reaches the real `claude -p` invocation on the host.
func TestClaudeCodeDriverPassesEffortToCLI(t *testing.T) {
	binDir := fakeClaude(t)
	logPath := filepath.Join(binDir, "calls.log")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CLAUDE_LOG", logPath)

	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5-5", "low", em, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.Enqueue("work", "chat")
	deadline := time.After(5 * time.Second)
	for !contains(em.kinds(), "turn_done") {
		select {
		case <-deadline:
			t.Fatalf("no turn_done; kinds: %v", em.kinds())
		case <-time.After(20 * time.Millisecond):
		}
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--model claude-opus-5-5 --effort low") {
		t.Errorf("claude not invoked with model/effort; calls:\n%s", raw)
	}
}

// A sandboxed agent-kind Claude session carries model and effort through
// `docker exec` into the container.
func TestSandboxedAgentPassesModelAndEffort(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(engineDir, "claude-calls.log"))

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-eff", Repo: "proj", Kind: "agent",
		Sandbox: true, InitialPrompt: "hello", Model: "claude-fable-5-1", Effort: "xhigh",
	})
	sender.waitForKind(t, "turn_done")
	t.Cleanup(func() { host.Kill("s-eff") })

	assertExecsInContainer(t, dockerCalls(t, logPath), "s-eff",
		"claude -p hello --output-format stream-json --verbose --dangerously-skip-permissions --model claude-fable-5-1 --effort xhigh")
}

// AgentHost refuses a spawn with an invalid effort itself — the cluster pod
// calls Spawn directly, with no Manager in front of it.
func TestAgentHostRefusesInvalidEffort(t *testing.T) {
	engineDir := fakeClaude(t)
	host, sender, logPath, home := sandboxHost(t, engineDir, AgentHostConfig{})
	writeClaudeLogin(t, home)

	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s-bad", Repo: "proj", Kind: "agent",
		Sandbox: true, Model: "sonnet", Effort: "max --dangerously-skip-permissions",
	})
	if reason := sender.waitForSessionError(t); !strings.Contains(reason, "invalid effort") {
		t.Errorf("reason = %q", reason)
	}
	if _, err := os.Stat(logPath); err == nil && strings.Contains(dockerCalls(t, logPath), "run -d") {
		t.Error("a container was started for a refused spawn")
	}
}

// recordingDriver is a sessionDriver that records SetModel calls.
type recordingDriver struct {
	sessionDriver
	calls [][2]string
}

func (r *recordingDriver) SetModel(model, effort, _ string) {
	r.calls = append(r.calls, [2]string{model, effort})
}

// Every in-session change — a /model or /effort chat command, or the UI's
// switch — is checked against the session engine's rules in ONE place
// (agentSession.changeModel) before any driver sees it.
func TestInSessionModelChangesAreValidatedBeforeTheDriver(t *testing.T) {
	for _, tc := range []struct {
		engine string
		text   string
		want   [][2]string
	}{
		{"codex", "/model -c", nil},
		{"codex", "/model gpt 5", nil},
		{"codex", "/effort auto", nil}, // not a codex level
		{"codex", "/effort minimal", [][2]string{{"", "minimal"}}},
		{"codex", "/effort ultra", [][2]string{{"", "ultra"}}},
		{"codex", "/model gpt-5-codex", [][2]string{{"gpt-5-codex", ""}}},
		{"hermes", "/model --yolo", nil},
		{"hermes", "/effort minimal", [][2]string{{"", "minimal"}}},
		{"openclaw", "/effort high", nil}, // openclaw takes no effort
		{"openclaw", "/model $(id)", nil},
		{"", "/effort foo", nil},
		{"", "/model Foo Bar", nil},
		{"", "/effort xhigh", [][2]string{{"", "xhigh"}}},
		{"", "/model claude-opus-5-5", [][2]string{{"claude-opus-5-5", ""}}},
	} {
		drv := &recordingDriver{}
		h := &AgentHost{sessions: map[string]*agentSession{"s": {loop: drv, engine: tc.engine}}}
		h.UserMessage("s", tc.text, "chat")
		if !slices.Equal(drv.calls, tc.want) {
			t.Errorf("engine %q %q: driver got %v, want %v", tc.engine, tc.text, drv.calls, tc.want)
		}
	}
	// The UI path goes through the same gate.
	drv := &recordingDriver{}
	h := &AgentHost{sessions: map[string]*agentSession{"s": {loop: drv, engine: "hermes"}}}
	h.SetModel("s", "-m", "")
	h.SetModel("s", "", "auto")
	if len(drv.calls) != 0 {
		t.Errorf("UI change reached the driver: %v", drv.calls)
	}
}

// Each CLI driver's SetModel refuses what the engine's rules refuse, and its
// argv carries only what they accept, through the EngineSpec's flags.
func TestCLIDriversSetModelIsDefensive(t *testing.T) {
	em := &collectEmitter{}
	codex := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Model: "gpt-5-codex", Emitter: em})
	codex.SetModel("-c", "", "command")
	codex.SetModel("", "auto", "command")
	codex.SetModel("", "high", "command")
	if got := codex.args(); !slices.Equal(got, []string{"--model", "gpt-5-codex", "-c", "model_reasoning_effort=high"}) {
		t.Errorf("codex args = %v", got)
	}
	hermes := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
	hermes.SetModel("openrouter/x", "", "ui")
	hermes.SetModel("", "none", "ui")
	if got := hermes.args(); !slices.Equal(got, []string{"-m", "openrouter/x", "--reasoning", "none"}) {
		t.Errorf("hermes args = %v", got)
	}
	// OpenClaw's interactive chat has no model flag, its `agent exec` does.
	oc := newOpenclawDriver(AgentDriverOpts{Spec: engineRegistry["openclaw"], WorkDir: t.TempDir(), Model: "box/qwen", Emitter: em})
	oc.SetModel("--evil", "", "ui")
	if got := oc.args(); !slices.Equal(got, []string{"--model", "box/qwen"}) {
		t.Errorf("openclaw args = %v", got)
	}
	errs := 0
	for _, k := range em.kinds() {
		if k == "error" {
			errs++
		}
	}
	if errs != 3 {
		t.Errorf("want 3 refusal events, got %d", errs)
	}
}

// A driver built through the registry gets the launch effort, and renders it
// through its spec's EffortArgs hook — the path a new engine plugs into.
func TestAgentDriverRendersEffortThroughTheSpecHook(t *testing.T) {
	t.Cleanup(models.RegisterEngineRules("codex", models.EngineRules{Efforts: []string{"low", "ultra"}}))
	spec := engineRegistry["codex"]
	spec.EffortArgs = func(e string) []string { return []string{"-c", "model_reasoning_effort=" + e} }
	d := spec.NewAgentDriver(AgentDriverOpts{Spec: spec, WorkDir: t.TempDir(), Model: "gpt-5-codex", Effort: "ultra", Emitter: &collectEmitter{}}).(*codexDriver)
	want := []string{"--model", "gpt-5-codex", "-c", "model_reasoning_effort=ultra"}
	if got := d.args(); !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

// Every engine the daemon can run has rules in the shared models table: the
// server validates against the same table, so a new EngineSpec without a row
// there would be validated by the generic fallback on one side only.
func TestEveryEngineHasSharedRules(t *testing.T) {
	for id, spec := range engineRegistry {
		if spec.ID != id {
			t.Errorf("registry key %q has spec ID %q", id, spec.ID)
		}
		if !models.HasEngineRules(id) {
			t.Errorf("engine %q has no row in internal/models builtinEngines", id)
		}
	}
	if !models.HasEngineRules(claudeEngineSpec.ID) {
		t.Error("claude has no rules")
	}
}

// Switching model drops an effort the new model does not offer (to its
// default, or none), as the built-in list knows the models.
func TestClaudeCodeDriverReconcilesEffortOnModelSwitch(t *testing.T) {
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5-5", "xhigh", &collectEmitter{}, nil)
	d.SetModel("claude-opus-4-6", "", "ui")
	if d.effort != "high" {
		t.Errorf("opus 4.6 has no xhigh: effort = %q, want its default high", d.effort)
	}
	d.SetModel("claude-haiku-4-5-20251001", "", "ui")
	if d.effort != "" {
		t.Errorf("haiku takes no effort: effort = %q", d.effort)
	}
	d.SetModel("sonnet", "max", "ui") // an alias the list doesn't know: kept as asked
	if d.effort != "max" {
		t.Errorf("effort = %q", d.effort)
	}
}
