package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBundleFixture(t *testing.T, home, rel, content string) {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigBundleRoundTripAndVersionFiltering(t *testing.T) {
	home := t.TempDir()
	writeBundleFixture(t, home, ".claude/CLAUDE.md", "USER RULES")
	writeBundleFixture(t, home, ".claude/skills/foo/SKILL.md", "---\nname: foo\ndescription: d\n---\nbody")
	writeBundleFixture(t, home, ".claude/agents/rev.md", "---\nname: rev\n---\nprompt")
	// two plugin versions incl. a two-digit one — only 10.0.0 ships
	writeBundleFixture(t, home, ".claude/plugins/cache/mp/sp/9.9.9/skills/s/SKILL.md", "old")
	writeBundleFixture(t, home, ".claude/plugins/cache/mp/sp/10.0.0/skills/s/SKILL.md", "new")
	// oversized file skipped
	writeBundleFixture(t, home, ".claude/skills/big/SKILL.md", strings.Repeat("x", bundleFileCap+1))

	bundle, err := BuildConfigBundle(home)
	if err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := ExtractConfigBundle(bytes.NewReader(bundle), dest); err != nil {
		t.Fatal(err)
	}

	mustExist := []string{
		".claude/CLAUDE.md",
		".claude/skills/foo/SKILL.md",
		".claude/agents/rev.md",
		".claude/plugins/cache/mp/sp/10.0.0/skills/s/SKILL.md",
	}
	for _, rel := range mustExist {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	mustNotExist := []string{
		".claude/plugins/cache/mp/sp/9.9.9/skills/s/SKILL.md",
		".claude/skills/big/SKILL.md",
	}
	for _, rel := range mustNotExist {
		if _, err := os.Stat(filepath.Join(dest, rel)); err == nil {
			t.Errorf("should not ship: %s", rel)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dest, ".claude/CLAUDE.md"))
	if string(data) != "USER RULES" {
		t.Fatalf("content mismatch: %q", data)
	}
}

func TestConfigBundleSyncsSafeCodexCustomization(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "default"
		if custom {
			name = "custom-home"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			source := filepath.Join(home, ".codex")
			t.Setenv("CODEX_HOME", "")
			if custom {
				source = t.TempDir()
				t.Setenv("CODEX_HOME", source)
			}
			for _, rel := range []string{"AGENTS.md", "AGENTS.override.md", "skills/review/SKILL.md", "skills/review/scripts/check.py"} {
				writeBundleFixture(t, source, rel, "safe fixture instructions")
			}
			for _, rel := range []string{"auth.json", "config.toml", "sessions/fixture.jsonl", "state.sqlite", "plugins/private/config.toml"} {
				writeBundleFixture(t, source, rel, "private fixture content")
			}
			writeBundleFixture(t, home, ".agents/skills/shared/SKILL.md", "shared fixture instructions")
			if custom {
				writeBundleFixture(t, home, ".codex/skills/default/SKILL.md", "wrong default home")
			}
			raw, err := BuildConfigBundle(home)
			if err != nil {
				t.Fatal(err)
			}
			dest := t.TempDir()
			if err := ExtractConfigBundle(bytes.NewReader(raw), dest); err != nil {
				t.Fatal(err)
			}
			for _, rel := range []string{".codex/AGENTS.md", ".codex/AGENTS.override.md", ".codex/skills/review/SKILL.md", ".codex/skills/review/scripts/check.py", ".agents/skills/shared/SKILL.md"} {
				if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
					t.Errorf("missing customization %s", rel)
				}
			}
			for _, rel := range []string{".codex/auth.json", ".codex/config.toml", ".codex/sessions", ".codex/state.sqlite", ".codex/plugins", ".codex/skills/default"} {
				if _, err := os.Stat(filepath.Join(dest, rel)); !os.IsNotExist(err) {
					t.Errorf("unexpected state or unselected home %s", rel)
				}
			}
		})
	}
}

func TestConfigBundleUsesRegisteredEngineRoots(t *testing.T) {
	oldOrder := engineOrder
	engineOrder = append(append([]string(nil), engineOrder...), "fixture")
	engineRegistry["fixture"] = EngineSpec{ID: "fixture", ConfigBundle: &EngineConfigBundle{Directory: ".fixture", Roots: []string{"instructions.md"}}}
	t.Cleanup(func() { engineOrder = oldOrder; delete(engineRegistry, "fixture") })
	home := t.TempDir()
	writeBundleFixture(t, home, ".fixture/instructions.md", "fixture instructions")
	writeBundleFixture(t, home, ".fixture/auth.json", "private fixture state")
	raw, err := BuildConfigBundle(home)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := ExtractConfigBundle(bytes.NewReader(raw), dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".fixture/instructions.md")); err != nil {
		t.Fatal("registered customization was not exported")
	}
	if _, err := os.Stat(filepath.Join(dest, ".fixture/auth.json")); !os.IsNotExist(err) {
		t.Fatal("unregistered state was exported")
	}
}
