// Package models owns what the runner knows about which model a session may
// run on and at what effort, per engine:
//
//   - the per-engine rules every model/effort is checked against before it
//     can reach an engine's command line (this file). They live in ONE table,
//     builtinEngines below, registered by this package's init. The server and
//     the daemon (and the cluster pod) are separate processes with separate
//     in-memory registries, but every one of them is built from this package,
//     so they all load the same table and cannot drift;
//   - the model lists the launch sheet picks from, as Sources keyed by engine
//     (source.go): the Claude Code catalog the server fetches (catalog.go),
//     and the lists daemons probe from an engine's own CLI or endpoint and
//     report (reported.go checks those, on both ends). Engines without a
//     Source get an empty list.
//
// What a new engine needs here: an entry in builtinEngines (model rule, and
// effort levels if it takes any). A picker needs either a Source registered
// in the server's registry (server.NewModelSources) or — for a list only the
// workstation can see — a ListModels probe on the daemon's EngineSpec, which
// the server serves through its daemon-reported source. Its command-line
// flags are declared on its daemon EngineSpec, not here.
package models

import (
	"regexp"
	"sync"
)

// EngineRules is what an engine accepts as a model and an effort.
type EngineRules struct {
	// ModelPattern is the shape a model name must have for this engine; nil
	// means the generic provider/model rule.
	ModelPattern *regexp.Regexp
	// Efforts is the engine's effort allowlist, lowest first. Empty means the
	// engine takes no effort: any non-empty effort is refused.
	Efforts []string
}

// ClaudeEngine is the engine id Claude Code is registered under. The spawn
// APIs spell it "" (the default engine) as well; NormalizeEngine maps both.
const ClaudeEngine = "claude"

// claudeModelMaxLen bounds a Claude model name; the catalog id rule below is
// derived from it so the picker never offers an id the launch would refuse.
const claudeModelMaxLen = 64

// claudeModelPattern is what a Claude session accepts as --model: a full id
// (claude-sonnet-5), a CLI alias (sonnet, opus) or an alias with a context
// suffix (sonnet[1m]). Lowercase, no leading dash (it would read as a flag),
// no whitespace or shell metacharacters. The brackets reach the CLI as a
// plain argv element: tmux ≥ 3.0 runs a multi-argument new-session command
// without a shell (see runner/README.md).
var claudeModelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-\[\]]{0,63}$`)

// genericModelPattern is the rule for engines whose model names are
// provider/model shaped (Hermes: "openrouter/anthropic/…", Codex:
// "gpt-5-codex"). Still no leading dash, whitespace or shell metacharacters.
var genericModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\-\[\]]{0,127}$`)

// builtinEngines is THE table of engine rules — every engine the daemon's
// EngineSpec registry knows, plus Claude. A daemon test fails if an engine is
// added there without a row here.
var builtinEngines = map[string]EngineRules{
	ClaudeEngine: {ModelPattern: claudeModelPattern, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	// Codex: `-c model_reasoning_effort=<level>` — Codex's own reasoning
	// levels (none and minimal included); which of them a model takes comes
	// with its list from `codex debug models`.
	"codex": {ModelPattern: genericModelPattern, Efforts: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}},
	// Hermes: `--reasoning <level>`, the levels its CLI documents. Hermes does
	// not say which of them a model supports, so its list offers all of them.
	"hermes": {ModelPattern: genericModelPattern, Efforts: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}},
	// No effort levels: OpenClaw's reasoning knob is not wired, so any effort
	// is refused for it until this row lists some.
	"openclaw": {ModelPattern: genericModelPattern},
}

var (
	rulesMu sync.RWMutex
	rules   = map[string]EngineRules{}
)

func init() {
	for engine, r := range builtinEngines {
		RegisterEngineRules(engine, r)
	}
}

