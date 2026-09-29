package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// boardOpenAPISpec is the subset of the document these tests reason about.
type boardOpenAPISpec struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"info"`
	Servers []any                     `json:"servers"`
	Paths   map[string]map[string]any `json:"paths"`
}

func parseBoardOpenAPI(t *testing.T) boardOpenAPISpec {
	t.Helper()
	var spec boardOpenAPISpec
	if err := json.Unmarshal(boardOpenAPIDoc, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	return spec
}

// The embedded document must be a well-formed OpenAPI 3.1 description of
// blerg-board, with no servers block: the manifest's base_url is the single
// source of truth for where board lives, and a baked-in server URL would be
// wrong on every install but the one it was written on.
func TestBoardOpenAPIDocumentShape(t *testing.T) {
	spec := parseBoardOpenAPI(t)
	if spec.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", spec.OpenAPI)
	}
	if spec.Info.Title != "blerg-board" {
		t.Errorf("info.title = %q, want blerg-board", spec.Info.Title)
	}
	if spec.Info.Version != "v1" {
		t.Errorf("info.version = %q, want v1", spec.Info.Version)
	}
	if len(spec.Servers) != 0 {
		t.Errorf("servers = %v, want empty", spec.Servers)
	}
}

// Every operation board advertises in its manifest entry must be described in
// openapi.json with the same method and path. This is what stops the two
// hand-maintained lists drifting apart.
func TestBoardOpenAPICoversEveryManifestOperation(t *testing.T) {
	spec := parseBoardOpenAPI(t)
	for _, op := range BoardManifest("https://board.example.test").Operations {
		methods, ok := spec.Paths[op.Path]
		if !ok {
			t.Errorf("manifest operation %s %s has no paths entry in openapi.json", op.Method, op.Path)
			continue
		}
		if _, ok := methods[strings.ToLower(op.Method)]; !ok {
			t.Errorf("manifest operation %s %s: openapi.json has the path but not the method", op.Method, op.Path)
		}
	}
}

// No phantom docs: every path openapi.json describes must actually be
// registered on board's route table.
//
// Board's production mux (cmd/blerg-board-server/main.go) ends in a "/" SPA
// fallback that answers every path, so asking ServeMux.Handler about the
// production mux would answer "routed" for anything at all. contractMux builds
// the same table WITHOUT that fallback — the agent-contract routes plus
// api.Routes — so an unrouted path matches nothing and Handler reports "".
func TestBoardOpenAPIPathsAreRegisteredOnTheMux(t *testing.T) {
	spec := parseBoardOpenAPI(t)
	mux := contractMux(&API{})
	for path, methods := range spec.Paths {
		for method := range methods {
			// A concrete request cannot carry a wildcard segment; "x" stands in for an id.
			target := strings.ReplaceAll(path, "{id}", "x")
			req := httptest.NewRequest(strings.ToUpper(method), target, nil)
			if _, pattern := mux.Handler(req); pattern == "" {
				t.Errorf("openapi.json documents %s %s but no route is registered for it", strings.ToUpper(method), path)
			}
		}
	}
}
