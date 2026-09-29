package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// openAPISpec is the subset of the document these tests reason about.
type openAPISpec struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"info"`
	Servers []any                     `json:"servers"`
	Paths   map[string]map[string]any `json:"paths"`
}

func parseOpenAPI(t *testing.T) openAPISpec {
	t.Helper()
	var spec openAPISpec
	if err := json.Unmarshal(openAPIDoc, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	return spec
}

// The embedded document must be a well-formed OpenAPI 3.1 description of blerg-core, with no
// servers block: the manifest's base_url is the single source of truth for where core lives,
// and a baked-in server URL would be wrong on every install but the one it was written on.
func TestOpenAPIDocumentShape(t *testing.T) {
	spec := parseOpenAPI(t)
	if spec.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", spec.OpenAPI)
	}
	if spec.Info.Title != "blerg-core" {
		t.Errorf("info.title = %q, want blerg-core", spec.Info.Title)
	}
	if spec.Info.Version != "v1" {
		t.Errorf("info.version = %q, want v1", spec.Info.Version)
	}
	if len(spec.Servers) != 0 {
		t.Errorf("servers = %v, want empty", spec.Servers)
	}
}

// GET /openapi.json is public and serves the embedded document byte-for-byte.
func TestOpenAPIEndpointIsPublic(t *testing.T) {
	h := NewRouter(Deps{Audience: "blerg-core"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var spec openAPISpec
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("served body is not valid JSON: %v", err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Errorf("served openapi = %q, want 3.1.0", spec.OpenAPI)
	}
}

// Every operation core advertises in its manifest entry must be described in openapi.json with
// the same method and path. This is what stops the two hand-maintained lists drifting apart.
func TestOpenAPICoversEveryManifestOperation(t *testing.T) {
	spec := parseOpenAPI(t)
	for _, op := range coreEntry("https://core.example.com").Operations {
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

// No phantom docs: every path openapi.json describes must actually be registered on the mux.
// ServeMux.Handler reports the pattern a request matched; an unrouted path matches nothing and
// reports "".
func TestOpenAPIPathsAreRegisteredOnTheMux(t *testing.T) {
	spec := parseOpenAPI(t)
	mux, ok := NewRouter(Deps{Audience: "blerg-core"}).(*http.ServeMux)
	if !ok {
		t.Fatal("NewRouter no longer returns a *http.ServeMux; update this test")
	}
	for path, methods := range spec.Paths {
		for method := range methods {
			// A concrete request cannot carry a wildcard segment; "x" stands in for an id.
			target := strings.ReplaceAll(path, "{id}", "x")
			req := httptest.NewRequest(strings.ToUpper(method), target, nil)
			_, pattern := mux.Handler(req)
			if registered := pattern != ""; !registered {
				t.Errorf("openapi.json documents %s %s but no route is registered for it", strings.ToUpper(method), path)
			}
		}
	}
}