// RegisterEngineRules sets (or replaces) an engine's rules and returns a func
// that restores what was there before (tests use it with t.Cleanup). Engines
// shipped with the runner are registered from builtinEngines at init.
func RegisterEngineRules(engine string, r EngineRules) (restore func()) {
	engine = NormalizeEngine(engine)
	rulesMu.Lock()
	defer rulesMu.Unlock()
	prev, had := rules[engine]
	if r.ModelPattern == nil {
		r.ModelPattern = genericModelPattern
	}
	r.Efforts = append([]string(nil), r.Efforts...)
	rules[engine] = r
	return func() {
		rulesMu.Lock()
		defer rulesMu.Unlock()
		if had {
			rules[engine] = prev
		} else {
			delete(rules, engine)
		}
	}
}

// HasEngineRules reports whether engine has registered rules.
func HasEngineRules(engine string) bool {
	rulesMu.RLock()
	defer rulesMu.RUnlock()
	_, ok := rules[NormalizeEngine(engine)]
	return ok
}

// NormalizeEngine maps the spawn APIs' "" (default engine) to ClaudeEngine.
func NormalizeEngine(engine string) string {
	if engine == "" {
		return ClaudeEngine
	}
	return engine
}

func rulesFor(engine string) EngineRules {
	rulesMu.RLock()
	defer rulesMu.RUnlock()
	if r, ok := rules[NormalizeEngine(engine)]; ok {
		return r
	}
	return EngineRules{ModelPattern: genericModelPattern}
}

// ValidModelFor reports whether model may be passed to engine's CLI. The
// empty model is valid (the engine's own default).
func ValidModelFor(engine, model string) bool {
	return model == "" || rulesFor(engine).ModelPattern.MatchString(model)
}

// ValidEffortFor reports whether effort is in engine's allowlist. The empty
// effort is valid (the model's own default); an engine with no allowlist
// accepts nothing else.
func ValidEffortFor(engine, effort string) bool {
	if effort == "" {
		return true
	}
	for _, e := range rulesFor(engine).Efforts {
		if e == effort {
			return true
		}
	}
	return false
}

// EffortsFor returns a copy of engine's effort allowlist (nil when it takes
// none).
func EffortsFor(engine string) []string {
	e := rulesFor(engine).Efforts
	if len(e) == 0 {
		return nil
	}
	return append([]string(nil), e...)
}

// ValidAnyEffort reports whether effort is in SOME engine's allowlist — for a
// caller that cannot know the session's engine (the browser's in-session
// switch); the daemon then applies the session's own engine's rules.
func ValidAnyEffort(effort string) bool {
	if effort == "" {
		return true
	}
	rulesMu.RLock()
	defer rulesMu.RUnlock()
	for _, r := range rules {
		for _, e := range r.Efforts {
			if e == effort {
				return true
			}
		}
	}
	return false
}

// ValidAnyModel is the engine-agnostic model check (the loosest rule) for the
// same kind of caller.
func ValidAnyModel(model string) bool {
	return model == "" || genericModelPattern.MatchString(model)
}

// catalogIDPattern is the shape every model id taken from the published
// Claude catalog must have: a Claude model id no longer than a launch accepts
// (claudeModelMaxLen, "claude-" included). The catalog is fetched from the
// network, so an id that does not fit is dropped rather than shown.
var catalogIDPattern = regexp.MustCompile(`^claude-[a-z0-9.-]{1,57}$`)

// ValidCatalogID reports whether id is an acceptable Claude catalog model id.
func ValidCatalogID(id string) bool {
	return len(id) <= claudeModelMaxLen && catalogIDPattern.MatchString(id)
}

// BuiltinModelInfo reports what the compiled-in Claude list knows about id:
// its effort levels and default, and whether it is listed at all. For callers
// with no catalog of their own (the daemon) that need to reconcile an effort
// with a model switch.
func BuiltinModelInfo(id string) (efforts []string, defaultEffort string, ok bool) {
	for _, m := range Builtin() {
		if m.ID == id {
			return m.Efforts, m.DefaultEffort, true
		}
	}
	return nil, "", false
}
