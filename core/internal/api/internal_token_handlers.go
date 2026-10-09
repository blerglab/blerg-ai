package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// This file implements the internal cron-token routes (spec 4.5 tokens/*, 7.4): the runner
// creates, checks and revokes the identity of a scheduled run. They are guarded like
// /internal/credentials/*: the internal key (constant-time compare, 503 when unconfigured),
// then a proof, and any failure past the shape checks is the uniform 404.
//
// SECURITY: no signed token is ever produced. A cron "token" is an agent_tokens row of kind
// "cron" whose id is the only thing the runner holds; nothing here returns a credential.

// internalTokenGate runs the checks shared by the three routes: internal key, identity service,
// capped body decode. It writes the response itself and returns nil,false on failure.
func (d Deps) internalTokenGate(w http.ResponseWriter, r *http.Request, body any) (*identity.Service, bool) {
	if d.InternalKey == "" {
		http.Error(w, "internal tokens disabled", http.StatusServiceUnavailable)
		return nil, false
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
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

// internalTokenMintRequest is POST /internal/tokens/mint's body. TokenID exists only so a
// token_id proof can be recognised and refused: a cron may only be created by a live human login.
type internalTokenMintRequest struct {
	AccountID     string `json:"account_id"`
	SessionID     string `json:"session_id"`
	TokenID       string `json:"token_id,omitempty"`
	Name          string `json:"name"`
	ExpiresInDays int    `json:"expires_in_days"`
}

// internalTokenMintResponse carries NO credential: only the row's id and expiry.
type internalTokenMintResponse struct {
	ID        string `json:"id"`
	ExpiresAt string `json:"expires_at"`
}

// handleInternalTokenMint is POST /internal/tokens/mint. The proof MUST be a live human
// session_id of account_id (a token_id proof, or both, is a 400): an unattended identity is only
// ever created on the say-so of a person who is logged in right now.
func (d Deps) handleInternalTokenMint(w http.ResponseWriter, r *http.Request) {
	var body internalTokenMintRequest
	svc, ok := d.internalTokenGate(w, r, &body)
	if !ok {
		return
	}
	if !uuidShape.MatchString(body.AccountID) {
		http.Error(w, "account_id must be a uuid", http.StatusBadRequest)
		return
	}
	if body.TokenID != "" || body.SessionID == "" || !uuidShape.MatchString(body.SessionID) {
		http.Error(w, "a live human session_id is required as proof (token_id is not accepted)", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || utf8.RuneCountInString(name) > maxTokenNameLen {
		http.Error(w, "name is required and at most 64 characters", http.StatusBadRequest)
		return
	}
	days := body.ExpiresInDays
	if days == 0 {
		days = identity.DefaultAgentTokenDays
	}
	if days < 1 || days > identity.MaxAgentTokenDays {
		http.Error(w, "expires_in_days must be between 1 and 365", http.StatusBadRequest)
		return
	}

	if _, live, err := svc.HumanSessionLive(r.Context(), body.AccountID, body.SessionID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !live {
		log.Printf("internal tokens mint: no live session for account %s", body.AccountID)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	rec, err := svc.CreateUnsignedAgentToken(r.Context(), body.AccountID, name, time.Duration(days)*24*time.Hour)
	switch {
	case errors.Is(err, identity.ErrAccountDisabled):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case errors.Is(err, identity.ErrTooManyTokens):
		// The literal 50 must track identity.maxLiveAgentTokens (as in handleCreateToken).
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": "too many active agent tokens (limit 50); revoke one first",
		})
		return
	case err != nil:
		log.Printf("internal tokens mint: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Audit line: ids only, nothing secret (there is nothing secret to leak).
	log.Printf("internal tokens: minted cron token %s for account %s", rec.ID, rec.AccountID)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, internalTokenMintResponse{ID: rec.ID, ExpiresAt: rec.ExpiresAt.Format(time.RFC3339)})
}

// internalTokenRevokeRequest is POST /internal/tokens/revoke's body.
type internalTokenRevokeRequest struct {
	AccountID string `json:"account_id"`
	TokenID   string `json:"token_id"`
	SessionID string `json:"session_id,omitempty"`
}

// handleInternalTokenRevoke is POST /internal/tokens/revoke. Two paths, both bound to account_id:
//
//   - with session_id: a live human session of that account, which may revoke any of that
//     account's tokens. A session that is not live is a 404; it never falls back to the key path.
//   - without: the internal key alone, used when the scheduler pauses a cron on the account's
//     behalf. It can revoke ONLY a cron-kind token owned by account_id, so the key alone can
//     never revoke an ordinary token or another account's.
//
// Idempotent. Every non-match is the uniform 404.
func (d Deps) handleInternalTokenRevoke(w http.ResponseWriter, r *http.Request) {
	var body internalTokenRevokeRequest
	svc, ok := d.internalTokenGate(w, r, &body)
	if !ok {
		return
	}
	if !uuidShape.MatchString(body.AccountID) {
		http.Error(w, "account_id must be a uuid", http.StatusBadRequest)
		return
	}
	if !uuidShape.MatchString(body.TokenID) {
		http.Error(w, "token_id must be a uuid", http.StatusBadRequest)
		return
	}
	if body.SessionID != "" && !uuidShape.MatchString(body.SessionID) {
		http.Error(w, "session_id must be a uuid", http.StatusBadRequest)
		return
	}

	var err error
	if body.SessionID != "" {
		var live bool
		if _, live, err = svc.HumanSessionLive(r.Context(), body.AccountID, body.SessionID); err == nil && !live {
			log.Printf("internal tokens revoke: no live session for account %s", body.AccountID)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err == nil {
			err = svc.RevokeAgentToken(r.Context(), body.AccountID, body.TokenID)
		}
	} else {
		err = svc.RevokeCronToken(r.Context(), body.AccountID, body.TokenID)
	}
	switch {
	case errors.Is(err, identity.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		log.Printf("internal tokens revoke: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("internal tokens: revoked token %s of account %s (session proof: %t)", body.TokenID, body.AccountID, body.SessionID != "")
	w.WriteHeader(http.StatusOK)
}

// internalTokenStatusRequest is POST /internal/tokens/status's body.
type internalTokenStatusRequest struct {
	AccountID string `json:"account_id"`
	TokenID   string `json:"token_id"`
}

type internalTokenStatusResponse struct {
	Live      bool   `json:"live"`
	AccountID string `json:"account_id"`
	// Name is the owner's label for their own token (the runner shows it as who started a
	// session); absent for a token that is not the account's.
	Name string `json:"name,omitempty"`
}

// handleInternalTokenStatus is POST /internal/tokens/status: whether token_id is a live token of
// account_id (present, unrevoked, unexpired, account enabled). A token that is unknown or another
// account's, and an owned token that is dead, are the same 200 {"live": false}.
func (d Deps) handleInternalTokenStatus(w http.ResponseWriter, r *http.Request) {
	var body internalTokenStatusRequest
	svc, ok := d.internalTokenGate(w, r, &body)
	if !ok {
		return
	}
	if !uuidShape.MatchString(body.AccountID) {
		http.Error(w, "account_id must be a uuid", http.StatusBadRequest)
		return
	}
	if !uuidShape.MatchString(body.TokenID) {
		http.Error(w, "token_id must be a uuid", http.StatusBadRequest)
		return
	}
	live, owned, err := svc.AgentTokenStatus(r.Context(), body.AccountID, body.TokenID)
	if err != nil {
		log.Printf("internal tokens status: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A token that is unknown or another account's answers exactly like a dead one of the
	// caller's own (200 {"live": false}): it discloses nothing, and a 404 is left to mean "this
	// is not core" (a proxy, a rolled-back deploy), which the runner must never read as "revoked".
	if !owned {
		live = false
	}
	w.Header().Set("Cache-Control", "no-store")
	name := ""
	if owned {
		if n, err := svc.AgentTokenName(r.Context(), body.AccountID, body.TokenID); err == nil {
			name = n
		}
	}
	writeJSON(w, internalTokenStatusResponse{Live: live, AccountID: body.AccountID, Name: name})
}
