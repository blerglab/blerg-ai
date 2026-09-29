package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, frontName, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + frontName + "\ndescription: does " + frontName + "\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (projectDir, homeDir string) {
	t.Helper()
	projectDir, homeDir = t.TempDir(), t.TempDir()
	writeSkill(t, filepath.Join(projectDir, ".claude/skills"), "review", "review", "Review things.\nUsage: /review [persona]")
	writeSkill(t, filepath.Join(homeDir, ".claude/skills"), "notes", "notes", "Body with $ARGUMENTS placeholder")
	// plugin cache with TWO versions; only highest may load
	for _, v := range []string{"5.1.0", "6.1.1"} {
		writeSkill(t, filepath.Join(homeDir, ".claude/plugins/cache/mp/superpowers", v, "skills"),
			"brainstorming", "brainstorming", "version "+v)
	}
	settings := `{"enabledPlugins":["superpowers"]}`
	if err := os.MkdirAll(filepath.Join(projectDir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".claude/settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	return
}

func TestDiscoverFindsAllScopesOneVersionPerPlugin(t *testing.T) {
	p, h := fixture(t)
	list, err := Discover(p, h)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, s := range list {
		names[s.Name] = s.Dir
	}
	if len(list) != 3 {
		t.Fatalf("want 3 skills, got %v", names)
	}
	if !strings.Contains(names["superpowers:brainstorming"], "6.1.1") {
		t.Fatalf("plugin version dedup failed: %v", names)
	}
}

func TestLoadReturnsContentAndDir(t *testing.T) {
	p, h := fixture(t)
	list, _ := Discover(p, h)
	content, dir, err := Load(list, "review")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Review things.") || dir == "" {
		t.Fatalf("content=%q dir=%q", content, dir)
	}
}

func TestResolveSlashSubstitutesOrAppends(t *testing.T) {
	p, h := fixture(t)
	list, _ := Discover(p, h)

	// no placeholder → appended Arguments line
	out, ok, err := ResolveSlash(list, "/review carmack spec.md")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !strings.Contains(out, "**Arguments:** carmack spec.md") {
		t.Fatalf("out=%q", out)
	}
	// placeholder → substituted
	out, ok, _ = ResolveSlash(list, "/notes hello world")
	if !ok || !strings.Contains(out, "Body with hello world placeholder") {
		t.Fatalf("out=%q", out)
	}
	// plugin skill resolvable by bare name
	out, ok, _ = ResolveSlash(list, "/brainstorming")
	if !ok || !strings.Contains(out, "version 6.1.1") {
		t.Fatalf("out=%q ok=%v", out, ok)
	}
	// unknown command
	_, ok, _ = ResolveSlash(list, "/nope")
	if ok {
		t.Fatal("unknown slash must return ok=false")
	}
}

func TestPromptListRendersLines(t *testing.T) {
	p, h := fixture(t)
	list, _ := Discover(p, h)
	out := PromptList(list)
	if !strings.Contains(out, "- review: does review") {
		t.Fatalf("out=%q", out)
	}
}

func TestSessionStartContextRunsHooks(t *testing.T) {
	p, h := fixture(t)
	pluginDir := filepath.Join(h, ".claude/plugins/cache/mp/superpowers/6.1.1")
	hooks := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo skill discipline loaded"}]}]}}`
	if err := os.MkdirAll(filepath.Join(pluginDir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks/hooks.json"), []byte(hooks), 0o644); err != nil {
		t.Fatal(err)
	}
	out := SessionStartContext(p, h)
	if !strings.Contains(out, "skill discipline loaded") {
		t.Fatalf("out=%q", out)
	}
}

func TestPluginVersionNumericCompare(t *testing.T) {
	if compareVersions("10.0.0", "9.9.9") <= 0 {
		t.Fatal("10.0.0 must sort above 9.9.9")
	}
	if compareVersions("6.1.1", "10.0.0") >= 0 {
		t.Fatal("6.1.1 must sort below 10.0.0")
	}
	p, h := t.TempDir(), t.TempDir()
	for _, v := range []string{"9.9.9", "10.0.0"} {
		writeSkill(t, filepath.Join(h, ".claude/plugins/cache/mp/sp", v, "skills"), "s", "s", "version "+v)
	}
	if err := os.MkdirAll(filepath.Join(p, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, ".claude/settings.json"), []byte(`{"enabledPlugins":["sp"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	list, _ := Discover(p, h)
	if len(list) != 1 || !strings.Contains(list[0].Dir, "10.0.0") {
		t.Fatalf("list = %+v", list)
	}
}
