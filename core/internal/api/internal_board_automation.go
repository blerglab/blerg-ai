package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// This file implements "Run as me" (POST /internal/tokens/board-automation and .../revoke): the
// board asks core to mint a run-sessions agent token for the person signed in to it, and stores
// the token as the board's automation token without anyone copying it.
//
// Trust model, in short (the full threat model is in board/docs/CONFIG.md):
//
//   - Caller: the board, proved by BLERG_CORE_BOARD_MINT_KEY. That key opens these two routes and
//     nothing else; the internal key (credential fetches) does not open them and they do not
//     accept it, so the board never holds a key that reads engine credentials.
//   - Person: proved by the board-audience access token their browser sent the board on this
//     very request. Core verifies it as its own token (signature, audience blerg-board, expiry,
//     revocations), requires kind human, a sid naming a LIVE browser session of that same
//     account, an account that is enabled and past its first password change, and an account_id
//     in the body equal to the token's subject. Agent tokens, runner session tokens, tokens for
//     another audience, tokens with no or a dead sid, and another account's token are all the
//     uniform 404. The key alone mints nothing: it needs a live person's short-lived token.
//   - What comes back: an ordinary run-sessions token of that person (role-intersected caps, the
//     50-token cap, the default 90-day life), listed in their Settings with source "board <id>".
//     The value travels only in this response body, which is never cached or logged.
//   - Rate: every attempt counts against the account (BoardMintLimit per BoardMintWindow).
//   - Revoke: the key can revoke only a token whose source names that board, of that account.

// BoardMintLimit / BoardMintWindow: at most this many "Run as me" attempts per account per
// window. Ten an hour is far above a person clicking a button and far below a loop.
const (
	BoardMintLimit  = 10
	BoardMintWindow = time.Hour
)

const (
	maxBoardMintAccessTokenLen = 8 << 10
	maxBoardMintBoardNameLen   = 200
)

type boardAutomationMintRequest struct {
	AccountID   string `json:"account_id"`
	BoardID     string `json:"board_id"`
	BoardName   string `json:"board_name"`
	AccessToken string `json:"access_token"`
}

// boardAutomationMintResponse is the only place the minted token's value is ever written.
type boardAutomationMintResponse struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	Name      string `json:"name"`
	ExpiresAt string `json:"expires_at"`
}

type boardAutomationRevokeRequest struct {
	AccountID string `json:"account_id"`
	TokenID   string `json:"token_id"`
	BoardID   string `json:"board_id"`
}

// boardMintGate is the shared front of both routes: the routes exist only with a configured
// key, the caller must present it (and not through a proxy), and the body is capped and decoded.
func (d Deps) boardMintGate(w http.ResponseWriter, r *http.Request, body any) (*identity.Service, bool) {
	if d.BoardMintKey == "" {
		http.Error(w, "board automation minting disabled", http.StatusServiceUnavailable)
		return nil, false
	}
	if !validInternalKey(r, d.BoardMintKey) {
		http.Error(w, "invalid board key", http.StatusUnauthorized)
		return nil, false
	}
	svc, ok := d.identitySvc()
	if !ok || d.Store == nil {
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return nil, false
	}
	return svc, true
}

