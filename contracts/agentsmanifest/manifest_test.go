package agentsmanifest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func fullEntry() ComponentEntry {
	return ComponentEntry{
		Name:            "blerg-runner",
		BaseURL:         "https://runner.example.test",
		Version:         "1.2.3",
		ContractVersion: ContractVersion,
		Capabilities:    []string{"session.start"},
		LastSeen:        1700000000,
		Stale:           false,
		Description:     "Runs Claude Code sessions against a repository.",
		DocsURL:         "https://runner.example.test/agents",
		OpenAPIURL:      "https://runner.example.test/openapi.json",
		MCPURL:          "https://runner.example.test/mcp",
		Auth: &AuthInfo{
			Audience:      "blerg-runner",
			TokenEndpoint: "https://core.example.test/api/tokens",
			Presets:       []string{"run-sessions"},
			Accepts:       []string{"agent_token", "human_session", "runner_key"},
		},
		Operations: []Operation{
			{
				Name:       "start",
				Method:     "POST",
				Path:       "/api/runner/start",
				Summary:    "Start a session.",
				Cap:        "session.start",
				Idempotent: true,
			},
			{
				Name:    "status",
				Method:  "GET",
				Path:    "/api/runner/sessions/{id}",
				Summary: "Read session status.",
			},
		},
		UI: &UIInfo{ChatPackage: &PackageInfo{
			Name:    "@blerglab/chat",
			Version: "0.1.0",
			URL:     "https://runner.example.test/packages/blerglab-chat-0.1.0.tgz",
			SHA512:  "sha512-AAAA",
			DocsURL: "https://runner.example.test/packages/@blerglab/chat/README.md",
		}},
	}
}

// The UI field names what an app installs: every key an installer needs is spelled out, and a
// component with no UI omits the field rather than sending an empty object.
func TestUIInfoJSON(t *testing.T) {
	b, err := json.Marshal(fullEntry())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	var ui map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw["ui"], &ui); err != nil {
		t.Fatalf("unmarshal ui: %v", err)
	}
	for _, key := range []string{"name", "version", "url", "sha512", "docs_url"} {
		if _, ok := ui["chat_package"][key]; !ok {
			t.Errorf("ui.chat_package is missing key %q, got %s", key, raw["ui"])
		}
	}
	b, err = json.Marshal(ComponentEntry{Name: "blerg-board"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"ui"`) {
		t.Errorf("a component with no UI must omit the field, got %s", b)
	}
}

func TestComponentEntryJSONRoundTrip(t *testing.T) {
	want := fullEntry()
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ComponentEntry
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip lost fields:\n got %+v\nwant %+v", got, want)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for _, key := range []string{
		"name", "base_url", "version", "contract_version", "capabilities", "last_seen", "stale",
		"description", "docs_url", "openapi_url", "mcp_url", "auth", "operations",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("JSON is missing key %q", key)
		}
	}

	var authRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw["auth"], &authRaw); err != nil {
		t.Fatalf("unmarshal auth: %v", err)
	}
	for _, key := range []string{"audience", "token_endpoint", "presets", "accepts"} {
		if _, ok := authRaw[key]; !ok {
			t.Errorf("auth JSON is missing key %q", key)
		}
	}

	var ops []map[string]json.RawMessage
	if err := json.Unmarshal(raw["operations"], &ops); err != nil {
		t.Fatalf("unmarshal operations: %v", err)
	}
	for _, key := range []string{"name", "method", "path", "summary", "cap", "idempotent"} {
		if _, ok := ops[0][key]; !ok {
			t.Errorf("operation JSON is missing key %q", key)
		}
	}
	if _, ok := ops[1]["cap"]; ok {
		t.Errorf("operation with no cap should omit %q", "cap")
	}
}

func TestComponentEntryOmitsEmptyNewFields(t *testing.T) {
	b, err := json.Marshal(ComponentEntry{Name: "blerg-board"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for _, key := range []string{"description", "docs_url", "openapi_url", "mcp_url", "auth", "operations"} {
		if _, ok := raw[key]; ok {
			t.Errorf("empty field %q should be omitted, got %s", key, b)
		}
	}
	for _, key := range []string{"name", "base_url", "version", "contract_version", "capabilities", "last_seen", "stale"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("existing field %q must always be present, got %s", key, b)
		}
	}
}

func TestNilListsMarshalAsEmptyArrays(t *testing.T) {
	b, err := json.Marshal(ComponentEntry{Name: "blerg-board", Auth: &AuthInfo{Audience: "blerg-board"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"capabilities":[]`, `"presets":[]`, `"accepts":[]`} {
		if !strings.Contains(got, want) {
			t.Errorf("want %s in %s", want, got)
		}
	}
	if strings.Contains(got, "null") {
		t.Errorf("no manifest list may serialise as null, got %s", got)
	}
}

func TestNilListsMarshalAsEmptyArraysInsideAggregate(t *testing.T) {
	b, err := json.Marshal(AggregateManifest{
		ContractVersion: ContractVersion,
		Components:      []ComponentEntry{{Name: "blerg-board", Auth: &AuthInfo{}}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"capabilities":[]`, `"presets":[]`, `"accepts":[]`} {
		if !strings.Contains(got, want) {
			t.Errorf("want %s in %s", want, got)
		}
	}
}

func TestAggregateManifestJSONNames(t *testing.T) {
	b, err := json.Marshal(AggregateManifest{
		ContractVersion: ContractVersion,
		CoreBaseURL:     "https://core.example.test",
		Quickstart:      "1. Read this document.",
		Components:      []ComponentEntry{fullEntry()},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for _, key := range []string{"contract_version", "core_base_url", "quickstart", "components"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("aggregate JSON is missing key %q, got %s", key, b)
		}
	}
}

func TestContractVersionIsV1(t *testing.T) {
	if ContractVersion != "v1" {
		t.Errorf("ContractVersion = %q, want %q", ContractVersion, "v1")
	}
}
