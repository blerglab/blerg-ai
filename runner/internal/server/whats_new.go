package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

// RegisterWhatsNewRoutes keeps browser traffic same-origin with runner. Core's
// internal handler deliberately has no browser or CORS surface.
func (a *API) RegisterWhatsNewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me/whats-new", a.handleWhatsNew)
	mux.HandleFunc("POST /api/me/whats-new", a.handleWhatsNew)
}

func (a *API) handleWhatsNew(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != string(identity.Human) || p.OnBehalfOf != "" || p.Sub == "" || p.Sid == "" {
		writeError(w, http.StatusForbidden, "human sign-in required")
		return
	}
	if a.coreURL == "" || a.coreInternalKey == "" {
		writeError(w, http.StatusServiceUnavailable, "release notes unavailable")
		return
	}
	body, _ := json.Marshal(map[string]any{"account_id": p.Sub, "session_id": p.Sid,
		"release": r.URL.Query().Get("release"), "dismiss": r.Method == http.MethodPost})
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.coreURL+"/internal/accounts/whats-new", bytes.NewReader(body))
	if err != nil {
		writeError(w, 503, "release notes unavailable")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", a.coreInternalKey)
	resp, err := coreHTTPClient(nil).Do(req) // #nosec G704 -- Operator-configured Core URL, with redirects disabled.
	if err != nil {
		writeError(w, 503, "release notes unavailable")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		status := http.StatusServiceUnavailable
		if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusForbidden {
			status = resp.StatusCode
		}
		writeError(w, status, "release notes unavailable")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (40<<10)+1))
	if err != nil || len(raw) > 40<<10 || !json.Valid(raw) {
		writeError(w, 503, "release notes unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}
