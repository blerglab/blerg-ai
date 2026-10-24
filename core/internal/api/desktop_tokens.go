package api

import (
	"encoding/json"
	"net/http"

	"github.com/blerglab/blerg-ai/core/internal/identity"
)

type desktopIssueRequest struct {
	DesktopID string `json:"desktop_id"`
	Revision  int64  `json:"ownership_revision"`
	OwnerID   string `json:"owner_id,omitempty"` // internal bootstrap only
}

func (d Deps) desktopService(w http.ResponseWriter, r *http.Request, internal bool) (*identity.Service, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if internal && (d.InternalKey == "" || !validInternalKey(r, d.InternalKey)) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}
	return svc, ok
}

func (d Deps) issueDesktop(w http.ResponseWriter, r *http.Request, internal bool) {
	svc, ok := d.desktopService(w, r, internal)
	if !ok {
		return
	}
	var body desktopIssueRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil {
		http.Error(w, "invalid desktop binding", http.StatusBadRequest)
		return
	}
	owner := body.OwnerID
	if !internal {
		p, ok := principalFromCtx(r.Context())
		if !ok || p.Sid == "" || body.OwnerID != "" {
			http.Error(w, "live person login required", http.StatusForbidden)
			return
		}
		if _, _, live, err := d.principalLiveness(r.Context(), p.Sub, "", p.Sid); err != nil || !live {
			http.Error(w, "live person login required", http.StatusUnauthorized)
			return
		}
		if body.DesktopID != "" || (body.Revision != 0 && body.Revision != 1) {
			http.Error(w, "new desktop issuance cannot choose an existing identity", http.StatusBadRequest)
			return
		}
		var err error
		body.DesktopID, err = identity.NewDesktopID()
		if err != nil {
			http.Error(w, "credential unavailable", http.StatusServiceUnavailable)
			return
		}
		body.Revision = 1
		owner = p.Sub
	} else {
		if !uuidShape.MatchString(body.DesktopID) || body.Revision < 1 {
			http.Error(w, "invalid desktop binding", http.StatusBadRequest)
			return
		}
		var err error
		owner, err = svc.FirstDesktopOwner(r.Context(), owner)
		if err != nil {
			http.Error(w, "create the installing account first, or assign an enabled account", http.StatusConflict)
			return
		}
	}
	b, token, err := svc.IssueDesktopToken(r.Context(), owner, body.DesktopID, body.Revision)
	if err != nil {
		http.Error(w, "desktop credential refused", http.StatusForbidden)
		return
	}
	// The only response containing the bearer: no store, no logging, never broadcast.
	writeJSON(w, map[string]any{"binding": b, "token": token})
}
func (d Deps) handleIssueDesktopToken(w http.ResponseWriter, r *http.Request) {
	d.issueDesktop(w, r, false)
}
func (d Deps) handleInternalIssueDesktopToken(w http.ResponseWriter, r *http.Request) {
	d.issueDesktop(w, r, true)
}
func (d Deps) handleValidateDesktopToken(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.desktopService(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	b, err := svc.DesktopToken(r.Context(), body.Token)
	if err != nil {
		http.Error(w, "credential refused", http.StatusUnauthorized)
		return
	}
	writeJSON(w, b)
}
func (d Deps) handleDesktopOwners(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.desktopService(w, r, true)
	if !ok {
		return
	}
	owners, err := svc.DesktopOwners(r.Context())
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, owners)
}
