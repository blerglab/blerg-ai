package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func TestDerivePreset(t *testing.T) {
	ro := json.RawMessage(`{"readOnlyHint":true}`)
	live := []mcpgw.ListedTool{{Name: "read", Hash: "r", Annotations: ro}, {Name: "send", Hash: "s"}}
	allow := map[string]mcpgw.ToolGrant{"read": {Mode: "allow", Hash: "r"}, "send": {Mode: "allow", Hash: "s"}}
	approval := map[string]mcpgw.ToolGrant{"read": {Mode: "allow", Hash: "r"}, "send": {Mode: "propose", Hash: "s"}}
	partial := map[string]mcpgw.ToolGrant{"read": {Mode: "allow", Hash: "r"}}
	for _, tc := range []struct {
		claimed string
		tools   map[string]mcpgw.ToolGrant
		want    string
	}{
		{"allow_all", allow, db.PresetAllowAll},
		{"approval", approval, db.PresetApproval},
		{"approval", allow, db.PresetCustom},    // the claim does not match the modes
		{"allow_all", partial, db.PresetCustom}, // a tool left off
		{"", allow, db.PresetCustom},            // an older client claims nothing
		{"bogus", allow, db.PresetCustom},
	} {
		if got := derivePreset(tc.claimed, tc.tools, live); got != tc.want {
			t.Errorf("derivePreset(%q, %v) = %q, want %q", tc.claimed, tc.tools, got, tc.want)
		}
	}
	// An unreviewable live tool is off in the picker and does not count against a preset.
	withUnsafe := append([]mcpgw.ListedTool{{Name: "odd", Hash: "o", Unsafe: "hidden_chars"}}, live...)
	if got := derivePreset("allow_all", allow, withUnsafe); got != db.PresetAllowAll {
		t.Errorf("unsafe tool counted: %q", got)
	}
}

func TestResolveGrantRecordsPresetKnownToolsAndDefinitions(t *testing.T) {
	fx := newMCPFx(t)
	ctx := context.Background()
	sel := []MCPSelection{{Connection: fx.connID, Preset: "allow_all", Tools: map[string]MCPToolSelection{
		"echo": {Mode: "allow", Hash: fx.up.hash("echo")},
		"send": {Mode: "allow", Hash: fx.up.hash("send")},
	}}}
	g, apiErr := fx.api.resolveGrant(ctx, humanWho(), sel, clusterTarget())
	if apiErr != nil {
		t.Fatalf("resolveGrant: %v", apiErr)
	}
	spec := g.Connections[0]
	if spec.Preset != db.PresetAllowAll {
		t.Fatalf("preset = %q", spec.Preset)
	}
	if len(spec.KnownTools) != 2 || spec.KnownTools[0] != "echo" || spec.KnownTools[1] != "send" {
		t.Fatalf("known_tools = %v", spec.KnownTools)
	}
	for _, name := range []string{"echo", "send"} {
		if d, err := db.GetToolDefinition(ctx, fx.pool, fx.up.hash(name)); err != nil || d.Name != name {
			t.Fatalf("definition of %s: %+v %v", name, d, err)
		}
	}
	// Defaults (tools omitted) are custom.
	g, apiErr = fx.api.resolveGrant(ctx, humanWho(), []MCPSelection{{Connection: fx.connID}}, clusterTarget())
	if apiErr != nil || g.Connections[0].Preset != db.PresetCustom {
		t.Fatalf("defaults preset: %+v %v", g, apiErr)
	}
}

// A selected tool whose live definition cannot be reviewed is refused at the picker, even when the
// selection carries its live hash.
func TestResolveGrantRefusesAnUnreviewableSelectedTool(t *testing.T) {
	fx := newMCPFx(t)
	fx.up.setDescription("echo", "reads a file \u202E and more")
	sel := []MCPSelection{{Connection: fx.connID, Tools: map[string]MCPToolSelection{
		"echo": {Mode: "allow", Hash: fx.up.hash("echo")},
	}}}
	_, apiErr := fx.api.resolveGrant(context.Background(), humanWho(), sel, clusterTarget())
	if apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity || !strings.Contains(apiErr.Message, "cannot be reviewed") {
		t.Fatalf("resolveGrant = %v, want a 422 naming that it cannot be reviewed", apiErr)
	}
}
