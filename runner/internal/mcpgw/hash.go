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

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// MaxDefinitionBytes bounds a tool definition that can be stored and reviewed: a canonical form
// over it is recorded as unreviewable (ClassifyDefinition) and the tool stays off.
const MaxDefinitionBytes = 256 << 10

// CanonicalTool is the exact byte string a tool's pin hash is computed over: the canonical JSON
// (sorted keys, no insignificant whitespace, numbers kept verbatim) of the tool's name,
// description and input schema, with no trailing newline. It is what mcp_tool_definitions stores,
// so a review shows the text that was hashed and an accept can recompute the hash over it.
//
// The name is part of the hashed object (spec 6.3 names only description and schema):
// it costs nothing and stops a hash confirmed for one tool being replayed for another.
func CanonicalTool(name, description string, inputSchema json.RawMessage) (canonical []byte, hash string) {
	var schema any
	if len(bytes.TrimSpace(inputSchema)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(inputSchema))
		dec.UseNumber()
		if err := dec.Decode(&schema); err != nil {
			// Not JSON at all: carry the bytes as a string so the result is still
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
		return nil, ""
	}
	canonical = bytes.TrimRight(buf.Bytes(), "\n")
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:])
}

// ToolHash is the pin recorded for a tool: SHA-256, hex, of CanonicalTool. The picker, the
// launch-time check and the gateway all use this one function, so a description or schema the
// upstream changes after the person confirmed it no longer matches and the tool is treated as off.
func ToolHash(name, description string, inputSchema json.RawMessage) string {
	_, h := CanonicalTool(name, description, inputSchema)
	return h
}

// ClassifyDefinition says why a canonical definition cannot be shown to a person for review, or
// "" when it can: over MaxDefinitionBytes; a NUL in any string (the encoder writes it as \u0000,
// so the check is on the escape); a Unicode tag character (U+E0000–E007F) or a bidi control
// (U+202A–202E, U+2066–2069), which render as nothing in a diff while a model reads them. An
// unsafe definition is off everywhere and is never re-pinned or granted by any policy.
func ClassifyDefinition(canonical []byte) string {
	if len(canonical) > MaxDefinitionBytes {
		return db.UnsafeTooLarge
	}
	if hasNULEscape(canonical) {
		return db.UnsafeNUL
	}
	for _, r := range string(canonical) {
		switch {
		case r >= 0xE0000 && r <= 0xE007F,
			r >= 0x202A && r <= 0x202E,
			r >= 0x2066 && r <= 0x2069:
			return db.UnsafeHiddenChars
		}
	}
	return ""
}

// hasNULEscape reports whether the canonical JSON encodes a NUL: a `\u0000` escape preceded by an
// even number of backslashes. The literal six characters `\u0000` in a description are encoded as
// `\\u0000` (an odd run) and are not a NUL.
func hasNULEscape(canonical []byte) bool {
	needle := []byte(`\u0000`)
	for i := bytes.Index(canonical, needle); i >= 0; {
		n := 0
		for j := i - 1; j >= 0 && canonical[j] == '\\'; j-- {
			n++
		}
		if n%2 == 0 {
			return true
		}
		k := bytes.Index(canonical[i+1:], needle)
		if k < 0 {
			break
		}
		i += 1 + k
	}
	return false
}
