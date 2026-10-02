package api

import (
	"encoding/json"
	"net/http"

	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

// internalMCPDefaultsRequest is POST /internal/mcp/connections/defaults' body.
type internalMCPDefaultsRequest struct {
	internalMCPProof
	ConnectionID string          `json:"connection_id"`
	DefaultTools json.RawMessage `json:"default_tools"`
}

// handleInternalMCPDefaults is POST /internal/mcp/connections/defaults: how the runner saves a
// person's default tool selection for one of their connections, on behalf of their signed-in
// browser session (the public PATCH needs a core-audience human token the runner does not hold).
//
// Gated like the other internal MCP routes (internal key, liveness proof, uniform 404 for a dead
// proof, another account's connection or an unknown id), with one difference: the proof must be a
// live human session_id. A token_id proof is refused, because a token started by an agent or a
// cron must never rewrite a person's defaults. default_tools is validated by exactly the code the
// public PATCH uses, and nothing else on the connection (above all the secret) can change here.
func (d Deps) handleInternalMCPDefaults(w http.ResponseWriter, r *http.Request) {
	var body internalMCPDefaultsRequest
	_, ok := d.internalMCPGateLimit(w, r, &body, &body.internalMCPProof, func() string {
		if body.TokenID != "" {
			return "session_id is required"
		}
		if !uuidShape.MatchString(body.ConnectionID) {
			return "connection_id must be a uuid"
		}
		return ""
	}, mcpPatchBodyLimit)
	if !ok {
		return
	}
	c, err := d.MCPConnections.Patch(r.Context(), body.AccountID, body.ConnectionID, mcpconn.PatchInput{DefaultTools: body.DefaultTools})
	if err != nil {
		writeMCPError(w, err)
		return
	}
	writeJSON(w, newInternalMCPConnection(c))
}
