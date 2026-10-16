package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/blerglab/blerg-ai/core/internal/credentials"
)

// Managed endpoints never serialize vault state or provider diagnostics.
func managedCredentialError(w http.ResponseWriter, err error) {
	w.Header().Set("Cache-Control", "no-store")
	status, code := http.StatusInternalServerError, "unavailable"
	switch {
	case errors.Is(err, credentials.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, credentials.ErrNotManaged):
		status, code = http.StatusConflict, "not_managed"
	case errors.Is(err, credentials.ErrReconnectRequired):
		status, code = http.StatusConflict, "reconnect_required"
	case errors.Is(err, credentials.ErrRefreshPending):
		status, code = http.StatusAccepted, "refresh_pending"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}
func (d Deps) managedBrowser(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if d.Credentials == nil || d.Store == nil {
		http.Error(w, "credentials unavailable", http.StatusServiceUnavailable)
		return "", "", false
	}
	p, ok := principalFromCtx(r.Context())
	if !ok || p.Sid == "" {
		http.Error(w, "live browser session required", http.StatusUnauthorized)
		return "", "", false
	}
	_, _, live, err := d.principalLiveness(r.Context(), p.Sub, "", p.Sid)
	if err != nil || !live {
		http.Error(w, "live browser session required", http.StatusUnauthorized)
		return "", "", false
	}
	supported := false
	for _, capability := range d.Credentials.ManagedCapabilities() {
		if capability.Engine == r.PathValue("engine") && capability.Login {
			supported = true
			break
		}
	}
	if !supported {
		http.Error(w, "unsupported managed engine", http.StatusBadRequest)
		return "", "", false
	}
	return p.Sub, p.Sid, true
}
func (d Deps) handleManagedLoginStart(w http.ResponseWriter, r *http.Request) {
	account, sid, ok := d.managedBrowser(w, r)
	if !ok {
		return
	}
	status, err := d.Credentials.StartLogin(r.Context(), account, r.PathValue("engine"), sid)
	if err != nil {
		managedCredentialError(w, err)
		return
	}
	// Starting a device request discloses no tokens, but its code is still bound
	// to the initiating browser and must not be handed to a revoked session.
	_, _, live, err := d.principalLiveness(r.Context(), account, "", sid)
	if err != nil || !live {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, status)
}
func (d Deps) handleManagedLoginPoll(w http.ResponseWriter, r *http.Request) {
	account, sid, ok := d.managedBrowser(w, r)
	if !ok {
		return
	}
	attempt := r.PathValue("attempt")
	if !uuidShape.MatchString(attempt) {
		http.Error(w, "invalid attempt", http.StatusBadRequest)
		return
	}
	status, err := d.Credentials.PollLogin(r.Context(), account, r.PathValue("engine"), sid, attempt)
	if err != nil {
		managedCredentialError(w, err)
		return
	}
	_, _, live, err := d.principalLiveness(r.Context(), account, "", sid)
	if err != nil || !live {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, status)
}
func (d Deps) handleManagedLoginCancel(w http.ResponseWriter, r *http.Request) {
	account, sid, ok := d.managedBrowser(w, r)
	if !ok {
		return
	}
	attempt := r.PathValue("attempt")
	if !uuidShape.MatchString(attempt) {
		http.Error(w, "invalid attempt", http.StatusBadRequest)
		return
	}
	if err := d.Credentials.CancelLogin(r.Context(), account, r.PathValue("engine"), sid, attempt); err != nil {
		managedCredentialError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type managedAccessRequest struct {
	AccountID          string `json:"account_id"`
	Engine             string `json:"engine"`
	TokenID            string `json:"token_id,omitempty"`
	SessionID          string `json:"session_id,omitempty"`
	Force              bool   `json:"force,omitempty"`
	PreviousGeneration string `json:"previous_generation,omitempty"`
}

func (d Deps) handleInternalManagedAccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if d.InternalKey == "" {
		http.Error(w, "internal credentials disabled", http.StatusServiceUnavailable)
		return
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
		return
	}
	if d.Credentials == nil || d.Store == nil {
		http.Error(w, "credentials unavailable", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body managedAccessRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if msg := validateProofShape(body.AccountID, body.TokenID, body.SessionID); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if !validEngines[body.Engine] || (body.Force && !uuidShape.MatchString(body.PreviousGeneration)) || (body.PreviousGeneration != "" && !uuidShape.MatchString(body.PreviousGeneration)) {
		http.Error(w, "bad credential request", http.StatusBadRequest)
		return
	}
	_, _, live, err := d.principalLiveness(r.Context(), body.AccountID, body.TokenID, body.SessionID)
	if err != nil {
		managedCredentialError(w, err)
		return
	}
	if !live {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	access, err := d.Credentials.Obtain(r.Context(), body.AccountID, body.Engine, body.Force, body.PreviousGeneration)
	if err != nil {
		managedCredentialError(w, err)
		return
	}
	// Provider refresh is an external side effect. Authorization must still stand
	// after it, and audit failure must prevent disclosure of even cached access.
	sid, tid, live, err := d.principalLiveness(r.Context(), body.AccountID, body.TokenID, body.SessionID)
	if err != nil {
		managedCredentialError(w, err)
		return
	}
	if !live {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err = d.logManagedCredentialAccess(r.Context(), body.AccountID, body.Engine, access.Generation, sid, tid); err != nil {
		managedCredentialError(w, err)
		return
	}
	writeJSON(w, access)
}

func (d Deps) handleManagedCapabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if d.Credentials == nil {
		http.Error(w, "credentials unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, d.Credentials.ManagedCapabilities())
}

// The audit write is also the final generation/proof fence. Its snapshot must
// contain the still-current managed generation and exact still-live principal.
func (d Deps) logManagedCredentialAccess(ctx context.Context, account, engine, generation, sid, tid string) error {
	var session, token *string
	if sid != "" {
		session = &sid
	}
	if tid != "" {
		token = &tid
	}
	tag, err := d.Store.Pool().Exec(ctx, `INSERT INTO credential_access_log(account_id,engine,fetched_by_session_id,fetched_by_token_id,fetched_at)
 SELECT c.account_id,c.engine,$4::uuid,$5::uuid,now() FROM user_credentials c
 JOIN accounts a ON a.id=c.account_id
 WHERE c.account_id=$1 AND c.engine=$2 AND c.generation::text=$3 AND c.kind<>'opaque' AND c.state='usable'
 AND c.expires_at>now() AND a.disabled_at IS NULL
 AND (($4::uuid IS NOT NULL AND EXISTS(SELECT 1 FROM human_sessions h WHERE h.id=$4 AND h.account_id=c.account_id AND h.revoked_at IS NULL AND h.expires_at>now()))
 OR ($5::uuid IS NOT NULL AND EXISTS(SELECT 1 FROM agent_tokens t WHERE t.id=$5 AND t.account_id=c.account_id AND t.revoked_at IS NULL AND t.expires_at>now())))`, account, engine, generation, session, token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return credentials.ErrNotFound
	}
	return nil
}
