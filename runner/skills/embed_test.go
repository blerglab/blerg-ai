package skills

import (
	"os"
	"strings"
	"testing"
)

func TestBoardAndRunnerTicketRulesAgree(t *testing.T) {
	raw, err := os.ReadFile("../../board/skills/managing-cards/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	body := func(s string) string {
		p := strings.SplitN(s, "---", 3)
		if len(p) != 3 {
			t.Fatal("missing frontmatter")
		}
		return strings.TrimSpace(p[2])
	}
	if body(string(raw)) != body(ManagingTickets) {
		t.Fatal("Board and Runner ticket rules drifted; update both together")
	}
}

// Distribution is declared in the daemon and installer. Keep this guard here so
// the skill pack can be checked without compiling or running the daemon suite.
func TestTDDDistribution(t *testing.T) {
	testSkillDistribution(t, "test-driven-development", "TestDrivenDevelopment", TestDrivenDevelopment)
}

func TestUXDistribution(t *testing.T) {
	testSkillDistribution(t, "ux-design", "UXDesign", UXDesign)
}

func TestTestTimingDistribution(t *testing.T) {
	testSkillDistribution(t, "test-timing", "TestTiming", TestTiming)
}

func testSkillDistribution(t *testing.T, name, symbol, embedded string) {
	t.Helper()
	read := func(path string) string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	body := read(name + "/SKILL.md")
	if body != embedded {
		t.Errorf("embedded %s skill differs from distributed file", name)
	}
	provision := read("../internal/daemon/provision.go")
	if !strings.Contains(provision, `"`+name+`"`) {
		t.Errorf("%s skill missing from engine discovery links", name)
	}
	installer := read("../../install/desktop/daemon/install.sh")
	for _, engine := range []string{".claude", ".codex", ".agents", ".hermes"} {
		if !strings.Contains(provision, `"`+engine+`"`) {
			t.Errorf("missing discovery directory %s", engine)
		}
		if !strings.Contains(installer, engine+"/skills/"+name+"|skills/"+name) {
			t.Errorf("missing uninstall cleanup for %s", engine)
		}
	}
}

func TestPlanningDistribution(t *testing.T) {
	testSkillDistribution(t, "planning", "Planning", Planning)
}
