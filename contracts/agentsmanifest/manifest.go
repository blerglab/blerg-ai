// Package agentsmanifest holds the shared agent-facing contract: the manifest
// every component serves at GET /agents, and its Markdown rendering.
package agentsmanifest

import "encoding/json"

// ContractVersion is the version of the agent contract these types describe.
// Later changes add fields; they never rename or remove them.
const ContractVersion = "v1"

// AuthInfo says how an agent authenticates to a component (§1).
type AuthInfo struct {
	// Audience is the token "aud" this component verifies, e.g. "blerg-runner".
	Audience string `json:"audience"`
	// TokenEndpoint is core's absolute POST /api/tokens URL; core fills it in.
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	// Presets are the agent-token presets that grant access, e.g. ["run-sessions"].
	Presets []string `json:"presets"`
	// Accepts lists the principal kinds this component accepts, e.g.
	// ["agent_token", "human_session"].
	Accepts []string `json:"accepts"`
}

// MarshalJSON renders nil Presets and Accepts as empty arrays. The manifest is
// public documentation read by agents, so a list-valued field is always a list,
// never null, whatever the producing component left unset.
func (a AuthInfo) MarshalJSON() ([]byte, error) {
	type alias AuthInfo
	out := alias(a)
	if out.Presets == nil {
		out.Presets = []string{}
	}
	if out.Accepts == nil {
		out.Accepts = []string{}
	}
	return json.Marshal(out)
}

// Operation is one documented endpoint of a component (§1).
type Operation struct {
	Name    string `json:"name"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Summary string `json:"summary"`
	// Cap is the capability the caller needs, or "" when none is required.
	Cap        string `json:"cap,omitempty"`
	Idempotent bool   `json:"idempotent"`
}

// ComponentEntry is one enabled component in the aggregate /agents manifest (§4.3).
// Components send everything except LastSeen, Stale and Auth.TokenEndpoint, which
// core fills in.
type ComponentEntry struct {
	Name            string   `json:"name"`
	BaseURL         string   `json:"base_url"`
	Version         string   `json:"version"`
	ContractVersion string   `json:"contract_version"`
	Capabilities    []string `json:"capabilities"`
	LastSeen        int64    `json:"last_seen"`
	Stale           bool     `json:"stale"`

	// Description is one sentence about what the component does.
	Description string `json:"description,omitempty"`
	// DocsURL is <base_url>/agents (JSON, or Markdown via Accept). The Markdown
	// renderer deliberately lists it in the "Machine-readable" section so an
	// agent reading a component inside the aggregate guide can fetch that
	// component's own manifest directly.
	DocsURL string `json:"docs_url,omitempty"`
	// OpenAPIURL is <base_url>/openapi.json.
	OpenAPIURL string `json:"openapi_url,omitempty"`
	// MCPURL is <base_url>/mcp, or "" when the component has no MCP server.
	MCPURL string `json:"mcp_url,omitempty"`

	Auth       *AuthInfo   `json:"auth,omitempty"`
	Operations []Operation `json:"operations,omitempty"`
}

// MarshalJSON renders nil Capabilities as an empty array, for the same reason
// AuthInfo.MarshalJSON does: the manifest is public documentation and a
// list-valued field is always a list. Operations keeps its omitempty — an
// absent operations list means "not documented", not "no operations".
func (e ComponentEntry) MarshalJSON() ([]byte, error) {
	type alias ComponentEntry
	out := alias(e)
	if out.Capabilities == nil {
		out.Capabilities = []string{}
	}
	return json.Marshal(out)
}

// AggregateManifest is core's GET /agents document: the whole install at once.
type AggregateManifest struct {
	ContractVersion string `json:"contract_version"`
	CoreBaseURL     string `json:"core_base_url"`
	// Quickstart is Markdown, rendered verbatim into the agent guide.
	Quickstart string           `json:"quickstart"`
	Components []ComponentEntry `json:"components"`
}
