package api

// Install-wide session attachment storage: the per-session default and the most an administrator
// may raise one session to. An administrator with a live sign-in updates them (each save is
// audited); the runner reads them with its internal key.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/sessionstorage"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/jackc/pgx/v5"
)

type sessionStorageView struct {
	sessionstorage.Limits
	Defaults  sessionstorage.Limits `json:"defaults"`
	Ranges    map[string][2]int64   `json:"ranges"`
	StepMB    int64                 `json:"step_mb"`
	Custom    bool                  `json:"custom"`
	UpdatedAt *time.Time            `json:"updated_at"`
}

// sessionStorageInternal is what the runner reads: Set false means nothing was saved, and the
// runner keeps its own environment value (BLERG_RUNNER_MAX_SESSION_UPLOADS_MB).
type sessionStorageInternal struct {
	sessionstorage.Limits
	Set bool `json:"set"`
}

func (d Deps) writeSessionStorage(w http.ResponseWriter, r *http.Request, svc *identity.Service) {
	st, err := svc.SessionStorage(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessionStorageView{Limits: st.Limits, Defaults: svc.SessionStorageDefaults(), Ranges: sessionstorage.Ranges,
		StepMB: sessionstorage.StepMB, Custom: st.Set, UpdatedAt: st.UpdatedAt})
}

func (d Deps) handleGetSessionStorage(w http.ResponseWriter, r *http.Request) {
	if svc, ok := d.childLimitsService(w); ok {
		d.writeSessionStorage(w, r, svc)
	}
}

func (d Deps) handlePutSessionStorage(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.childLimitsService(w)
	if !ok {
		return
	}
	p, ok := principalFromCtx(r.Context())
	if !ok || p.Sid == "" {
		http.Error(w, "live browser session required", http.StatusUnauthorized)
		return
	}
	if _, _, live, err := d.principalLiveness(r.Context(), p.Sub, "", p.Sid); err != nil || !live {
		http.Error(w, "live browser session required", http.StatusUnauthorized)
		return
	}
	var body sessionstorage.Limits
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if err := body.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	before, err := svc.SetSessionStorage(r.Context(), p.Sub, body)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("session storage set by %s: default %d->%d MiB, maximum %d->%d MiB", p.Sub, before.DefaultUploadMB, body.DefaultUploadMB, before.MaxUploadMB, body.MaxUploadMB)
	d.writeSessionStorage(w, r, svc)
}

// handleInternalSessionStorage is POST /internal/session-storage: the settings the runner
// enforces. They are no account's data, so the internal key is the only gate.
func (d Deps) handleInternalSessionStorage(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.childLimitsService(w)
	if !ok {
		return
	}
	if d.InternalKey == "" {
		http.Error(w, "internal session storage unavailable", http.StatusServiceUnavailable)
		return
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
		return
	}
	st, err := svc.SessionStorage(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessionStorageInternal{Limits: st.Limits, Set: st.Set})
}

// accountStanding is what the runner learns about a person a broker (the board) names on a file
// change: whether the account exists and is active, and whether it is an administrator. The
// runner never takes the broker's word for either (#231).
type accountStanding struct {
	Exists bool `json:"exists"`
	Active bool `json:"active"`
	Admin  bool `json:"admin"`
}

// handleInternalAccountStanding is POST /internal/accounts/standing {"account": "<id>"}. The
// internal key is the only gate: it reveals no more than whether an id is a live (admin) account.
func (d Deps) handleInternalAccountStanding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if d.InternalKey == "" || d.Store == nil {
		http.Error(w, "internal account standing unavailable", http.StatusServiceUnavailable)
		return
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
		return
	}
	var body struct {
		Account string `json:"account"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Account == "" || len(body.Account) > 256 {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var st accountStanding
	var role string
	var disabled bool
	err := d.Store.Pool().QueryRow(r.Context(), `SELECT role, disabled_at IS NOT NULL FROM accounts WHERE id::text = $1`, body.Account).Scan(&role, &disabled)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	default:
		st = accountStanding{Exists: true, Active: !disabled, Admin: !disabled && role == "admin"}
	}
	writeJSON(w, st)
}
