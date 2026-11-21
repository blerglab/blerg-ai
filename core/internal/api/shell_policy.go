package api

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"time"

	wire "github.com/blerglab/blerg-ai/contracts/shellpolicy"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

func (d Deps) shellReady(w http.ResponseWriter) bool {
	w.Header().Set("Cache-Control", "no-store")
	if d.ShellPolicy == nil {
		http.Error(w, "shell policy unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}
func (d Deps) handleGetShellPolicy(w http.ResponseWriter, r *http.Request) {
	if !d.shellReady(w) {
		return
	}
	p, err := d.ShellPolicy.Read(r.Context())
	if err != nil {
		http.Error(w, "shell policy unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, p)
}
func (d Deps) handlePutShellPolicy(w http.ResponseWriter, r *http.Request) {
	if !d.shellReady(w) {
		return
	}
	if !d.sameOriginRequest(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	p, ok := principalFromCtx(r.Context())
	if !ok || p.Sid == "" {
		http.Error(w, "live sign-in required", http.StatusForbidden)
		return
	}
	_, _, live, err := d.principalLiveness(r.Context(), p.Sub, "", p.Sid)
	if err != nil {
		http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
		return
	}
	if !live {
		http.Error(w, "live sign-in required", http.StatusForbidden)
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || body.Enabled == nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	policy, err := d.ShellPolicy.SetForLogin(r.Context(), p.Sub, p.Sid, *body.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "administrator sign-in required", http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, "shell policy unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, policy)
}
func (d Deps) shellInternal(w http.ResponseWriter, r *http.Request) bool {
	if d.InternalKey == "" {
		http.Error(w, "shell control unavailable", http.StatusServiceUnavailable)
		return false
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return d.shellReady(w)
}

// Every operation and renewal can check the exact human login here, without credential access.
func (d Deps) handleAuthorizeShell(w http.ResponseWriter, r *http.Request) {
	if !d.shellInternal(w, r) {
		return
	}
	var body wire.AuthorizationRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || body.Owner == "" || body.Login == "" || len(body.Nonce) > 128 || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var live bool
	var err error
	var deadline time.Time
	if body.Nonce != "" {
		svc, ok := d.identitySvc()
		if !ok {
			http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
			return
		}
		_, deadline, live, err = svc.HumanSessionDeadline(r.Context(), body.Owner, body.Login)
	} else {
		_, _, live, err = d.principalLiveness(r.Context(), body.Owner, "", body.Login)
	}
	if err != nil {
		http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
		return
	}
	if !live {
		if body.Nonce == "" {
			http.Error(w, "sign-in unavailable", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(wire.AuthorizationDenial{Version: 1, Outcome: "deny", Owner: body.Owner, Login: body.Login, Nonce: body.Nonce, Reason: "login_unavailable"})
		return
	}
	if body.Nonce == "" {
		d.handleGetShellPolicy(w, r)
		return
	}
	policy, readErr := d.ShellPolicy.Read(r.Context())
	if readErr != nil {
		http.Error(w, "shell policy unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, wire.AuthorizationAllowed{ShellPolicy: policy, AuthorizationVersion: 2, Outcome: "allow", Owner: body.Owner, Login: body.Login, Nonce: body.Nonce, LoginExpiresAt: deadline.Unix()})
}

// The authenticated stream contains policy and identity IDs only. Dropped/stalled streams
// must revoke runner handles; they are never permission to keep cached enabled state.
func (d Deps) handleShellControl(w http.ResponseWriter, r *http.Request) {
	if !d.shellInternal(w, r) {
		return
	}
	if d.Identity == nil {
		http.Error(w, "identity unavailable", http.StatusServiceUnavailable)
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	events, stop, err := d.ShellPolicy.Listen(r.Context())
	if err != nil {
		http.Error(w, "shell control unavailable", http.StatusServiceUnavailable)
		return
	}
	defer stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		changed := d.ShellPolicy.Changed()
		p, err := d.ShellPolicy.Read(r.Context())
		if err != nil {
			return
		}
		rev, err := d.Identity.RevocationSnapshot(r.Context())
		if err != nil {
			return
		}
		// Bound writes even if the component stops reading. No request bodies or shell bytes.
		if http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3*time.Second)) != nil {
			return
		}
		if json.NewEncoder(w).Encode(struct {
			Policy      wire.ShellPolicy      `json:"policy"`
			Revocations []identity.Revocation `json:"revocations"`
		}{p, rev}) != nil {
			return
		}
		f.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-ticker.C:
		}
	}
}
