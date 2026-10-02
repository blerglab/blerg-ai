package mcp

import (
	"os"
	"regexp"
	"testing"
)

// Every MCP tool needs an explicit project-scope decision. Adding a tool
// (to the catalog or to dispatch) without one fails here; at run time such a
// tool is refused for project-scoped tokens.
func TestEveryToolHasAScopeDecision(t *testing.T) {
	listed := map[string]bool{}
	for _, d := range toolDefs {
		name, _ := d["name"].(string)
		if name == "" {
			t.Fatalf("tool definition without a name: %v", d)
		}
		listed[name] = true
	}

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	dispatched := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\tcase ((?:"blerg_[a-z_]+"(?:, )?)+):`).FindAllStringSubmatch(string(src), -1) {
		for _, n := range regexp.MustCompile(`"(blerg_[a-z_]+)"`).FindAllStringSubmatch(m[1], -1) {
			dispatched[n[1]] = true
		}
	}
	if len(dispatched) < 15 {
		t.Fatalf("only %d dispatched tools found; the source scan is broken", len(dispatched))
	}

	for n := range listed {
		if !dispatched[n] {
			t.Errorf("tool %q is in the catalog but not dispatched", n)
		}
	}
	for n := range dispatched {
		if !listed[n] {
			t.Errorf("tool %q is dispatched but not in the catalog", n)
		}
		if _, ok := toolScopes[n]; !ok {
			t.Errorf("tool %q has no entry in toolScopes: decide how a project-scoped token is scoped on it", n)
		}
	}
	for n, s := range toolScopes {
		if !listed[n] {
			t.Errorf("toolScopes names %q, which is not a tool", n)
		}
		if s.why == "" {
			t.Errorf("tool %q: scope decision needs a reason", n)
		}
	}
}
