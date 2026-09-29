package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// EngineSpec centralizes what varies per non-Claude CLI engine (Codex,
// Hermes, OpenClaw, ...) — the tmux-terminal invocation and the headless
// Agent-kind driver constructor — so adding a new engine touches this one
// registry instead of a string-literal branch in manager.go's spawn/recover
// switches and agentsession.go's API-key gate/model-default guard/driver
// switch. Claude (msg.Engine == "") is deliberately NOT in this registry:
// it's the fallback default, drives the native tool-calling loop or the
// claude-code driver, and is the one engine allowed to guess a default
// model ("claude-sonnet-5") when none is given — every registered engine
// here shells to its own CLI, billed to its own account/config, and uses
// that CLI's own configured default when Model is empty.
type EngineSpec struct {
	ID          string
	DisplayName string
	// Binary is the CLI name resolved via resolveEngineBinary for the
	// tmux-hosted interactive invocation.
	Binary string
	// ExtraArgs are static args always included right after Binary — e.g.
	// OpenClaw's "chat" subcommand.
	ExtraArgs []string
	// ModelFlag is the flag this CLI takes a model override with (e.g.
	// "--model" for Codex, "-m" for Hermes). Empty means the CLI has no
	// documented per-invocation model flag (OpenClaw's chat/tui) — Model is
	// then never passed on the command line at all.
	ModelFlag string
	// AgentModelFlag is the model flag of the headless agent-kind invocation
	// when it differs from ModelFlag (OpenClaw: its `agent exec` takes
	// --model although its interactive chat has none). Empty = ModelFlag.
	AgentModelFlag string
	// EffortArgs renders a reasoning-effort choice as this CLI's arguments
	// (Claude: "--effort <level>"). nil means the CLI takes no per-invocation
	// effort, and any effort is ignored. It is only ever called with a value
	// that passed models.ValidEffortFor for this engine — see modelEffortArgs.
	EffortArgs func(effort string) []string
	// ListModels, when set, is this engine's model probe: it asks the
	// engine's own CLI or endpoint which models (and effort levels) this
	// workstation can use. The daemon's ModelProber runs it at connect and
	// then periodically, never on the heartbeat or spawn path, checks the
	// result with models.SanitizeReported and reports it to the server, which
	// serves it as the engine's GET /api/models list for this daemon. nil =
	// no list. Return errNoModelList for a definite "nothing to offer" (not
	// installed, not configured, refused); any other error keeps the last
	// good list. It must bound its own work (the prober also passes a
	// deadline) and treat everything it reads as untrusted.
	ListModels func(ctx context.Context) ([]models.Model, error)
	// SkipPermsFlag is the flag this CLI takes to bypass its permission/
	// approval prompts (e.g. "--yolo" for Hermes). Empty means no
	// per-invocation equivalent exists (OpenClaw's policy is config-driven,
	// via `openclaw approvals`/`openclaw exec-policy`, not a CLI flag).
	SkipPermsFlag string
	// NewAgentDriver builds the headless Agent-kind driver from
	// AgentDriverOpts (which carries this spec, so the driver renders its
	// model/effort arguments through the same flags — see engineModel).
	NewAgentDriver func(o AgentDriverOpts) sessionDriver
	// ConfigVars lists this engine's operator-tunable env vars (e.g.
	// OpenClaw's replay-context budget). Logged once at daemon startup (see
	// NewManager) so they're discoverable without reading source — the log
	// line names the engine via DisplayName, never a per-engine literal, so
	// a new config knob needs no change outside this registry.
	ConfigVars []EngineConfigVar
	// DetectAvailable is a best-effort, host-local check for whether this
	// engine is already configured and usable (CLI installed + logged in/
	// has a provider set up) — mirrors the shell installer's
	// *_already_configured heuristics (install/desktop/daemon/install.sh) so
	// the two stay in sync. Advisory only, like its shell counterpart: a
	// false negative just means "can't tell", never "definitely broken" —
	// the launch sheet's Engine picker at session-launch time remains the
	// real source of truth.
	DetectAvailable func() bool
	// Capabilities, when set, is this engine's best-effort answer to "what
	// has this session loaded" (skills, plugins, MCP servers, ...), for an
	// engine that does not report it itself. AgentHost calls it once when an
	// agent-kind session starts and emits the result as a capabilities event
	// (protocol.CapabilitiesPayload). It must be cheap, read-only, bounded,
	// credential-free and never run a command; nil means the engine reports
	// nothing and the UI says so. An engine that does report its own (Claude
	// Code's init line) emits the event from its driver instead.
	Capabilities func(c CapabilityContext) []protocol.CapabilityGroup
}

