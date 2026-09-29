package coreauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
)

// Registration is how core learns blerg-board exists: it must carry the whole
// manifest entry (operations and auth included), not just a name and a base
// URL, or core's aggregate GET /agents cannot document board at all.
func TestRegisterOnceSendsFullManifestEntry(t *testing.T) {
	var got agentsmanifest.ComponentEntry
	var authz string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/components" {
			t.Errorf("registered at %q, want /components", r.URL.Path)
		}
		authz = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode registration body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer core.Close()

	entry := agentsmanifest.ComponentEntry{
		Name:            "blerg-board",
		BaseURL:         "http://blerg-board:8080",
		Version:         "1.2.3",
		ContractVersion: agentsmanifest.ContractVersion,
		Capabilities:    []string{"board", "mcp"},
		DocsURL:         "http://blerg-board:8080/agents",
		OpenAPIURL:      "http://blerg-board:8080/openapi.json",
		MCPURL:          "http://blerg-board:8080/mcp",
		Auth:            &agentsmanifest.AuthInfo{Audience: "blerg-board", Presets: []string{"board"}, Accepts: []string{"agent_token", "human_session"}},
		Operations: []agentsmanifest.Operation{
			{Name: "create_card", Method: "POST", Path: "/api/boards/{id}/cards", Summary: "File a card.", Cap: "card.write"},
		},
	}
	if err := registerOnce(t.Context(), core.Client(), core.URL, "register-key", entry); err != nil {
		t.Fatalf("registerOnce: %v", err)
	}

	if authz != "Bearer register-key" {
		t.Errorf("Authorization = %q, want the register key", authz)
	}
	if got.Name != "blerg-board" || got.BaseURL != "http://blerg-board:8080" {
		t.Errorf("name/base_url = %q/%q", got.Name, got.BaseURL)
	}
	if got.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", got.Version)
	}
	if got.ContractVersion != agentsmanifest.ContractVersion {
		t.Errorf("contract_version = %q, want %q", got.ContractVersion, agentsmanifest.ContractVersion)
	}
	if len(got.Operations) != 1 || got.Operations[0].Cap != "card.write" {
		t.Errorf("operations = %+v, want the table as sent", got.Operations)
	}
	if got.Auth == nil || got.Auth.Audience != "blerg-board" {
		t.Errorf("auth = %+v, want audience blerg-board", got.Auth)
	}
	if got.OpenAPIURL == "" || got.MCPURL == "" || got.DocsURL == "" {
		t.Errorf("derived URLs missing: %+v", got)
	}
}

// A non-2xx answer is an error the caller retries on, not a silent success.
func TestRegisterOnceReportsStatusError(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer core.Close()

	err := registerOnce(t.Context(), core.Client(), core.URL, "bad-key", agentsmanifest.ComponentEntry{Name: "blerg-board"})
	if err == nil {
		t.Fatal("registerOnce() = nil, want an error on HTTP 403")
	}
}
