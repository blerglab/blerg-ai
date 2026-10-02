package api

import (
	"errors"
	"log"
	"net/http"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// This file implements POST /internal/tokens/exchange and /internal/tokens/exchange/revoke (spec
// 4.5, 10.2): the runner trades a liveness proof for a short-lived agent token for a built-in
// target, so a cron or session can use the board without the user storing a board token. Guarded
// like /internal/credentials/*: internal key, exactly one proof, uniform 404 for every failure past
// the shape checks.

const (
	maxExchangeBoardIDLen   = 64
	maxExchangeSessionRefLn = 128
)

// validExchangeString reports whether s is a non-empty string of at most limit runes with no
// control characters. core does not know boards, so this is the only check on a board id.
func validExchangeString(s string, limit int) bool {
	if s == "" || utf8.RuneCountInString(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// internalTokenExchangeRequest is POST /internal/tokens/exchange's body. SessionRef is the
// runner's own session id, recorded in the audit log line only.
type internalTokenExchangeRequest struct {
	internalMCPProof
	Target     string `json:"target"`
	BoardID    string `json:"board_id"`
	SessionRef string `json:"session_ref,omitempty"`
}

type internalTokenExchangeResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// handleInternalTokenExchange is POST /internal/tokens/exchange. The proof is a live human
// session_id, or a live agent token_id of kind "token" or "cron" (an exchange token is never a
// proof, so an exchanged token cannot be used to mint more). Only target "board" exists; an unknown
// target is a 400. The response carries the signed token, so it is never cached or logged.
func (d Deps) handleInternalTokenExchange(w http.ResponseWriter, r *http.Request) {
	var body internalTokenExchangeRequest
	svc, ok := d.internalTokenGate(w, r, &body)
	if !ok {
		return
	}
	if msg := validateProofShape(body.AccountID, body.TokenID, body.SessionID); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if _, known := identity.ExchangeTargets[body.Target]; !known {
		http.Error(w, "unsupported target", http.StatusBadRequest)
		return
	}
	if !validExchangeString(body.BoardID, maxExchangeBoardIDLen) {
		http.Error(w, "board_id is required, at most 64 characters, no control characters", http.StatusBadRequest)
		return
	}
	if body.SessionRef != "" && !validExchangeString(body.SessionRef, maxExchangeSessionRefLn) {
		http.Error(w, "session_ref is at most 128 characters, no control characters", http.StatusBadRequest)
		return
	}

	var proofCaps []string // nil for a human session: the account's role caps apply
	if body.TokenID != "" {
		kind, err := svc.AgentTokenKindOf(r.Context(), body.AccountID, body.TokenID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if kind != identity.AgentTokenKindToken && kind != identity.AgentTokenKindCron {
			log.Printf("internal tokens exchange: proof token is not an eligible kind for account %s", body.AccountID)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}
	_, _, live, err := d.principalLiveness(r.Context(), body.AccountID, body.TokenID, body.SessionID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !live {
		log.Printf("internal tokens exchange: no live principal for account %s", body.AccountID)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if body.TokenID != "" {
		// The issued caps are bounded by the proof token's own: a read-only token cannot mint write.
		caps, err := svc.AgentTokenCaps(r.Context(), body.AccountID, body.TokenID)
		if err != nil {
			if errors.Is(err, identity.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		proofCaps = caps
	}

	rec, raw, err := svc.CreateExchangeToken(r.Context(), body.AccountID, body.Target, body.BoardID, proofCaps)
	switch {
	case errors.Is(err, identity.ErrNoExchangeCaps):
		http.Error(w, "the proof token holds no capability for this target", http.StatusForbidden)
		return
	case errors.Is(err, identity.ErrAccountDisabled):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case errors.Is(err, identity.ErrTooManyTokens):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "too many live exchange tokens; revoke some first"})
		return
	case err != nil:
		log.Printf("internal tokens exchange: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Audit line: ids only, never the token.
	log.Printf("internal tokens: exchanged %s token %s for account %s (board %s, runner session %q)",
		body.Target, rec.ID, rec.AccountID, body.BoardID, body.SessionRef)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, internalTokenExchangeResponse{Token: raw, ExpiresAt: rec.ExpiresAt.Format(time.RFC3339)})
}

// internalTokenExchangeRevokeRequest is POST /internal/tokens/exchange/revoke's body.
type internalTokenExchangeRevokeRequest struct {
	AccountID string `json:"account_id"`
	TokenSub  string `json:"token_sub"`
}

// handleInternalTokenExchangeRevoke is POST /internal/tokens/exchange/revoke: the runner revokes
// an exchanged token at session end. Internal key plus account_id; only an exchange-kind token
// owned by that account can be revoked (another account's, an ordinary token and an unknown id are
// the uniform 404). Idempotent.
func (d Deps) handleInternalTokenExchangeRevoke(w http.ResponseWriter, r *http.Request) {
	var body internalTokenExchangeRevokeRequest
	svc, ok := d.internalTokenGate(w, r, &body)
	if !ok {
		return
	}
	if !uuidShape.MatchString(body.AccountID) {
		http.Error(w, "account_id must be a uuid", http.StatusBadRequest)
		return
	}
	if !uuidShape.MatchString(body.TokenSub) {
		http.Error(w, "token_sub must be a uuid", http.StatusBadRequest)
		return
	}
	switch err := svc.RevokeExchangeToken(r.Context(), body.AccountID, body.TokenSub); {
	case errors.Is(err, identity.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		log.Printf("internal tokens exchange revoke: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("internal tokens: revoked exchange token %s of account %s", body.TokenSub, body.AccountID)
	w.WriteHeader(http.StatusOK)
}
