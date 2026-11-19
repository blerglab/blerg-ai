package server

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// Presentation verifies resource ownership/access as well as human liveness.
// It never substitutes the viewer's current login for stored grant provenance.
func (a *API) permissionResourceAuthority(ctx context.Context, seen map[string]string, account, sid, kind, resource string) string {
	state := a.permissionAuthority(ctx, seen, account, sid)
	if state != "active" {
		return state
	}
	cfg := a.hub.mcpStart.get()
	c, ok := cfg.Core.(*HTTPMCPCoreClient)
	if !ok {
		return state
	} // injected Core providers own their resource contract
	key := account + "\x00" + sid + "\x00" + kind + "\x00" + resource
	if cached, ok := seen[key]; ok {
		return cached
	}
	proof := mcpgw.Proof{AccountID: account, SessionID: sid}
	found := false
	var err error
	switch kind {
	case "board":
		boards, listErr := c.listSessionBoards(ctx, proof, resource, true)
		err = listErr
		for _, board := range boards {
			if board.ID == resource {
				found = true
			}
		}
	case "mcp":
		connections, listErr := c.listConnections(ctx, proof, true)
		err = listErr
		for _, connection := range connections {
			if connection.ID == resource {
				found = true
			}
		}
	}
	switch {
	case errors.Is(err, mcpgw.ErrBoardOwnerMissing):
		state = "board_owner_missing"
	case errors.Is(err, errStandingSuspended):
		state = "suspended"
	case errors.Is(err, errStandingRevoked), errors.Is(err, ErrMCPProofInvalid):
		state = "revoked"
	case err != nil:
		state = "unavailable"
	case !found:
		state = "revoked"
	}
	seen[key] = state
	return state
}

// Provider-session bindings remain tied to their exact original login. They
// are deliberately absent from account connection discovery.
func (a *API) permissionScopedMCPAuthority(ctx context.Context, account string, scope *db.MCPSessionScope) string {
	cfg := a.hub.mcpStart.get()
	if cfg == nil {
		return "unavailable"
	}
	c, ok := cfg.Core.(*HTTPMCPCoreClient)
	if !ok {
		return "unavailable"
	}
	err := verifyCorePrincipal(ctx, c.httpClient(), c.BaseURL, c.InternalKey, account, coreProof{SessionID: scope.HumanSessionID})
	if err == nil {
		return "active"
	}
	if errors.Is(err, errCorePrincipalRefused) {
		return "revoked"
	}
	return "unavailable"
}
