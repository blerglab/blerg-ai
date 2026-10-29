package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/core/internal/plugins"
	"github.com/jackc/pgx/v5"
)

func (d Deps) handleGetPluginPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if d.Plugins == nil {
		http.Error(w, "plugin policy unavailable", http.StatusServiceUnavailable)
		return
	}
	p, err := d.Plugins.Policy(r.Context())
	if err != nil {
		http.Error(w, "plugin policy unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, p)
}
func (d Deps) handlePutPluginPolicy(w http.ResponseWriter, r *http.Request) {
	if !d.sameOriginRequest(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok || principal.Sid == "" {
		http.Error(w, "administrator sign-in required", http.StatusForbidden)
		return
	}
	if d.Plugins == nil {
		http.Error(w, "plugin policy unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Revision       int64                    `json:"revision"`
		Marketplaces   []pluginspec.Marketplace `json:"marketplaces"`
		AnyMarketplace *bool                    `json:"any_marketplace"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || body.Revision < 1 || body.AnyMarketplace == nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	p, err := d.Plugins.SetPolicy(r.Context(), principal.Sub, principal.Sid, body.Revision, pluginspec.Policy{Marketplaces: body.Marketplaces, AnyMarketplace: *body.AnyMarketplace})
	var ve *plugins.ValidationError
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, plugins.ErrHumanProof):
		http.Error(w, "administrator sign-in required", http.StatusForbidden)
	case errors.Is(err, plugins.ErrConflict):
		http.Error(w, "marketplace policy changed; reload and try again", http.StatusConflict)
	case errors.As(err, &ve):
		http.Error(w, ve.Msg, http.StatusUnprocessableEntity)
	case err != nil:
		http.Error(w, "plugin policy unavailable", http.StatusServiceUnavailable)
	default:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, p)
	}
}
func (d Deps) handleInternalMarketplacePolicy(w http.ResponseWriter, r *http.Request) {
	if !d.internalPluginReady(w, r) {
		return
	}
	if d.Plugins == nil {
		http.Error(w, "plugin policy unavailable", http.StatusServiceUnavailable)
		return
	}
	p, err := d.Plugins.Policy(r.Context())
	if err != nil {
		http.Error(w, "plugin policy unavailable", http.StatusServiceUnavailable)
		return
	}
	p.UpdatedBy = ""
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, p)
}