// AgentDriverOpts is what an agent-kind driver is built from.
type AgentDriverOpts struct {
	// Spec is the engine's own EngineSpec: its flags render Model/Effort.
	Spec    EngineSpec
	WorkDir string
	// Model and Effort have already passed the engine's rules
	// (spawnModelProblem); the driver still renders them only through
	// engineModel, which drops anything invalid.
	Model  string
	Effort string
	// SessionID is only used by engines that pin their own conversation
	// identity upfront (Hermes's --continue <name>); others ignore it.
	SessionID string
	Emitter   agent.Emitter
	// Env is the sanitised environment every engine subprocess starts from
	// (never the daemon master token); nil means sanitizedEnviron().
	Env []string
}

// EngineConfigVar describes one operator-tunable env var for an engine.
// Describe renders its CURRENT resolved value (already applying defaults/
// validation), not the raw env string, so the startup log shows what the
// daemon actually uses.
type EngineConfigVar struct {
	EnvVar   string
	Describe func() string
}

// engineOrder fixes display/iteration order — map order isn't stable and
// user-facing text (error messages, docs) reads better with Claude first
// and everything else in the order it was added.
var engineOrder = []string{"codex", "hermes", "openclaw"}

var engineRegistry = map[string]EngineSpec{
	// Unlike Claude, Codex assigns its own session identity on first spawn —
	// there is no equivalent of --session-id to pin one upfront — so there
	// is no "resume" form for tmux-kind sessions (see recoverSession).
	// Interactive codex reads its initial prompt from the terminal like
	// claude does, so none is passed here; queueInitialPrompt injects it
	// after spawn either way.
	"codex": {
		ID: "codex", DisplayName: "Codex",
		Binary: "codex", ModelFlag: "--model", SkipPermsFlag: "--dangerously-bypass-approvals-and-sandbox",
		// A config override: the value is TOML-parsed, and a bare level
		// (already one of the rules table's tokens) parses as that string.
		EffortArgs:      func(effort string) []string { return []string{"-c", "model_reasoning_effort=" + effort} },
		ListModels:      probeCodexModels,
		NewAgentDriver:  func(o AgentDriverOpts) sessionDriver { return newCodexDriver(o) },
		DetectAvailable: codexAlreadyLoggedIn,
		Capabilities:    codexCapabilities,
	},
	// Like Codex, Hermes assigns its own session identity on first spawn —
	// its --continue/-c <name> resolver exists but pairing it with
	// --create-if-missing (needed to pin an upfront id) is only wired on the
	// `chat` subcommand, not plain interactive `hermes` — so every tmux-kind
	// hermes session (including post-crash recovery) starts fresh too.
	"hermes": {
		ID: "hermes", DisplayName: "Hermes",
		Binary: "hermes", ModelFlag: "-m", SkipPermsFlag: "--yolo",
		EffortArgs:      func(effort string) []string { return []string{"--reasoning", effort} },
		ListModels:      probeHermesModels,
		NewAgentDriver:  func(o AgentDriverOpts) sessionDriver { return newHermesDriver(o) },
		DetectAvailable: hermesAlreadyConfigured,
		Capabilities:    hermesCapabilities,
	},
	// `openclaw chat` is an alias for `tui --local` (embedded runtime, no
	// separate Gateway process needed). No documented --model flag (model
	// selection is a persistent `openclaw models set` choice, not a
	// per-invocation override) and no per-invocation permission-bypass flag
	// either (OpenClaw's approval policy is config-driven).
	"openclaw": {
		ID: "openclaw", DisplayName: "OpenClaw",
		Binary: "openclaw", ExtraArgs: []string{"chat"},
		// ...but its headless `agent exec` does take --model.
		AgentModelFlag: "--model",
		NewAgentDriver: func(o AgentDriverOpts) sessionDriver { return newOpenclawDriver(o) },
		ConfigVars: []EngineConfigVar{
			{
				EnvVar: "BLERG_OPENCLAW_CONTEXT_CHARS",
				Describe: func() string {
					return fmt.Sprintf("%d chars (replay budget — raise for bigger hardware)", openclawContextBudgetChars())
				},
			},
		},
		DetectAvailable: openclawAlreadyConfigured,
	},
}

