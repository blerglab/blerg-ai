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

func (d Deps) desktopService(w http.ResponseWriter, r *http.Request) (*identity.Service, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if d.InternalKey == "" || !validInternalKey(r, d.InternalKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	svc, ok := d.identitySvc()
	if !ok {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}
	return svc, ok
}

func (d Deps) handleInternalIssueDesktopToken(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.desktopService(w, r)
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
	if !uuidShape.MatchString(body.DesktopID) || body.Revision < 1 {
		http.Error(w, "invalid desktop binding", http.StatusBadRequest)
		return
	}
	owner, err := svc.FirstDesktopOwner(r.Context(), owner)
	if err != nil {
		http.Error(w, "create the installing account first, or assign an enabled account", http.StatusConflict)
		return
	}
	b, token, err := svc.IssueDesktopToken(r.Context(), owner, body.DesktopID, body.Revision)
	if err != nil {
		http.Error(w, "desktop credential refused", http.StatusForbidden)
		return
	}
	// Component-key-only transport to the local installer or runner; never a browser route.
	writeJSON(w, map[string]any{"binding": b, "token": token})
}
func (d Deps) handleValidateDesktopToken(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.desktopService(w, r)
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
	svc, ok := d.desktopService(w, r)
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
