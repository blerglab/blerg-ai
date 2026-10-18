package protocol

import "github.com/blerglab/blerg-ai/contracts/pluginspec"

// ApplySessionPlugins contains owner-approved additions to the currently applied session plugin list.
// Only a session whose daemon advertises SessionPluginEngines may receive it.
// The daemon revalidates the allowlist and applies it between turns only.
type ApplySessionPlugins struct {
	Type      string             `json:"type"`
	RequestID string             `json:"request_id"`
	SessionID string             `json:"session_id"`
	Plugins   []pluginspec.Entry `json:"plugins"`
}

// SessionPluginsResult is asynchronous: queued is nonterminal. All other states
// (applied, failed, unsupported) are terminal; Plugins names only applied specs.
type SessionPluginsResult struct {
	Type      string             `json:"type"`
	RequestID string             `json:"request_id"`
	SessionID string             `json:"session_id"`
	State     string             `json:"state"`
	Error     string             `json:"error,omitempty"`
	Plugins   []pluginspec.Entry `json:"plugins,omitempty"`
}