// claudeEngineSpec is Claude Code's flag vocabulary. Claude stays out of
// engineRegistry (see EngineSpec: it is the default engine with its own
// command builders, claudeCommand and ccTurnArgs), but its model/effort flags
// are declared here the same way every registered engine declares its own,
// and built by the same modelEffortArgs.
var claudeEngineSpec = EngineSpec{
	ID: "claude", DisplayName: "Claude", Binary: "claude",
	ModelFlag: "--model", SkipPermsFlag: "--dangerously-skip-permissions",
	EffortArgs: func(effort string) []string { return []string{"--effort", effort} },
}

// modelEffortArgs is an engine invocation's model and effort arguments, from
// the spec's own ModelFlag/EffortArgs. A value that fails the engine's rules
// (models.ValidModelFor / ValidEffortFor) is left out rather than passed on:
// spawns are refused upstream for a bad value (spawnModelProblem, and the
// server's 422), so reaching here with one means a caller skipped that check,
// and the one thing that must not happen is it landing on a command line.
func modelEffortArgs(spec EngineSpec, model, effort string) []string {
	var args []string
	if model != "" && spec.ModelFlag != "" && models.ValidModelFor(spec.ID, model) {
		args = append(args, spec.ModelFlag, model)
	}
	if effort != "" && spec.EffortArgs != nil && models.ValidEffortFor(spec.ID, effort) {
		args = append(args, spec.EffortArgs(effort)...)
	}
	return args
}

// engineModel is an agent-kind CLI driver's current model and effort plus
// the engine flags that render them. Drivers embed it (guarded by their own
// mutex); set is the defensive last check before a value can reach argv.
type engineModel struct {
	spec   EngineSpec
	model  string
	effort string
}

func newEngineModel(o AgentDriverOpts) engineModel {
	return engineModel{spec: o.Spec, model: o.Model, effort: o.Effort}
}

// set applies a model/effort change ("" = unchanged). A value that fails the
// engine's rules is refused (ok=false) and nothing changes. AgentHost has
// already checked the same rules; this is the driver not trusting its caller.
func (m *engineModel) set(model, effort string) (ok bool) {
	if !models.ValidModelFor(m.spec.ID, model) || !models.ValidEffortFor(m.spec.ID, effort) {
		return false
	}
	if model != "" {
		m.model = model
	}
	if effort != "" {
		m.effort = effort
	}
	return true
}

// args renders the headless invocation's model/effort arguments.
func (m *engineModel) args() []string {
	spec := m.spec
	if spec.AgentModelFlag != "" {
		spec.ModelFlag = spec.AgentModelFlag
	}
	return modelEffortArgs(spec, m.model, m.effort)
}

// buildTerminalCommand renders spec's tmux-hosted interactive invocation
// from its declarative fields — one generic builder instead of a bespoke
// xCommand function per engine, so a new engine's CLI flags are a registry
// entry, not a new function. sandboxed skips resolving Binary to an
// absolute host path (see resolveEngineBinary).
func buildTerminalCommand(spec EngineSpec, model, effort string, skipPerms, sandboxed bool) []string {
	cmd := []string{resolveEngineBinary(spec.Binary, sandboxed)}
	cmd = append(cmd, spec.ExtraArgs...)
	cmd = append(cmd, modelEffortArgs(spec, model, effort)...)
	if skipPerms && spec.SkipPermsFlag != "" {
		cmd = append(cmd, spec.SkipPermsFlag)
	}
	return cmd
}

// logEngineConfig prints every registered engine's resolved ConfigVars once
// at daemon startup, so operator-tunable knobs are discoverable in
// journalctl without reading source. Purely registry-driven: a new engine's
// ConfigVars entries show up here with no change to this function.
func logEngineConfig() {
	for _, id := range engineOrder {
		for _, cv := range engineRegistry[id].ConfigVars {
			log.Printf("manager: %s config: %s = %s", engineRegistry[id].DisplayName, cv.EnvVar, cv.Describe())
		}
	}
}

// engineDisplayNames renders "Claude, Codex, Hermes, or OpenClaw" for
// user-facing error messages — Claude first (it's the default), then every
// registered engine in engineOrder.
func engineDisplayNames() string {
	names := []string{"Claude"}
	for _, id := range engineOrder {
		names = append(names, engineRegistry[id].DisplayName)
	}
	if len(names) == 1 {
		return names[0]
	}
	return joinWithOr(names)
}

