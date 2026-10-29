package api

// Install-wide child start limits: an administrator with a live sign-in may
// update them; runner reads them with its internal key at the next admission.

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/childlimits"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

type childLimitsView struct {
	childlimits.Limits
	Defaults  childlimits.Limits `json:"defaults"`
	Ranges    map[string][2]int  `json:"ranges"`
	Custom    bool               `json:"custom"`
	UpdatedAt *time.Time         `json:"updated_at"`
	// Zero: read again on the next start request.
	RunnerRefreshSeconds int `json:"runner_refresh_seconds"`
}

func (d Deps) childLimitsService(w http.ResponseWriter) (*identity.Service, bool) {
	w.Header().Set("Cache-Control", "no-store")
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
	}
	return svc, ok
}

func (d Deps) writeChildLimits(w http.ResponseWriter, r *http.Request, svc *identity.Service) {
	st, err := svc.ChildLimits(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, childLimitsView{Limits: st.Limits, Defaults: svc.ChildLimitDefaults(), Ranges: childlimits.Ranges,
		Custom: st.Set, UpdatedAt: st.UpdatedAt, RunnerRefreshSeconds: 0})
}

func (d Deps) handleGetChildLimits(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.childLimitsService(w)
	if !ok {
		return
	}
	d.writeChildLimits(w, r, svc)
}

func (d Deps) handlePutChildLimits(w http.ResponseWriter, r *http.Request) {
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
	var body childlimits.Limits
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	before, err := svc.ChildLimits(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := body.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := svc.SetChildLimits(r.Context(), p.Sub, body); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("child limits set by %s: parallel %d->%d, starts per hour %d->%d", p.Sub, before.Parallel, body.Parallel, before.StartsPerHour, body.StartsPerHour)
	d.writeChildLimits(w, r, svc)
}

// handleInternalChildLimits is POST /internal/child-limits: the limits the runner enforces. They are
// no account's data, so the internal key is the only gate.
func (d Deps) handleInternalChildLimits(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.childLimitsService(w)
	if !ok {
		return
	}
	if d.InternalKey == "" {
		http.Error(w, "internal child limits unavailable", http.StatusServiceUnavailable)
		return
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
		return
	}
	st, err := svc.ChildLimits(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, st.Limits)
}
