package api

import (
	"encoding/json"
	"net/http"
	"os"
)

// InstallType reports explicit configuration first, then the Kubernetes runtime signal.
func InstallType() string {
	switch os.Getenv("BLERG_INSTALL_TYPE") {
	case "desktop":
		return "desktop"
	case "kubernetes":
		return "kubernetes"
	case "":
		if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
			return "kubernetes"
		}
	}
	return "unknown"
}

func (d Deps) handleUpdateNotice(w http.ResponseWriter, r *http.Request) {
	svc, ok := d.childLimitsService(w)
	if !ok {
		return
	}
	p, present := principalFromCtx(r.Context())
	if !present {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	state, err := svc.AccountState(r.Context(), p.Sub)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if state.Role != "admin" || state.Disabled || state.MustChangePassword {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPut {
		p, ok := principalFromCtx(r.Context())
		if !ok || p.Sid == "" {
			http.Error(w, "live browser session required", http.StatusUnauthorized)
			return
		}
		if _, _, live, err := d.principalLiveness(r.Context(), p.Sub, "", p.Sid); err != nil || !live {
			http.Error(w, "live browser session required", http.StatusUnauthorized)
			return
		}
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || body.Enabled == nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if err := svc.SetUpdateNotice(r.Context(), p.Sub, *body.Enabled); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	enabled, err := svc.UpdateNoticeEnabled(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"enabled": enabled, "install_type": InstallType()})
}