// hermesConfigKeyPattern matches a real provider key/token line in
// ~/.hermes/.env — kept as a package-level var so it compiles once.
var hermesConfigKeyPattern = regexp.MustCompile(`(?m)^[A-Z_]+(_API_KEY|_TOKEN)=.+`)

// hermesBaseURLPattern matches a non-empty base_url line in
// ~/.hermes/config.yaml (e.g. a keyless local Ollama backend).
var hermesBaseURLPattern = regexp.MustCompile(`(?m)^\s*base_url:\s*\S`)

// claudeCredentialPattern matches a non-empty accessToken in Claude's
// internal (undocumented) credentials file.
var claudeCredentialPattern = regexp.MustCompile(`"accessToken"\s*:\s*"[^"]`)

// homeDir returns the current user's home directory, or "" if it can't be
// determined — every *AlreadyConfigured/*AlreadyLoggedIn check below treats
// that as "can't tell" (not configured), same as a missing file would.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// nonEmptyFile reports whether path exists and has non-zero size.
func nonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// readFile returns a file's contents, or "" if it can't be read.
func readFile(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // callers pass fixed credential/config paths under the user's own home (~/.claude, ~/.hermes)
	if err != nil {
		return ""
	}
	return string(b)
}

// claudeAvailable mirrors install.sh's claude_already_logged_in: Claude
// isn't in engineRegistry (see EngineSpec's doc comment) so it needs its own
// check rather than a DetectAvailable field.
func claudeAvailable() bool {
	home := homeDir()
	if home == "" {
		return false
	}
	cred := filepath.Join(home, ".claude", ".credentials.json")
	if !nonEmptyFile(cred) {
		return false
	}
	return claudeCredentialPattern.MatchString(readFile(cred))
}

// codexAlreadyLoggedIn mirrors install.sh's codex_already_logged_in.
func codexAlreadyLoggedIn() bool {
	if _, err := exec.LookPath("codex"); err != nil {
		return false
	}
	home := homeDir()
	if home == "" {
		return false
	}
	return nonEmptyFile(filepath.Join(home, ".codex", "auth.json"))
}

// hermesAlreadyConfigured mirrors install.sh's hermes_already_configured:
// either a real provider key in .env, or a custom/local base_url in
// config.yaml (e.g. a keyless local Ollama backend), is sufficient.
func hermesAlreadyConfigured() bool {
	if _, err := exec.LookPath("hermes"); err != nil {
		return false
	}
	home := homeDir()
	if home == "" {
		return false
	}
	envPath := filepath.Join(home, ".hermes", ".env")
	if nonEmptyFile(envPath) && hermesConfigKeyPattern.MatchString(readFile(envPath)) {
		return true
	}
	cfgPath := filepath.Join(home, ".hermes", "config.yaml")
	return nonEmptyFile(cfgPath) && hermesBaseURLPattern.MatchString(readFile(cfgPath))
}

// openclawAlreadyConfigured mirrors install.sh's openclaw_already_configured.
func openclawAlreadyConfigured() bool {
	if _, err := exec.LookPath("openclaw"); err != nil {
		return false
	}
	home := homeDir()
	if home == "" {
		return false
	}
	return nonEmptyFile(filepath.Join(home, ".openclaw", "state", "openclaw.sqlite"))
}

// availableEngines returns the IDs of engines that appear configured and
// usable on this host right now — Claude first (it's the default), then
// every registered engine in engineOrder. Best-effort/advisory, same as the
// underlying DetectAvailable/claudeAvailable checks: reported to the server
// in DaemonHello/DaemonHeartbeat so the launch UI can default its Engine
// picker to something that will actually work, instead of guessing.
func availableEngines() []string {
	var found []string
	if claudeAvailable() {
		found = append(found, "claude")
	}
	for _, id := range engineOrder {
		spec := engineRegistry[id]
		if spec.DetectAvailable != nil && spec.DetectAvailable() {
			found = append(found, id)
		}
	}
	return found
}

func joinWithOr(items []string) string {
	if len(items) == 1 {
		return items[0]
	}
	out := items[0]
	for i := 1; i < len(items)-1; i++ {
		out += ", " + items[i]
	}
	if len(items) > 1 {
		sep := ", or "
		if len(items) == 2 {
			sep = " or "
		}
		out += sep + items[len(items)-1]
	}
	return out
}