// handleBoardAutomationMint is POST /internal/tokens/board-automation.
func (d Deps) handleBoardAutomationMint(w http.ResponseWriter, r *http.Request) {
	if d.BoardMintDisabled {
		// The header tells the board this is the operator's switch, not a missing mint key.
		w.Header().Set("Blerg-Mint-Disabled", "1")
		http.Error(w, "board automation minting disabled", http.StatusServiceUnavailable)
		return
	}
	var body boardAutomationMintRequest
	svc, ok := d.boardMintGate(w, r, &body)
	if !ok {
		return
	}
	switch {
	case !uuidShape.MatchString(body.AccountID):
		http.Error(w, "account_id must be a uuid", http.StatusBadRequest)
		return
	case !validBoardID(body.BoardID):
		http.Error(w, "board_id is required, at most 64 characters, no control characters", http.StatusBadRequest)
		return
	case !validExchangeString(body.BoardName, maxBoardMintBoardNameLen):
		http.Error(w, "board_name is required, at most 200 characters, no control characters", http.StatusBadRequest)
		return
	case body.AccessToken == "" || len(body.AccessToken) > maxBoardMintAccessTokenLen:
		http.Error(w, "access_token is required", http.StatusBadRequest)
		return
	}

	// Every attempt counts, refused or not: the route must not be a free oracle for tokens.
	if d.BoardMintLimiter != nil {
		if allowed, retryAfter := d.BoardMintLimiter.Take(body.AccountID, body.AccountID); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			http.Error(w, "too many attempts — try again later", http.StatusTooManyRequests)
			return
		}
	}

	deny := func(why string) {
		// Ids and a reason only: never the token, never the verification detail to the caller.
		log.Printf("internal tokens board-automation: refused for account %s (board %s): %s", body.AccountID, body.BoardID, why)
		http.Error(w, "not found", http.StatusNotFound)
	}
	keys, err := d.Identity.JWKS(r.Context())
	if err != nil {
		http.Error(w, "key lookup failed", http.StatusServiceUnavailable)
		return
	}
	checker, err := d.revChecker(r.Context())
	if err != nil {
		http.Error(w, "revocation check unavailable", http.StatusServiceUnavailable)
		return
	}
	p, err := cid.Verify(body.AccessToken, "blerg-board", keys, checker, sensitiveCaps)
	switch {
	case err != nil:
		deny("access token does not verify for blerg-board")
		return
	case p.Kind != string(cid.Human):
		deny("access token is not a person's")
		return
	case p.Sub != body.AccountID:
		deny("access token belongs to another account")
		return
	case p.Sid == "":
		deny("access token names no browser session")
		return
	case !p.Has("card.read"):
		deny("access token is limited (password change pending)")
		return
	}
	_, live, err := svc.HumanSessionLive(r.Context(), body.AccountID, p.Sid)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !live {
		deny("browser session is not live")
		return
	}
	state, err := svc.AccountState(r.Context(), body.AccountID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if state.Disabled || state.MustChangePassword {
		deny("account disabled or password change pending")
		return
	}
	if !slices.Contains(identity.PlatformRoleCaps[state.Role], "session.start") {
		http.Error(w, "this account's role cannot start sessions", http.StatusForbidden)
		return
	}

	rec, raw, err := svc.CreateBoardAutomationToken(r.Context(), body.AccountID, body.BoardID, body.BoardName,
		identity.DefaultAgentTokenDays*24*time.Hour)
	switch {
	case errors.Is(err, identity.ErrAccountDisabled):
		deny("account disabled")
		return
	case errors.Is(err, identity.ErrTooManyTokens):
		// The literal 50 must track identity.maxLiveAgentTokens (as in handleCreateToken).
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": "too many active agent tokens (limit 50); revoke one in Settings → Agent tokens first",
		})
		return
	case err != nil:
		log.Printf("internal tokens board-automation: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("internal tokens: minted board automation token %s for account %s (board %s)", rec.ID, rec.AccountID, body.BoardID)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, boardAutomationMintResponse{
		ID: rec.ID, Token: raw, Name: rec.Name, ExpiresAt: rec.ExpiresAt.Format(time.RFC3339),
	})
}

// handleBoardAutomationRevoke is POST /internal/tokens/board-automation/revoke: the board revokes
// the token it minted when it is replaced or removed. Only a token of account_id whose source is
// "board <board_id>" can be revoked; anything else is the uniform 404. Idempotent.
func (d Deps) handleBoardAutomationRevoke(w http.ResponseWriter, r *http.Request) {
	var body boardAutomationRevokeRequest
	svc, ok := d.boardMintGate(w, r, &body)
	if !ok {
		return
	}
	switch {
	case !uuidShape.MatchString(body.AccountID):
		http.Error(w, "account_id must be a uuid", http.StatusBadRequest)
		return
	case !uuidShape.MatchString(body.TokenID):
		http.Error(w, "token_id must be a uuid", http.StatusBadRequest)
		return
	case !validBoardID(body.BoardID):
		http.Error(w, "board_id is required, at most 64 characters, no control characters", http.StatusBadRequest)
		return
	}
	switch err := svc.RevokeBoardAutomationToken(r.Context(), body.AccountID, body.TokenID, body.BoardID); {
	case errors.Is(err, identity.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		log.Printf("internal tokens board-automation revoke: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("internal tokens: revoked board automation token %s of account %s (board %s)", body.TokenID, body.AccountID, body.BoardID)
	w.WriteHeader(http.StatusOK)
}
