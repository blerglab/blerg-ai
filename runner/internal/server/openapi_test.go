package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// runnerOpenAPISpec is the subset of the document these tests reason about.
type runnerOpenAPISpec struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"info"`
	Servers []any                     `json:"servers"`
	Paths   map[string]map[string]any `json:"paths"`
}

func parseRunnerOpenAPI(t *testing.T) runnerOpenAPISpec {
	t.Helper()
	var spec runnerOpenAPISpec
	if err := json.Unmarshal(runnerOpenAPIDoc, &spec); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	return spec
}

// The embedded document must be a well-formed OpenAPI 3.1 description of
// blerg-runner, with no servers block: the manifest's base_url is the single
// source of truth for where the runner lives, and a baked-in server URL would
// be wrong on every install but the one it was written on.
func TestRunnerOpenAPIDocumentShape(t *testing.T) {
	spec := parseRunnerOpenAPI(t)
	if spec.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", spec.OpenAPI)
	}
	if spec.Info.Title != "blerg-runner" {
		t.Errorf("info.title = %q, want blerg-runner", spec.Info.Title)
	}
	if spec.Info.Version != "v1" {
		t.Errorf("info.version = %q, want v1", spec.Info.Version)
	}
	if len(spec.Servers) != 0 {
		t.Errorf("servers = %v, want empty", spec.Servers)
	}
}

// Every operation the runner advertises in its manifest entry must be described
// in openapi.json with the same method and path. This is what stops the two
// hand-maintained lists drifting apart.
func TestRunnerOpenAPICoversEveryManifestOperation(t *testing.T) {
	spec := parseRunnerOpenAPI(t)
	for _, op := range RunnerManifest("https://runner.example.test").Operations {
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
// registered on the runner's agent-contract mux. ServeMux.Handler reports the
// pattern a request matched; an unrouted path matches nothing and reports "".
func TestRunnerOpenAPIPathsAreRegisteredOnTheMux(t *testing.T) {
	spec := parseRunnerOpenAPI(t)
	mux := agentContractMux(&API{})
	for path, methods := range spec.Paths {
		for method := range methods {
			// A concrete request cannot carry a wildcard segment; "x" stands in for an id.
			target := strings.ReplaceAll(path, "{id}", "x")
			req := httptest.NewRequest(strings.ToUpper(method), target, nil)
			_, pattern := mux.Handler(req)
			if pattern == "" {
				t.Errorf("openapi.json documents %s %s but no route is registered for it", strings.ToUpper(method), path)
			}
		}
	}
}
