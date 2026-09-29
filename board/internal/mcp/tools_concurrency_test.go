package mcp

import (
	"strings"
	"testing"
)

// TestBoardToolsDeclareConcurrency: the tool catalog is how an agent learns a
// board field exists. A field that decodes fine server-side but is missing from
// the inputSchema is invisible to every agent, and no round-trip test catches
// that — the Go decode ignores the schema entirely.
//
// The type must be "integer", not "number": these land in a Go int, so a client
// that took "number" at its word and sent 3.0 would get a decode error.
//
// The descriptions must also say what the dial is NOT. It is a per-board
// shaping knob with no global cap above it, counting cards rather than
// sessions, and blerg_board_update is the primary place an agent forms its
// understanding of it — so "not a ceiling" has to be in the text, or the next
// agent asked to cap spend reaches for this.
func TestBoardToolsDeclareConcurrency(t *testing.T) {
	for _, tool := range []string{"blerg_board_create", "blerg_board_update"} {
		def := toolNamed(t, tool)
		schema, _ := def["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(props)
		if props == nil {
			t.Fatalf("%s: inputSchema has no properties", tool)
		}
		field, ok := props["concurrency"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no `concurrency` in inputSchema — agents cannot see the field", tool)
		}
		if field["type"] != "integer" {
			t.Errorf("%s: concurrency type = %v, want \"integer\"", tool, field["type"])
		}
		desc, _ := field["description"].(string)
		if !strings.Contains(desc, "0 parks") {
			t.Errorf("%s: concurrency description does not explain that 0 parks the board: %q", tool, desc)
		}
		if !strings.Contains(strings.ToLower(desc), "not a spend") {
			t.Errorf("%s: concurrency description does not warn it is not a spend ceiling: %q", tool, desc)
		}
	}
}

func toolNamed(t *testing.T, name string) map[string]any {
	t.Helper()
	for _, d := range toolDefs {
		if d["name"] == name {
			return d
		}
	}
	t.Fatalf("no tool named %q in the catalog", name)
	return nil
}
