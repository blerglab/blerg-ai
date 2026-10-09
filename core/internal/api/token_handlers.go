package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// This file implements the agent-token endpoints (spec §2): the credentials a user mints in a
// browser so an external automated tool can talk to this install without database access and
// without a shared static key.
//
// Three properties hold across all three handlers:
//
//   - The account acted on is ALWAYS the caller's own, taken from the verified access token's
//     Sub claim (principalFromCtx). There is deliberately no account field in any request body
//     or path, exactly as in credential_handlers.go.
//   - The token VALUE leaves core exactly once, in POST's 201 body. It is never logged, never
//     listed, and unrecoverable afterwards — only a hash is stored.
//   - All three are gated on a HUMAN principal (router.go). An agent token must not be able to
//     mint another agent token: a revocation that killed the parent would not reach the child,
//     making revocation unenforceable.

// maxTokenNameLen bounds the user-supplied label. 64 characters is generous for "laptop cli"
// or "ci pipeline" while keeping the value renderable in a Settings list without truncation
// games, and is checked in RUNES, not bytes, so a non-ASCII name isn't silently shorter.
const maxTokenNameLen = 64

// createTokenRequest is POST /api/tokens's body. No account field, by design (see above).
type createTokenRequest struct {
	Name          string `json:"name"`
	Preset        string `json:"preset"`
	ExpiresInDays int    `json:"expires_in_days"`
}

// createdTokenResponse is POST /api/tokens's 201 body — the ONLY place Token is ever populated.
type createdTokenResponse struct {
	ID        string   `json:"id"`
	Token     string   `json:"token"`
	Name      string   `json:"name"`
	Aud       string   `json:"aud"`
	Caps      []string `json:"caps"`
	ExpiresAt string   `json:"expires_at"`
	CreatedAt string   `json:"created_at"`
}

// tokenSummaryResponse is one entry of GET /api/tokens: metadata only. It has no field for the
// token or its hash at all, so no future edit can leak one by forgetting to strip it.
type tokenSummaryResponse struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Aud        string   `json:"aud"`
	Caps       []string `json:"caps"`
	CreatedAt  string   `json:"created_at"`
	ExpiresAt  string   `json:"expires_at"`
	LastUsedAt *string  `json:"last_used_at"`
	RevokedAt  *string  `json:"revoked_at"`
}

// handleCreateToken is POST /api/tokens. Gated behind requireHumanPrincipal("card.read") in
// router.go — the capability gate is what stops a must-change-password bootstrap token (whose
// caps are only ["password.change"]) from minting a year-long credential before its owner has
// even chosen a password.
//
// Same-origin guarded like /auth/login: this mints a long-lived credential off the caller's
// browser session, which is precisely what a CSRF POST would want. Rate-limited by the shared
// LoginLimiter keyed on the authenticated account, so a stolen access token does not buy an
// unbounded run at the endpoint.
func (d Deps) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}

	ip := d.clientIP(r)
	if allowed, retryAfter := d.LoginLimiter.Allow(ip, principal.Sub); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		http.Error(w, "too many attempts — try again later", http.StatusTooManyRequests)
		return
	}

	// Cap the body BEFORE decoding (audit M-4) — 16 KiB is generous for {name, preset, days},
	// none of which is a free-text field of any real size.
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}

	// Only REJECTED attempts count against the limiter. Counting successes too would drain the
	// bucket that POST /auth/login shares by IP, so creating a handful of tokens would lock the
	// account out of signing in — a self-inflicted denial of service for entirely legitimate
	// use. Garbage attempts, which is what an abuser produces, still fill it.
	fail := func(msg string) {
		d.LoginLimiter.Fail(ip, principal.Sub)
		http.Error(w, msg, http.StatusBadRequest)
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		fail("name is required")
		return
	}
	if utf8.RuneCountInString(name) > maxTokenNameLen {
		fail("name must be at most 64 characters")
		return
	}
	if _, known := identity.AgentTokenPresets[body.Preset]; !known {
		// The valid presets are public (GET /agents documents them), so naming them here
		// is not an oracle — it is the difference between a usable error and a guessing game.
		fail("preset must be one of: " + strings.Join(identity.AgentTokenPresetNames(), ", "))
		return
	}
	days := body.ExpiresInDays
	if days == 0 {
		days = identity.DefaultAgentTokenDays
	}
	if days < 1 || days > identity.MaxAgentTokenDays {
		fail("expires_in_days must be between 1 and 365")
		return
	}

	rec, raw, err := svc.CreateAgentToken(r.Context(), principal.Sub, name, body.Preset,
		time.Duration(days)*24*time.Hour)
	switch {
	case errors.Is(err, identity.ErrAccountDisabled):
		http.Error(w, "account disabled", http.StatusForbidden)
		return
	case errors.Is(err, identity.ErrTooManyTokens):
		// JSON, not the plain-text http.Error every other branch uses: this is the one
		// rejection the Settings UI has to render as a specific, actionable message ("revoke
		// one first") rather than a generic failure toast.
		// The literal 50 must track identity.maxLiveAgentTokens.
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": "too many active agent tokens (limit 50); revoke one first",
		})
		return
	case errors.Is(err, identity.ErrUnknownPreset):
		// Unreachable (the preset was checked above); kept so a future refactor that moves the
		// check cannot turn a closed-list violation into a 500.
		fail("unknown preset")
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSONStatus(w, http.StatusCreated, createdTokenResponse{
		ID:        rec.ID,
		Token:     raw,
		Name:      rec.Name,
		Aud:       rec.Aud,
		Caps:      rec.Caps,
		ExpiresAt: rec.ExpiresAt.Format(time.RFC3339),
		CreatedAt: rec.CreatedAt.Format(time.RFC3339),
	})
}

