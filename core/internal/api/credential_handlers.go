package api

import (
	"encoding/json"
	"errors"
	"net/http"
)

// validEngines is the closed set the runner actually knows how to inject (see
// runner/internal/server/k8sjobs.go engineCredentialKey). Free-text engine names were a
// storage sink (audit M-3): anyone with a human token could POST an arbitrary "engine" string
// and have it accepted and encrypted forever, even though nothing downstream would ever read it.
//
// The git-provider kinds are not agent engines but credential KINDS stored in the same per-user
// vault: the personal token a cluster session pod clones and pushes with (so work lands as the
// launching human rather than as a shared service identity), and that the runner lists the
// person's own repositories with. Each is the id of a provider registered in the runner's
// gitprovider.Default — adding a provider there means adding its id here.
var gitCredentialKinds = []string{"github", "gitlab"}

var validEngines = func() map[string]bool {
	m := map[string]bool{"claude": true, "codex": true, "hermes": true, "openclaw": true}
	for _, k := range gitCredentialKinds {
		m[k] = true
	}
	return m
}()

// storeCredentialRequest is POST /api/credentials's body. There is deliberately no account-id
// field here (and handleStoreCredential never reads one from the request) — the account acted
// on is always the caller's own, taken from the verified access token's Sub claim by
// requireHumanPrincipal/principalFromCtx, never from anything client-supplied (§5's critical
// security requirement).
type storeCredentialRequest struct {
	Engine     string `json:"engine"`
	Credential string `json:"credential"`
}

// handleStoreCredential encrypts and upserts the caller's own credential for one engine.
// Gated behind requireHumanPrincipal in router.go.
func (d Deps) handleStoreCredential(w http.ResponseWriter, r *http.Request) {
	if d.Credentials == nil {
		http.Error(w, "credentials service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	// Cap the body BEFORE decoding (audit M-4): an unbounded POST body would otherwise let any
	// authenticated caller force the server to buffer an arbitrarily large "credential" value.
	// 64 KiB is generous for any real API token/credential while still bounding the worst case.
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body storeCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if body.Engine == "" || body.Credential == "" {
		http.Error(w, "engine and credential are required", http.StatusBadRequest)
		return
	}
	if !validEngines[body.Engine] {
		http.Error(w, "unknown engine", http.StatusBadRequest)
		return
	}
	if err := d.Credentials.Store(r.Context(), principal.Sub, body.Engine, []byte(body.Credential)); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// credentialSummaryResponse is what GET /api/credentials returns per credential — engine and
// last-updated timestamp only. Never ciphertext, never plaintext (§5).
type credentialSummaryResponse struct {
	Engine    string `json:"engine"`
	UpdatedAt string `json:"updated_at"`
}

// handleListCredentials lists the caller's own credentials (metadata only). Gated behind
// requireHumanPrincipal in router.go.
func (d Deps) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if d.Credentials == nil {
		http.Error(w, "credentials service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	list, err := d.Credentials.List(r.Context(), principal.Sub)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]credentialSummaryResponse, 0, len(list))
	for _, c := range list {
		out = append(out, credentialSummaryResponse{Engine: c.Engine, UpdatedAt: c.UpdatedAt.Format(rfc3339Milli)})
	}
	writeJSON(w, out)
}

// rfc3339Milli is the timestamp format used for credentialSummaryResponse.UpdatedAt.
const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

// handleDeleteCredential removes the caller's own credential for {engine}. Gated behind
// requireHumanPrincipal in router.go. Deleting a nonexistent engine is a no-op 200, matching
// credentials.Service.Delete's idempotent-delete convention.
func (d Deps) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	if d.Credentials == nil {
		http.Error(w, "credentials service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	engine := r.PathValue("engine")
	if engine == "" {
		http.Error(w, "engine is required", http.StatusBadRequest)
		return
	}
	if !validEngines[engine] {
		http.Error(w, "unknown engine", http.StatusBadRequest)
		return
	}
	if err := d.Credentials.Delete(r.Context(), principal.Sub, engine); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
