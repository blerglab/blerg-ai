package api

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/core/internal/credentials"
)

// uuidShape matches a standard 8-4-4-4-12 hex UUID. account_id must look like one before it is
// ever used in a query — a malformed value (audit M-5) should 400 immediately rather than fall
// through to liveSessionID and get treated the same as "well-formed but unknown", which is a
// wasted round trip at best and a probing surface at worst.
var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// This file implements POST /internal/credentials/fetch — the ONE place in the whole system a
// plaintext personal API credential (e.g. a Claude/Codex token) is ever reconstituted. It is a
// component-to-component call (blerg-runner -> blerg-core), never a browser-facing endpoint:
//
//   - It is never registered under /api/... (the public API surface) and lives under
//     /internal/... specifically so it's obviously not part of that surface.
//   - NewRouter never adds CORS headers anywhere (there is no CORS middleware in this package at
//     all), so this endpoint sends none — a browser calling it cross-origin gets no
//     Access-Control-Allow-Origin and its fetch() is blocked by the browser itself. Do not add
//     CORS support to this handler or to this file.
//   - Its auth is a static shared secret (BLERG_CORE_INTERNAL_KEY), not a bearer/session token a
//     browser could ever hold — see handleInternalFetchCredential below for why this is a
//     deliberately SEPARATE secret from BLERG_CORE_REGISTER_KEY.

