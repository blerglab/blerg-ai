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
