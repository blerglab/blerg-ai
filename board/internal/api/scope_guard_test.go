package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// registeredPatterns scans the package's non-test sources for every mux
// registration. A route added through any other mechanism than
// mux.HandleFunc("<METHOD> <path>", ...) would escape this guard, so the
// scan also fails on a registration form it cannot read.
func registeredPatterns(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	reg := regexp.MustCompile(`\.(?:HandleFunc|Handle)\(\s*"([^"]+)"`)
	loose := regexp.MustCompile(`\.(?:HandleFunc|Handle)\(`)
	out := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		found := reg.FindAllStringSubmatch(text, -1)
		if n := len(loose.FindAllString(text, -1)); n != len(found) {
			t.Errorf("%s: %d mux registrations but only %d have a literal pattern; use a literal so the scope guard can see the route", f, n, len(found))
		}
		for _, m := range found {
			out[m[1]] = true
		}
	}
	return out
}

// Every registered route needs an explicit project-scope decision (and every
// decision must name a route that exists). Adding a route without one fails
// here; at run time such a route is refused for project-scoped tokens.
func TestEveryRouteHasAScopeDecision(t *testing.T) {
	reg := registeredPatterns(t)
	if len(reg) < 50 {
		t.Fatalf("only %d routes found; the source scan is broken", len(reg))
	}
	for p := range reg {
		rs, ok := routeScopes[p]
		if !ok {
			t.Errorf("route %q has no entry in routeScopes: decide how a project-scoped token is scoped on it", p)
			continue
		}
		if rs.why == "" {
			t.Errorf("route %q: scope decision needs a reason", p)
		}
	}
	for p := range routeScopes {
		if !reg[p] {
			t.Errorf("routeScopes names %q, which is not a registered route", p)
		}
	}
	// Id-addressed rules only make sense on a route with an {id} segment, and
	// board-addressed ones must not be mislabeled.
	for p, rs := range routeScopes {
		hasID := strings.Contains(p, "{id}")
		switch rs.rule {
		case scopeBoard, scopeBoardStart, scopeCardStart, scopeCard, scopeColumn, scopeReview, scopeStanding, scopeSession:
			if !hasID {
				t.Errorf("route %q: rule %d needs an {id} path segment", p, rs.rule)
			}
		}
	}
}
