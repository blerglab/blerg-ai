package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func (a *API) liveFreshDefinition(ctx context.Context, proof mcpgw.Proof, connection, url, tool, hash string) (json.RawMessage, error) {
	cfg := a.hub.mcpStart.get()
	if cfg == nil {
		return nil, boundFailure("live_tool_unavailable")
	}
	lister, ok := cfg.Hasher.(liveToolListerComplete)
	if !ok {
		return nil, boundFailure("live_tool_unavailable")
	}
	tools, complete, err := lister.ListToolsComplete(ctx, proof, connection, url)
	if err != nil || !complete {
		return nil, boundFailure("live_tool_unavailable")
	}
	for _, t := range tools {
		if t.Name == tool && t.Unsafe == "" && readOnlyHint(t) {
			canonical, pin := mcpgw.CanonicalTool(t.Name, t.Description, t.InputSchema)
			if hash != "" && pin != hash {
				return nil, boundFailure("live_tool_changed")
			}
			return canonical, nil
		}
	}
	return nil, boundFailure("live_tool_not_read_only")
}

// This endpoint lists metadata only. It is never an invocation or capability
// verifier and is not mounted on /mcp-gw/ or the sandbox listener.
func (a *API) handleLiveDefinition(w http.ResponseWriter, r *http.Request) {
	if !a.liveDefinitionAuthenticated(r) {
		_ = boundFailure("live_definition_unauthenticated")
		writeError(w, 401, "Unavailable.")
		return
	}
	var body struct {
		AccountID  string `json:"account_id"`
		SessionID  string `json:"session_id"`
		TokenID    string `json:"token_id"`
		Connection string `json:"connection_id"`
		URL        string `json:"url"`
		Tool       string `json:"tool"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body) != nil {
		_ = boundFailure("invalid_subject")
		writeError(w, 400, "Unavailable.")
		return
	}
	definition, err := a.liveFreshDefinition(r.Context(), mcpgw.Proof{AccountID: body.AccountID, SessionID: body.SessionID, TokenID: body.TokenID}, body.Connection, body.URL, body.Tool, "")
	if err != nil {
		writeError(w, 409, "Tool unavailable.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"definition": definition, "read_only": true})
}

// Dedicated Core mTLS is required even during the disabled rollout. The host
// internal key alone cannot turn this metadata route into an upstream proxy.
func (a *API) liveDefinitionAuthenticated(r *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP", "Forwarded"} {
		if _, present := r.Header[http.CanonicalHeaderKey(h)]; present {
			return false
		}
	}
	if a.liveDataPeerVerifier != nil && a.liveDataPeerVerifier(r) != nil {
		return false
	}
	if a.coreInternalKey == "" || !tokenEqual(r.Header.Get("X-Internal-Key"), a.coreInternalKey) || a.liveDataCoreIdentity == "" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
		return false
	}
	leaf := r.TLS.VerifiedChains[0][0]
	return len(leaf.URIs) == 1 && leaf.URIs[0].String() == a.liveDataCoreIdentity
}

// SetLiveDataDefinitionIdentity takes the exact dedicated Core URI from trusted
// installation configuration. Never derive it from forwarding headers.
func (a *API) SetLiveDataDefinitionIdentity(uri string) { a.liveDataCoreIdentity = uri }

// SetLiveDataPeerVerifier restricts even public-listener callbacks to the dedicated CA.
func (a *API) SetLiveDataPeerVerifier(verify func(*http.Request) error) {
	a.liveDataPeerVerifier = verify
}
