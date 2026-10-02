package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
)

// Project scoping guard for the WHOLE server, not only the api package: every
// place in the module that registers an HTTP route (mux.Handle/HandleFunc,
// http.Handle/HandleFunc) must carry an explicit project-scope decision.
//
//   - routes registered in internal/api: api.RouteScopeDecision (routeScopes);
//   - /mcp and /ws: /mcp is one route, scoped per tool (internal/mcp
//     toolScopes, guarded by its own test); /ws is in routeScopes;
//   - everything registered here, in main.go: the table below;
//   - any OTHER file registering a route fails: it needs a decision first.

// mainRouteScopes is the decision for each route main.go registers itself.
var mainRouteScopes = map[string]string{
	"GET /api/version": "public static version string; no principal",
	"/mcp":             "one route for every tool; scoped per tool by internal/mcp toolScopes (TestEveryToolHasAScopeDecision), argument decode shared with the check",
	"/":                "static SPA assets and index.html; no principal, no data",
	"GET /healthz":     "public liveness probe; no principal",
}

var (
	registerCall = regexp.MustCompile(`\.(?:HandleFunc|Handle)\(`)
	registerLit  = regexp.MustCompile(`\.(?:HandleFunc|Handle)\(\s*"([^"]+)"`)
)

func TestEveryRegisteredRouteHasAScopeDecision(t *testing.T) {
	root := filepath.Join("..", "..") // the board module
	found := map[string]string{}      // pattern -> file
	var files int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "web", "vendor", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(src)
		calls := registerCall.FindAllString(text, -1)
		lits := registerLit.FindAllStringSubmatch(text, -1)
		if len(calls) == 0 && len(lits) == 0 {
			return nil
		}
		if len(calls) != len(lits) {
			t.Errorf("%s: %d route registrations but only %d with a literal pattern; use a literal so the scope guard can see the route", path, len(calls), len(lits))
		}
		rel, _ := filepath.Rel(root, path)
		inAPI := strings.HasPrefix(filepath.ToSlash(rel), "internal/api/")
		isMain := filepath.ToSlash(rel) == "cmd/blerg-board-server/main.go"
		if !inAPI && !isMain {
			t.Errorf("%s registers routes but is neither internal/api nor cmd/blerg-board-server/main.go: add a project-scope decision for them and teach this guard about the file", rel)
		}
		for _, m := range lits {
			pat := m[1]
			switch {
			case inAPI:
				if why, ok := api.RouteScopeDecision(pat); !ok || why == "" {
					t.Errorf("%s: route %q has no project-scope decision in api routeScopes", rel, pat)
				}
			case isMain:
				if why := mainRouteScopes[pat]; why == "" {
					t.Errorf("%s: route %q has no project-scope decision: add it to mainRouteScopes (or to api routeScopes)", rel, pat)
				}
			}
			found[pat] = rel
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 20 || len(found) < 50 {
		t.Fatalf("scanned %d files and found %d routes; the source scan is broken", files, len(found))
	}
	for pat := range mainRouteScopes {
		if found[pat] == "" {
			t.Errorf("mainRouteScopes names %q, which main.go does not register", pat)
		}
	}
	// /mcp and /ws are the two non-REST surfaces; both must be present and
	// decided (a rename would otherwise quietly drop the guard).
	if found["/mcp"] == "" {
		t.Error("/mcp is no longer registered where the guard expects it")
	}
	if why, ok := api.RouteScopeDecision("GET /ws"); !ok || why == "" || found["GET /ws"] == "" {
		t.Error("GET /ws has no project-scope decision")
	}
}
