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
	}
	skill, err := os.ReadFile("../../.claude/skills/session-messaging/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skill), "mermaid") || !strings.Contains(string(skill), "vega-lite") {
		t.Error("skill lacks visual fences")
	}
}
