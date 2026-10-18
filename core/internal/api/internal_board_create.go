package api

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// handleInternalBoardCreateExchange issues only a 30-second, one-intent capability.
// The authenticated Runner performs human approval and hashes the exact approved draft;
// Board verifies that digest and atomically consumes the globally unique intent. This
// credential never reaches an agent and grants no general read/write capability.
func (d Deps) handleInternalBoardCreateExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		internalMCPProof
		SessionRef string `json:"session_ref,omitempty"`
		BoardID    string `json:"board_id"`
		Intent     string `json:"intent"`
		Digest     string `json:"digest"`
	}
	svc, ok := d.internalTokenGate(w, r, &body)
	if !ok {
		return
	}
	if !uuidShape.MatchString(body.AccountID) || body.TokenID != "" || !uuidShape.MatchString(body.SessionID) {
		http.Error(w, "a live human session_id is required as proof", 400)
		return
	}
	capability, err := boardfiling.Capability(body.Intent, body.Digest)
	if err != nil || !validBoardID(body.BoardID) || (body.SessionRef != "" && !validExchangeString(body.SessionRef, maxExchangeSessionRefLn)) {
		http.Error(w, "invalid board filing intent, digest or target", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	deny := func(code int) { http.Error(w, http.StatusText(code), code) }
	// Consult both human liveness and current write authority again around the
	// remote access check and mint. A role change or logout cannot finish approval.
	liveWriter := func() (bool, error) {
		_, live, err := svc.HumanSessionLive(ctx, body.AccountID, body.SessionID)
		if err != nil || !live {
			return false, err
		}
		state, err := svc.AccountState(ctx, body.AccountID)
		return err == nil && !state.Disabled && !state.MustChangePassword && slices.Contains(identity.PlatformRoleCaps[state.Role], "card.write"), err
	}
	if live, err := liveWriter(); err != nil {
		deny(503)
		return
	} else if !live {
		deny(404)
		return
	}
	base, err := d.operatorBoardURL(ctx)
	if err != nil {
		deny(503)
		return
	}
	probe, err := svc.MintAgentToken(ctx, identity.AgentTokenInput{Sub: body.SessionID, Aud: "blerg-board", Project: body.BoardID, Caps: []string{"card.read"}, OnBehalfOf: body.AccountID, Lineage: body.AccountID, ExpiresAt: time.Now().Add(30 * time.Second)})
	if err != nil {
		deny(503)
		return
	}
	var board accessibleBoard
	if code := fetchBoard(ctx, base, "/api/boards/"+body.BoardID, probe, &board); code != 200 {
		deny(code)
		return
	}
	if board.ID != body.BoardID {
		deny(404)
		return
	}
	if live, err := liveWriter(); err != nil {
		deny(503)
		return
	} else if !live {
		deny(404)
		return
	}
	expiresAt := time.Now().Add(30 * time.Second)
	raw, err := svc.MintAgentToken(ctx, identity.AgentTokenInput{Sub: body.Intent, Aud: "blerg-board", Project: body.BoardID, Caps: []string{capability}, OnBehalfOf: body.AccountID, Lineage: body.AccountID, ExpiresAt: expiresAt})
	if err != nil {
		deny(503)
		return
	}
	if live, err := liveWriter(); err != nil {
		deny(503)
		return
	} else if !live {
		deny(404)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, internalTokenExchangeResponse{Token: raw, ExpiresAt: expiresAt.Format(time.RFC3339)})
}