// internalFetchRequest is POST /internal/credentials/fetch's body.
type internalFetchRequest struct {
	AccountID string `json:"account_id"`
	Engine    string `json:"engine"`
	// Exactly one of TokenID / SessionID names WHICH live thing authorises this fetch: the agent
	// token the calling session runs as (spec §2), or the human browser session (the `sid` claim
	// of the access token the launch request carried) that started it. Core verifies that
	// specific token/session belongs to AccountID and is live; see Deps.principalLiveness.
	TokenID   string `json:"token_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// internalFetchResponse is POST /internal/credentials/fetch's success body: the plaintext
// credential, base64-encoded since it may be arbitrary binary-ish token material.
type internalFetchResponse struct {
	PlaintextBase64 string `json:"plaintext_base64"`
}

// handleInternalFetchCredential is POST /internal/credentials/fetch. Two independent checks
// must BOTH pass before any plaintext is returned:
//
//  1. validInternalKey: the caller presents the correct BLERG_CORE_INTERNAL_KEY static secret
//     (constant-time compared, same header conventions as validRegisterKey/POST /components).
//     This is a DISTINCT secret from BLERG_CORE_REGISTER_KEY — registering a component into the
//     discovery directory and fetching a logged-in human's decrypted credential are wildly
//     different blast radii, and must not share a key. Fails closed (503) if unconfigured, 401
//     if configured but the caller's key doesn't match.
//
//  2. A named live principal: exactly one of token_id (an agent token) or session_id (a human
//     browser session) that core verifies belongs to account_id and is live. This bounds the
//     blast radius of a compromised/leaked internal key to the accounts whose specific
//     session/token the caller can name — knowing an account's uuid is not enough. It is NOT
//     redundant with check 1. Neither/both is a 400; a proof that is not live, or not this
//     account's, is the same 404 "not found" as a missing credential.
//
// Only once both checks pass does this call credentials.Service.Fetch and write the
// credential_access_log audit row — one row per successful fetch, never on a rejected request.
func (d Deps) handleInternalFetchCredential(w http.ResponseWriter, r *http.Request) {
	if d.InternalKey == "" {
		http.Error(w, "internal credential fetch disabled", http.StatusServiceUnavailable)
		return
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
		return
	}
	if d.Credentials == nil || d.Store == nil {
		http.Error(w, "credentials service unavailable", http.StatusServiceUnavailable)
		return
	}

	// Cap the body BEFORE decoding (audit M-4) — 16 KiB is more than enough for
	// {account_id, engine}, a fixed-shape request with no free-text field of any real size.
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body internalFetchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if body.AccountID == "" || body.Engine == "" {
		http.Error(w, "account_id and engine are required", http.StatusBadRequest)
		return
	}
	if msg := validateProofShape(body.AccountID, body.TokenID, body.SessionID); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	// Check 2: this account's credentials are still backed by a live principal — a live human
	// session, or the agent token the calling session runs as. This is a SEPARATE, additional
	// gate on top of check 1 above — a valid internal key alone is never enough.
	sessionID, tokenID, ok, err := d.principalLiveness(r.Context(), body.AccountID, body.TokenID, body.SessionID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		// Deliberately the SAME status and body as a missing credential below (audit M-8): a
		// caller holding a valid internal key must not be able to distinguish "this account is
		// logged out" from "this account never stored a credential for this engine" — either
		// would otherwise leak account-enumeration/state information to whoever holds the key.
		log.Printf("internal fetch: no live principal for account %s", body.AccountID)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	plaintext, err := d.Credentials.Fetch(r.Context(), body.AccountID, body.Engine)
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := d.logCredentialAccess(r.Context(), body.AccountID, body.Engine, sessionID, tokenID); err != nil {
		// The audit log write failing is treated as a hard failure — never hand back
		// plaintext without a corresponding access-log row. The caller (runner) must
		// retry; nothing has leaked.
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, internalFetchResponse{
		PlaintextBase64: base64.StdEncoding.EncodeToString(plaintext),
	})
}

// internalListRequest is POST /internal/credentials/list's body.
type internalListRequest struct {
	AccountID string `json:"account_id"`
	// TokenID / SessionID are exactly as on internalFetchRequest: exactly one, naming the live
	// agent token or human session that authorises the call.
	TokenID   string `json:"token_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// internalListResponse is POST /internal/credentials/list's success body: the sorted names of
// the credential kinds this account has stored. Names only — never a value, never a timestamp.
type internalListResponse struct {
	Engines []string `json:"engines"`
}

// handleInternalListCredentials is POST /internal/credentials/list. The runner calls it to learn
// which credential kinds a user has stored, so it can shape a cluster session pod for that user
// without first attempting a fetch per kind.
//
// It is gated exactly like handleInternalFetchCredential — the same internal key, the same
// named-live-principal requirement (token_id or session_id), and the same 404 "not found" for an account with no live
// session so a key holder still cannot distinguish logged-out from never-stored. It deliberately
// writes NO credential_access_log row: no plaintext is decrypted or returned here, so there is
// no credential access to log, and logging metadata reads would dilute that table's meaning.
func (d Deps) handleInternalListCredentials(w http.ResponseWriter, r *http.Request) {
	if d.InternalKey == "" {
		// This endpoint lists kinds; it fetches nothing. Saying "fetch" here
		// sends an operator looking at the wrong route.
		http.Error(w, "internal credentials disabled", http.StatusServiceUnavailable)
		return
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
		return
	}
	if d.Credentials == nil || d.Store == nil {
		http.Error(w, "credentials service unavailable", http.StatusServiceUnavailable)
		return
	}

	// Same 16 KiB cap as fetch — the body is a single fixed-shape uuid field.
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body internalListRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if body.AccountID == "" {
		http.Error(w, "account_id is required", http.StatusBadRequest)
		return
	}
	if msg := validateProofShape(body.AccountID, body.TokenID, body.SessionID); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	if _, _, ok, err := d.principalLiveness(r.Context(), body.AccountID, body.TokenID, body.SessionID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !ok {
		log.Printf("internal list: no live principal for account %s", body.AccountID)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	list, err := d.Credentials.List(r.Context(), body.AccountID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Service.List already orders by engine; the empty case must marshal as [] rather than null.
	engines := make([]string, 0, len(list))
	for _, c := range list {
		engines = append(engines, c.Engine)
	}
	writeJSON(w, internalListResponse{Engines: engines})
}

// validateProofShape is the structural half of check 2, shared by every internal endpoint that
// acts for an account: a uuid account_id, and EXACTLY ONE well-formed liveness proof. It returns
// a 400 message, or "" when the request is well formed. Structural mistakes are 400s (audit M-5),
// which is not the same thing as naming a proof that is not live (that is the uniform 404).
func validateProofShape(accountID, tokenID, sessionID string) string {
	if !uuidShape.MatchString(accountID) {
		return "account_id must be a uuid"
	}
	if (tokenID != "") == (sessionID != "") { // both set, or neither
		return "exactly one of token_id or session_id is required"
	}
	if tokenID != "" && !uuidShape.MatchString(tokenID) {
		return "token_id must be a uuid"
	}
	if sessionID != "" && !uuidShape.MatchString(sessionID) {
		return "session_id must be a uuid"
	}
	return ""
}

// principalLiveness is check 2 of the internal endpoints — "the specific thing that authorised
// this launch still stands, and it is this account's" — resolved against exactly the proof the
// caller named (validateProofShape has already guaranteed exactly one).
//
//   - token_id: that agent token must be one of this account's, unrevoked and unexpired
//     (identity.AgentTokenLive, which also stamps last_used_at). A session started by an agent
//     token has no browser behind it.
//   - session_id: the human browser session (the `sid` claim of the access token the launch
//     request carried) must belong to this account and still be live
//     (identity.HumanSessionLive). Knowing an account's uuid and that it is logged in is not
//     enough: the caller must name that account's own session.
//
// There is deliberately no "any live session of the account" fallback: the limit on what a
// holder of the internal key can read is enforced here, not left to its callers.
//
// ok=false is an ordinary outcome (dead, revoked, expired, or someone else's proof), not an
// error — the callers turn every such case into the same anti-enumeration 404, so a key holder
// cannot tell "no such session" from "not this account's" from "logged out".
func (d Deps) principalLiveness(ctx context.Context, accountID, tokenID, sessionID string) (forSessionID, forTokenID string, ok bool, err error) {
	svc, svcOK := d.identitySvc()
	if !svcOK {
		return "", "", false, errors.New("identity service unavailable")
	}
	if sessionID != "" {
		id, live, err := svc.HumanSessionLive(ctx, accountID, sessionID)
		if err != nil || !live {
			return "", "", false, err
		}
		return id, "", true, nil
	}
	live, err := svc.AgentTokenLive(ctx, accountID, tokenID)
	if err != nil || !live {
		return "", "", false, err
	}
	return "", tokenID, true, nil
}

// logCredentialAccess inserts one credential_access_log row for a successful fetch. Exactly one
// of sessionID/tokenID is set — which principal performed the fetch — and the other column is
// left NULL rather than reusing one column for two kinds of id an auditor could not tell apart.
func (d Deps) logCredentialAccess(ctx context.Context, accountID, engine, sessionID, tokenID string) error {
	nullable := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	_, err := d.Store.Pool().Exec(ctx,
		`INSERT INTO credential_access_log (account_id, engine, fetched_by_session_id, fetched_by_token_id, fetched_at)
		 VALUES ($1, $2, $3, $4, now())`,
		accountID, engine, nullable(sessionID), nullable(tokenID))
	return err
}

// validInternalKey constant-time-compares the caller-supplied internal key against the
// configured BLERG_CORE_INTERNAL_KEY secret. Mirrors validRegisterKey's exact shape (accepts
// either "Authorization: Bearer <key>" or an "X-..."-style header — here "X-Internal-Key",
// to avoid any confusion with the distinct registration secret's "X-Register-Key") for
// consistency of style, but is a wholly separate check against a wholly separate secret.
func validInternalKey(r *http.Request, want string) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" || got == r.Header.Get("Authorization") {
		got = r.Header.Get("X-Internal-Key")
	}
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
