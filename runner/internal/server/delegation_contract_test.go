package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDelegationRoutesAreNotInTheAgentContract(t *testing.T) {
	private := http.NewServeMux()
	a := &API{}
	a.RegisterDelegationRoutes(private)
	contract := agentContractMux(a)
	spec := parseRunnerOpenAPI(t)
	manifest := RunnerManifest("https://runner.example.test")
	routes := []string{
		"GET /api/sessions/{id}/budget", "POST /api/sessions/{id}/children",
		"GET /api/delegation-requests/{id}", "POST /api/delegation-requests/{id}/expire",
		"POST /api/delegation-requests/{id}/decide", "GET /api/delegation-rules",
		"POST /api/delegation-rules", "DELETE /api/delegation-rules/{id}",
		"POST /api/delegation-grants/{id}/revoke", "GET /api/sessions/{id}/children",
		"GET /api/sessions/{id}/children/{child}", "POST /api/sessions/{id}/children/{child}/message",
		"POST /api/sessions/{id}/children/{child}/model", "POST /api/sessions/{id}/children/{child}/interrupt",
		"POST /api/sessions/{id}/children/{child}/stop", "POST /api/sessions/{id}/children/{child}/discard", "GET /api/sessions/{id}/children/{child}/result",
		"GET /api/sessions/{id}/children/{child}/events", "POST /api/sessions/{id}/children/{child}/uploads",
		"GET /api/sessions/{id}/children/{child}/artifacts",
		"GET /api/sessions/{id}/children/{child}/artifacts/{aid}/download",
	}
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			method, path, _ := strings.Cut(route, " ")
			target := strings.NewReplacer("{id}", "parent", "{child}", "child", "{aid}", "artifact").Replace(path)
			req := httptest.NewRequest(method, target, nil)
			if _, pattern := private.Handler(req); pattern == "" {
				t.Fatal("private route is missing")
			}
			if _, pattern := contract.Handler(req); pattern != "" {
				t.Fatalf("private route exposed by contract: %s", pattern)
			}
			if _, exists := spec.Paths[path][strings.ToLower(method)]; exists {
				t.Fatal("private route exposed by OpenAPI")
			}
			for _, operation := range manifest.Operations {
				if operation.Path == path && operation.Method == method {
					t.Fatal("private route exposed by public manifest")
				}
			}
		})
	}
}
