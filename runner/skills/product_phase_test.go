package skills

import (
	"strings"
	"testing"
)

func TestProductPhaseRules(t *testing.T) {
	bodies := map[string]Entry{}
	for _, e := range Inventory(Resolve(true)) {
		bodies[e.Name] = e
	}
	e, ok := bodies["product-phase"]
	if !ok {
		t.Fatal("product-phase missing from shipped inventory")
	}
	for _, phrase := range []string{"PoC / pre-1.0", "Beta", "1.0 / GA", "Scaling", "Enterprise", "Product phase:", "AGENTS.md", "blackboard", "person", "Don't do yet", "focused tests per task", "once per batch before review", "identity, privacy, auth or money", "spec first", "different model", "SOC 2", "browser", "compatibility", "performance", "documentation"} {
		if !strings.Contains(e.Body, phrase) {
			t.Errorf("product-phase missing %q", phrase)
		}
	}
	if e.Examples == "" {
		t.Error("missing linked examples")
	}
	testSkillDistribution(t, "product-phase", "ProductPhase", e.Body)
	for _, name := range []string{"planning", "coordinating", "test-driven-development"} {
		if !strings.Contains(bodies[name].Body, "[[product-phase]]") {
			t.Errorf("%s does not read phase guidance", name)
		}
	}
	for _, e := range Inventory(Policy{}) {
		if e.Name == "product-phase" {
			return
		}
	}
	t.Error("product-phase must remain discoverable with optional skills off")
}
