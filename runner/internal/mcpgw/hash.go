// Package mcpgw is the runner's MCP gateway: the only thing a session (or cron) with
// an MCP grant can reach. It authenticates each request by a per-(session, connection)
// token, enforces the grant's per-tool modes and pinned hashes, holds the upstream
// credentials, and proxies Streamable HTTP to the user's MCP servers.
package mcpgw

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ToolHash is the pin recorded for a tool: SHA-256, hex, over the canonical JSON (sorted
// keys, no insignificant whitespace, numbers kept verbatim) of the tool's name,
// description and input schema. The picker, the launch-time check and the gateway all
// use this one function, so a description or schema the upstream changes after the
// person confirmed it no longer matches and the tool is treated as off.
//
// The name is part of the hashed object (spec 6.3 names only description and schema):
// it costs nothing and stops a hash confirmed for one tool being replayed for another.
func ToolHash(name, description string, inputSchema json.RawMessage) string {
	var schema any
	if len(bytes.TrimSpace(inputSchema)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(inputSchema))
		dec.UseNumber()
		if err := dec.Decode(&schema); err != nil {
			// Not JSON at all: hash the bytes as a string so the result is still
			// deterministic and still changes when they do.
			schema = string(inputSchema)
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// A map marshals with sorted keys at every level.
	if err := enc.Encode(map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": schema,
	}); err != nil {
		// Unreachable for values decoded from JSON; fail closed with a hash nothing matches.
		return ""
	}
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return hex.EncodeToString(sum[:])
}