// handleRemintToken is POST /api/tokens/{id}/remint: revoke the named token and mint a fresh
// one of the same name, audience, capabilities and lifetime, answering exactly as POST
// /api/tokens does (the new value, shown once). Guarded like a mint: same origin, the login
// limiter, a human principal with card.read.
func (d Deps) handleRemintToken(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	ip := d.clientIP(r)
	if allowed, retryAfter := d.LoginLimiter.Allow(ip, principal.Sub); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		http.Error(w, "too many attempts — try again later", http.StatusTooManyRequests)
		return
	}
	rec, raw, err := svc.RemintAgentToken(r.Context(), principal.Sub, r.PathValue("id"))
	switch {
	case errors.Is(err, identity.ErrNotFound):
		d.LoginLimiter.Fail(ip, principal.Sub)
		http.Error(w, "not found", http.StatusNotFound)
		return
	case errors.Is(err, identity.ErrAccountDisabled):
		http.Error(w, "account disabled", http.StatusForbidden)
		return
	case errors.Is(err, identity.ErrTooManyTokens):
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": "too many active agent tokens (limit 50); revoke one first",
		})
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSONStatus(w, http.StatusCreated, createdTokenResponse{
		ID:        rec.ID,
		Token:     raw,
		Name:      rec.Name,
		Aud:       rec.Aud,
		Caps:      rec.Caps,
		ExpiresAt: rec.ExpiresAt.Format(time.RFC3339),
		CreatedAt: rec.CreatedAt.Format(time.RFC3339),
	})
}

// handleListTokens is GET /api/tokens: the caller's own tokens, metadata only. Read-only, so
// no same-origin guard (a cross-site read cannot see the response — core sends no CORS headers
// anywhere) and no rate limit.
func (d Deps) handleListTokens(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	list, err := svc.ListAgentTokens(r.Context(), principal.Sub)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]tokenSummaryResponse, 0, len(list))
	for _, rec := range list {
		out = append(out, tokenSummaryResponse{
			ID:         rec.ID,
			Name:       rec.Name,
			Aud:        rec.Aud,
			Caps:       rec.Caps,
			CreatedAt:  rec.CreatedAt.Format(time.RFC3339),
			ExpiresAt:  rec.ExpiresAt.Format(time.RFC3339),
			LastUsedAt: formatOptionalTime(rec.LastUsedAt),
			RevokedAt:  formatOptionalTime(rec.RevokedAt),
		})
	}
	writeJSON(w, out)
}

// handleRevokeToken is DELETE /api/tokens/{id}: idempotent, own tokens only. Same-origin
// guarded for the same reason POST is — revoking somebody's automation off a drive-by page is
// a denial of service, not merely a read.
//
// Another account's token id is 404, identical to an id that does not exist, so this endpoint
// is never a token-id oracle (identity.ErrNotFound's own doc comment).
func (d Deps) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if !d.requireSameOrigin(w, r) {
		return
	}
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	switch err := svc.RevokeAgentToken(r.Context(), principal.Sub, r.PathValue("id")); {
	case errors.Is(err, identity.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// formatOptionalTime renders a nullable timestamp as RFC3339 or JSON null.
func formatOptionalTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

// writeJSONStatus is writeJSON with an explicit status code, for the one handler that answers
// 201 rather than 200. The Content-Type must be set BEFORE WriteHeader — afterwards it is
// silently ignored — which is why this exists instead of a WriteHeader call at the call site.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
