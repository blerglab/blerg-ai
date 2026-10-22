package api

import (
	"context"
	"log"
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

// newBoardBeforeRecheck is a test seam: it runs between the mint and the second liveness and role
// check of handleInternalNewBoardExchange.
var newBoardBeforeRecheck func()

// handleInternalNewBoardExchange is POST /internal/tokens/exchange/board-new: the person approved a
// session's proposed board in Runner, and Runner asks for the one credential that creates exactly
// that board. Creating a board needs board.admin, so the person's account must hold it now (an
// administrator); the session that proposed it never does. The credential is a 30-second,
// one-intent capability bound to the approved draft's digest (boardfiling.BoardCapability); it has
// no board scope and no other capability, Board consumes its intent atomically, and it never reaches
// an agent.
func (d Deps) handleInternalNewBoardExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		internalMCPProof
		SessionRef string `json:"session_ref,omitempty"`
		Intent     string `json:"intent"`
		Digest     string `json:"digest"`
	}
	svc, ok := d.internalTokenGate(w, r, &body)
	if !ok {
		return
	}
	if !uuidShape.MatchString(body.AccountID) || body.TokenID != "" || !uuidShape.MatchString(body.SessionID) {
		http.Error(w, "a live human session_id is required as proof", http.StatusBadRequest)
		return
	}
	capability, err := boardfiling.BoardCapability(body.Intent, body.Digest)
	if err != nil || (body.SessionRef != "" && !validExchangeString(body.SessionRef, maxExchangeSessionRefLn)) {
		http.Error(w, "invalid board creation intent or digest", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	liveAdmin := func() (live, admin bool, err error) {
		if _, live, err = svc.HumanSessionLive(ctx, body.AccountID, body.SessionID); err != nil || !live {
			return false, false, err
		}
		state, err := svc.AccountState(ctx, body.AccountID)
		if err != nil || state.Disabled || state.MustChangePassword {
			return false, false, err
		}
		return true, slices.Contains(identity.PlatformRoleCaps[state.Role], "board.admin"), nil
	}
	check := func() bool {
		live, admin, err := liveAdmin()
		switch {
		case err != nil:
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		case !live:
			// JSON, so the runner can tell "this sign-in is not live" from a core too old to have this
			// route (the mux's plain-text 404).
			writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not_live"})
		case !admin:
			http.Error(w, "only an administrator can create boards", http.StatusForbidden)
		default:
			return true
		}
		return false
	}
	if !check() {
		return
	}
	expiresAt := time.Now().Add(30 * time.Second)
	raw, err := svc.MintAgentToken(ctx, identity.AgentTokenInput{Sub: body.Intent, Aud: "blerg-board", Caps: []string{capability}, OnBehalfOf: body.AccountID, Lineage: body.AccountID, ExpiresAt: expiresAt})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	// A sign-out or a role change while minting cannot finish the approval.
	if newBoardBeforeRecheck != nil {
		newBoardBeforeRecheck()
	}
	if !check() {
		return
	}
	log.Printf("internal tokens: board creation capability for account %s (intent %s, runner session %q)", body.AccountID, body.Intent, body.SessionRef)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, internalTokenExchangeResponse{Token: raw, ExpiresAt: expiresAt.Format(time.RFC3339)})
}
