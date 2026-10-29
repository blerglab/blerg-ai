package daemon

import (
	"os"
	"strings"
	"testing"
)

func TestGuideNamesVisualFences(t *testing.T) {
	for name, guide := range map[string]string{"prompt": ccSessionGuide, "managed": managedBlock} {
		if !strings.Contains(guide, "mermaid") || !strings.Contains(guide, "vega-lite") || !strings.Contains(guide, "inline data") {
			t.Errorf("%s guide lacks safe visual fence instructions", name)
		}
		// BLG-329: agents learn what a chart may use, and what is refused.
		for _, want := range []string{"config", "layers, facets", "data transforms are not available yet", "transform, calculate, expr, params and url are refused"} {
			if !strings.Contains(guide, want) {
				t.Errorf("%s guide lacks chart rule %q", name, want)
			}
		}
	}
	skill, err := os.ReadFile("../../.claude/skills/session-messaging/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skill), "mermaid") || !strings.Contains(string(skill), "vega-lite") {
		t.Error("skill lacks visual fences")
	}
}
