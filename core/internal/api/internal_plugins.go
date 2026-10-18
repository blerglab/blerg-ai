package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/core/internal/plugins"
)

type internalPluginAddRequest struct {
	AccountID string           `json:"account_id"`
	SessionID string           `json:"session_id"`
	Engine    string           `json:"engine"`
	Entry     pluginspec.Entry `json:"entry"`
}

func (d Deps) internalPluginReady(w http.ResponseWriter, r *http.Request) bool {
	if d.InternalKey == "" || d.Plugins == nil || d.Store == nil {
		http.Error(w, "internal plugins unavailable", http.StatusServiceUnavailable)
		return false
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	return true
}

func decodeInternalPlugin(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		err = dec.Decode(new(any))
		if err == io.EOF {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
	} else {
		http.Error(w, "bad body", http.StatusBadRequest)
	}
	return false
}

// Catalog exposes installed settings and the operator's marketplace policy; it
// does not fetch or trust arbitrary remote marketplace manifests.
func (d Deps) handleInternalPluginCatalog(w http.ResponseWriter, r *http.Request) {
	if !d.internalPluginReady(w, r) {
		return
	}
	var body internalPluginsRequest
	if !decodeInternalPlugin(w, r, &body) {
		return
	}
	if msg := validateProofShape(body.AccountID, body.TokenID, body.SessionID); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if _, ok := plugins.Lookup(body.Engine); !ok {
		http.Error(w, "unknown engine", http.StatusBadRequest)
		return
	}
	if _, _, ok, err := d.principalLiveness(r.Context(), body.AccountID, body.TokenID, body.SessionID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	entries, err := d.Plugins.List(r.Context(), body.AccountID, body.Engine)
	if err != nil {
		writePluginError(w, err)
		return
	}
	d.writePluginList(w, body.Engine, entries)
}

// Policy contains no account settings or credentials, so the authenticated
// runner may check it before queuing an agent's request without a human proof.
func (d Deps) handleInternalPluginPolicy(w http.ResponseWriter, r *http.Request) {
	if !d.internalPluginReady(w, r) {
		return
	}
	var body struct {
		Engine string `json:"engine"`
	}
	if !decodeInternalPlugin(w, r, &body) {
		return
	}
	if _, ok := plugins.Lookup(body.Engine); !ok {
		http.Error(w, "unknown engine", http.StatusBadRequest)
		return
	}
	d.writePluginList(w, body.Engine, []plugins.Entry{})
}

// Add is intentionally narrower than Settings PUT: an internal caller must name
// the approving human's live browser session and can only append one entry.
// Agent-token proofs, arbitrary list replacements and foreign account proofs
// never grant persistent plugin installation authority.
func (d Deps) handleInternalPluginAdd(w http.ResponseWriter, r *http.Request) {
	if !d.internalPluginReady(w, r) {
		return
	}
	var body internalPluginAddRequest
	if !decodeInternalPlugin(w, r, &body) {
		return
	}
	if msg := validateProofShape(body.AccountID, "", body.SessionID); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if err := d.Plugins.AddApproved(r.Context(), body.AccountID, body.SessionID, body.Engine, body.Entry); err != nil {
		if errors.Is(err, plugins.ErrHumanProof) {
			http.NotFound(w, r)
		} else {
			writePluginError(w, err)
		}
		return
	}
	list, err := d.Plugins.List(r.Context(), body.AccountID, body.Engine)
	if err != nil {
		writePluginError(w, err)
		return
	}
	d.writePluginList(w, body.Engine, list)
}
