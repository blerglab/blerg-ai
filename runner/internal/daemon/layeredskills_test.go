package daemon

import (
	"os"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/skills"
)

func TestSessionGuideLoadsSkillsOnDemand(t *testing.T) {
	text := ccSystemPromptForPolicy("", skills.Resolve(true))
	for _, e := range skills.Inventory(skills.Resolve(true)) {
		if strings.Contains(text, workflowSkillGuide(e.Body)) {
			t.Errorf("%s is still always loaded", e.Name)
		}
		if !strings.Contains(text, e.Name) {
			t.Errorf("catalog omits %s", e.Name)
		}
	}
}

func TestSkillPackPolicyAndCost(t *testing.T) {
	off := false
	for _, main := range []bool{false, true} {
		policy := skills.Resolve(main, skills.Overrides{UX: &off})
		pack, err := prepareSkillPack(sandboxExec{}, policy)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pack.remove)
		for _, entry := range pack.entries {
			raw, err := os.ReadFile(pack.dir + "/skills/" + entry.Name + "/SKILL.md")
			if err != nil || string(raw) != entry.Body {
				t.Fatalf("missing body %s: %v", entry.Name, err)
			}
			if entry.Examples == "" {
				continue
			}
			raw, err = os.ReadFile(pack.dir + "/skills/" + entry.Name + "/examples.md")
			if err != nil || string(raw) != entry.Examples {
				t.Fatalf("missing examples file %s: %v", entry.Name, err)
			}
		}
		if _, err := os.Stat(pack.dir + "/skills/ux-design"); !os.IsNotExist(err) {
			t.Fatal("disabled skill installed")
		}
		if strings.Contains(pack.guide(), "coordinating") != main {
			t.Fatal("role leaked")
		}
	}
	policy := skills.Resolve(true)
	after := ccSystemPromptForPolicy("", policy)
	before := after
	for _, entry := range skills.Inventory(policy) {
		before = strings.ReplaceAll(before, skillCatalogEntry(entry.Name), workflowSkillGuide(entry.Body))
	}
	t.Logf("Main shared prompt bytes: before=%d after=%d savings=%d", len(before), len(after), len(before)-len(after))
	if len(after)*100/len(before) > 70 {
		t.Fatal("layering saves less than 30 percent")
	}
}

func TestSkillContextByteCost(t *testing.T) {
	for _, main := range []bool{false, true} {
		p := skills.Resolve(main)
		pack, err := prepareSkillPack(sandboxExec{}, p)
		if err != nil {
			t.Fatal(err)
		}
		bodies := 0
		for _, e := range pack.entries {
			bodies += len(e.Body)
		}
		shared := len(ccSystemPromptForPolicy("", p))
		initial := shared + len(pack.guide())
		t.Logf("main=%v shared=%d initial_with_private_paths=%d ux_once=%d ux_twice=%d all_skills_once=%d compact_skill_bytes=%d", main, shared, initial, initial+len(skills.UXDesign), initial+2*len(skills.UXDesign), initial+bodies, len(skillsCompact(p)))
		pack.remove()
	}
}

// Measure the actual Main prompt, including production catalog entries and the
// exact-path guide. A fixed representative state root makes the cap repeatable.
func TestSkillProductionContextBudget(t *testing.T) {
	policy := skills.Resolve(true)
	pack := &skillPack{dir: "/home/example/.blerg-runner-daemon/skill-packs/" + strings.Repeat("a", 64), entries: skills.Inventory(policy)}
	prompt := ccSystemPromptForPolicy("", policy)
	for _, entry := range pack.entries {
		if !strings.Contains(prompt, skillCatalogEntry(entry.Name)) {
			t.Fatalf("production catalog missing %s", entry.Name)
		}
	}
	size := len(prompt) + len(pack.guide())
	// Five always-available core skills, including the required planning skill
	// (BLG-380), cost at least 13,381 bytes even with its catalog line trimmed to
	// "Maintain the blackboard" — 69 bytes past the old 13 KiB (13,312) cap, which
	// has no room left for a fifth entry at any description length. Raised by a
	// measured 256 bytes, not rounded up to absorb future bloat. Two more core skills,
	// visualizing (BLG-469) and test-timing (BLG-486), each add one catalog line:
	// raised by their measured 441 bytes (14,009 in all), again not rounded up.
	const budget = 13*1024 + 256 + 441
	t.Logf("Main initial context: %d bytes; cap %d bytes", size, budget)
	if size > budget {
		t.Fatalf("Main context exceeds %d KiB: %d", budget/1024, size)
	}
}

func TestCoordinatingRetainsOperationalRules(t *testing.T) {
	for _, rule := range []string{"no version prefixes", "without private history", "severity, evidence and missing coverage", "Frontend-only batches run their region", "only when that code changed", "Unblock stalled work before the person asks", "needs no full gate", "fields are unavailable", "one heavy suite at a time", "where it came from, plain options, your pick", "apart from status"} {
		if !strings.Contains(skills.Coordinating, rule) {
			t.Errorf("missing coordination rule: %s", rule)
		}
	}
	off := false
	prompt := ccSystemPromptForPolicy("", skills.Resolve(true, skills.Overrides{UX: &off}))
	if !strings.Contains(prompt, "Put settings in the existing settings panel") {
		t.Fatal("settings placement depends on UX skill")
	}
}
