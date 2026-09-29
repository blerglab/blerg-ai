package models

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

func TestClaudeEfforts(t *testing.T) {
	for _, engine := range []string{"", "claude"} {
		for _, e := range []string{"", "low", "medium", "high", "xhigh", "max"} {
			if !ValidEffortFor(engine, e) {
				t.Errorf("engine %q: %q should be valid", engine, e)
			}
		}
		for _, e := range []string{"ultra", "HIGH", "high ", "high;id", "--effort", "none"} {
			if ValidEffortFor(engine, e) {
				t.Errorf("engine %q: %q should be invalid", engine, e)
			}
		}
	}
}

// Codex and Hermes each have their own effort allowlist in the one table.
func TestCodexAndHermesEfforts(t *testing.T) {
	for engine, tc := range map[string]struct{ ok, bad []string }{
		"codex": {
			ok:  []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"},
			bad: []string{"auto", "turbo", "HIGH", "high ", "high;id", "-c", "model_reasoning_effort=high"},
		},
		"hermes": {
			ok:  []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"},
			bad: []string{"auto", "High", "--yolo", "low\nhigh", "max --reasoning"},
		},
	} {
		for _, e := range tc.ok {
			if !ValidEffortFor(engine, e) {
				t.Errorf("%s: %q should be valid", engine, e)
			}
		}
		for _, e := range tc.bad {
			if ValidEffortFor(engine, e) {
				t.Errorf("%s: %q should be invalid", engine, e)
			}
		}
	}
}

// An engine with no registered effort levels takes none: only "" passes.
func TestUnregisteredEngineTakesNoEffort(t *testing.T) {
	if !ValidEffortFor("openclaw", "") || ValidEffortFor("openclaw", "high") {
		t.Error("openclaw has no registered efforts")
	}
	if EffortsFor("openclaw") != nil {
		t.Error("EffortsFor(openclaw) should be nil")
	}
}

func TestRegisterEngineRules(t *testing.T) {
	restore := RegisterEngineRules("testengine", EngineRules{
		ModelPattern: regexp.MustCompile(`^tm-[0-9]+$`),
		Efforts:      []string{"none", "ultra"},
	})
	t.Cleanup(func() {
		restore()
		if HasEngineRules("testengine") {
			t.Error("restore did not remove the test engine")
		}
	})
	if !ValidModelFor("testengine", "tm-1") || ValidModelFor("testengine", "claude-opus-5") {
		t.Error("registered model rule not applied")
	}
	if !ValidEffortFor("testengine", "ultra") || ValidEffortFor("testengine", "max") {
		t.Error("registered effort allowlist not applied")
	}
	if !ValidAnyEffort("ultra") || !ValidAnyEffort("max") || ValidAnyEffort("bogus") {
		t.Error("ValidAnyEffort should be the union of every engine's allowlist")
	}
}

func TestValidModelFor(t *testing.T) {
	good := map[string][]string{
		"":       {"", "sonnet", "opus", "haiku", "sonnet[1m]", "claude-opus-5-5", "claude-haiku-4-5-20251001", "claude-opus-5-5[1m]"},
		"claude": {"claude-sonnet-5"},
		"codex":  {"gpt-5-codex", "o3"},
		"hermes": {"openrouter/anthropic/claude-sonnet-4", "Qwen/Qwen3-32B", "provider:model@v1"},
	}
	for engine, list := range good {
		for _, m := range list {
			if !ValidModelFor(engine, m) {
				t.Errorf("engine %q model %q should be valid", engine, m)
			}
		}
	}
	bad := map[string][]string{
		"": {"-p", "--dangerously-skip-permissions", "sonnet --effort max", "sonnet;id", "$(id)", "Sonnet",
			"claude/opus", "a\nb", "`id`", "x'y", string(make([]byte, 65))},
		"codex": {"-m", "gpt 5", "gpt;5", "$(id)", "a|b"},
	}
	for engine, list := range bad {
		for _, m := range list {
			if ValidModelFor(engine, m) {
				t.Errorf("engine %q model %q should be invalid", engine, m)
			}
		}
	}
}

func TestValidCatalogID(t *testing.T) {
	if !ValidCatalogID("claude-opus-5-5") || ValidCatalogID("sonnet") || ValidCatalogID("claude-") {
		t.Error("catalog id rule wrong")
	}
	// The picker must never offer an id the launch refuses: the longest id
	// the catalog accepts is exactly the longest model a launch accepts.
	longest := "claude-" + strings.Repeat("a", claudeModelMaxLen-len("claude-"))
	if !ValidCatalogID(longest) || !ValidModelFor("claude", longest) {
		t.Errorf("a %d-char id should pass both rules", len(longest))
	}
	if ValidCatalogID(longest + "a") {
		t.Error("an id longer than a launch accepts must be dropped from the catalog")
	}
}

// Every engine in the shared table is registered at init.
func TestBuiltinEnginesRegistered(t *testing.T) {
	for engine := range builtinEngines {
		if !HasEngineRules(engine) {
			t.Errorf("%s not registered", engine)
		}
	}
	if !HasEngineRules("") {
		t.Error(`"" (the default engine) must resolve to claude`)
	}
}

func TestRegistryUnknownEngineIsEmptyNotError(t *testing.T) {
	r := NewRegistry()
	r.Register("", BuiltinClaudeSource)
	l, err := r.Models(context.Background(), "codex", "")
	if err != nil || l.Source != SourceNone || l.Models == nil || len(l.Models) != 0 {
		t.Fatalf("unknown engine: %+v %v", l, err)
	}
	l, err = r.Models(context.Background(), "claude", "")
	if err != nil || l.Source != SourceBuiltin || len(l.Models) != len(Builtin()) {
		t.Fatalf("claude (registered as \"\"): %+v %v", l, err)
	}
}
