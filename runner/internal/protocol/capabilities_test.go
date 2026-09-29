package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeCapabilities(t *testing.T) {
	in := CapabilitiesPayload{
		Engine:     "claude; rm -rf /",
		EngineName: "Claude\u202e Code",
		Model:      "claude-opus-5-5",
		Version:    "2.1.283",
		Note:       "see https://user:hunter2@example.com/x for details",
		Groups: []CapabilityGroup{
			{ID: "tools", Label: "Tools", Items: []CapabilityItem{{Name: "Bash"}, {Name: "Bash"}, {Name: "  "}, {Name: "Read", Status: "Connected"}}},
			{ID: "unknown", Label: "Unknown", Items: []CapabilityItem{{Name: "x"}}},
			{ID: "skills", Label: "", Items: []CapabilityItem{{Name: "s", Source: "user dir with spaces", Status: "needs-auth"}}},
			{ID: "tools", Label: "Dup", Items: []CapabilityItem{{Name: "Dup"}}},
		},
	}
	out := SanitizeCapabilities(in)
	if out.Engine != "" {
		t.Errorf("engine = %q, want dropped (not a token)", out.Engine)
	}
	if out.EngineName != "Claude Code" {
		t.Errorf("engine name = %q", out.EngineName)
	}
	if strings.Contains(out.Note, "hunter2") || !strings.Contains(out.Note, "https://…@example.com") {
		t.Errorf("note = %q, want credentials masked", out.Note)
	}
	if len(out.Groups) != 2 || out.Groups[0].ID != "skills" || out.Groups[1].ID != "tools" {
		t.Fatalf("groups = %+v, want skills then tools (fixed order, unknown and duplicate dropped)", out.Groups)
	}
	sk := out.Groups[0]
	if sk.Label != "skills" || sk.Items[0].Source != "" || sk.Items[0].Status != "needs-auth" {
		t.Errorf("skills = %+v", sk)
	}
	tools := out.Groups[1]
	if len(tools.Items) != 2 || tools.Items[1].Status != "connected" {
		t.Errorf("tools = %+v, want Bash and Read (deduped, blank dropped, status lowercased)", tools.Items)
	}
	if tools.Total != 0 {
		t.Errorf("total = %d, want 0 when nothing was cut", tools.Total)
	}
	if CapabilityCount(out) != 3 {
		t.Errorf("count = %d", CapabilityCount(out))
	}
	// Groups is never null on the wire.
	raw, _ := json.Marshal(SanitizeCapabilities(CapabilitiesPayload{}))
	if !strings.Contains(string(raw), `"groups":[]`) {
		t.Errorf("empty payload = %s", raw)
	}
}

func TestSanitizeCapabilitiesCaps(t *testing.T) {
	var items []CapabilityItem
	for i := 0; i < CapMaxItems+50; i++ {
		items = append(items, CapabilityItem{Name: strings.Repeat("n", i%5) + string(rune('A'+i%26)) + strings.Repeat("z", i/26), Description: strings.Repeat("d", 5000)})
	}
	out := SanitizeCapabilities(CapabilitiesPayload{Groups: []CapabilityGroup{{ID: "skills", Items: items, Total: 1 << 40}}})
	g := out.Groups[0]
	if len(g.Items) != CapMaxItems {
		t.Errorf("items = %d", len(g.Items))
	}
	if g.Total != 1_000_000 {
		t.Errorf("total = %d, want clamped", g.Total)
	}
	if n := len([]rune(g.Items[0].Description)); n != CapMaxDescription {
		t.Errorf("description runes = %d, want %d", n, CapMaxDescription)
	}
}
