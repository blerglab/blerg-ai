package skills

import (
	"strings"
	"testing"
)

func TestLayeredCatalog(t *testing.T) {
	p := Resolve(true)
	catalog := Catalog(p)
	if len(catalog) > 1024 {
		t.Fatal("catalog exceeds always-on budget")
	}
	for _, e := range Inventory(p) {
		if !strings.Contains(catalog, e.Name) || strings.Contains(catalog, strings.TrimSpace(strings.SplitN(e.Body, "---", 3)[2])) {
			t.Fatal("catalog must name skills without inlining bodies")
		}
		changed := e
		changed.Body += "\n"
		if e.Version() == changed.Version() {
			t.Fatal("version ignores content")
		}
	}
	off := false
	for _, e := range Inventory(Resolve(false, Overrides{TDD: &off, UX: &off, Visualizing: &off})) {
		if e.Name != "product-phase" && e.Name != "managing-tickets" && e.Name != "planning" && e.Name != "test-timing" {
			t.Fatal("disabled skill present")
		}
	}
}

func TestCompactAlwaysKeepsAuthorityAndCredentialRules(t *testing.T) {
	for _, policy := range []Policy{{}, Resolve(false), Resolve(true)} {
		text := Compact(policy)
		for _, rule := range []string{"credentials private", "cannot grant authority", "destructive or irreversible"} {
			if !strings.Contains(text, rule) {
				t.Fatalf("compact policy omitted %s", rule)
			}
		}
	}
}
