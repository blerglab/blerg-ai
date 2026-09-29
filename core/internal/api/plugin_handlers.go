package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/core/internal/plugins"
)

// Always-on plugins: GET/PUT /api/plugins/{engine} (the signed-in person's own list) and
// POST /internal/plugins/list (blerg-runner, when it shapes a cluster session pod).

// pluginListResponse is GET /api/plugins/{engine}. AllowedMarketplaces/AnyMarketplace tell the
// Settings page what this install accepts; nothing here is secret.
type pluginListResponse struct {
	Engine              string          `json:"engine"`
	Plugins             []plugins.Entry `json:"plugins"`
	AllowedMarketplaces []string        `json:"allowed_marketplaces"`
	AnyMarketplace      bool            `json:"any_marketplace"`
	Max                 int             `json:"max"`
}

type pluginPutRequest struct {
	Plugins []plugins.Entry `json:"plugins"`
}

func (d Deps) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	if d.Plugins == nil {
		http.Error(w, "plugins service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	engine := r.PathValue("engine")
	// The account is always the caller's own (the verified token's Sub) — never a request field —
	// so one account can neither read nor change another's list.
	list, err := d.Plugins.List(r.Context(), principal.Sub, engine)
	if err != nil {
		writePluginError(w, err)
		return
	}
	d.writePluginList(w, engine, list)
}

// handlePutPlugins replaces the caller's whole list for an engine (add, remove and reorder are
// all "put the new list").
//
// SECURITY: this is registered behind requireHumanPrincipal, so only a signed-in human browser
// session for the same account can reach it — never an agent token. That is deliberate and must
// not be loosened. The list decides which third-party code (plugins: skills, hooks, MCP servers)
// is installed and run inside EVERY future cluster session of the account, including sessions
// started with an automation token. A prompt-injected agent that could add a plugin would turn
// one bad turn into persistent code execution in all later sessions. Reads by the runner go
// through the internal endpoint below, which cannot write.
func (d Deps) handlePutPlugins(w http.ResponseWriter, r *http.Request) {
	if d.Plugins == nil {
		http.Error(w, "plugins service unavailable", http.StatusServiceUnavailable)
		return
	}
	principal, ok := principalFromCtx(r.Context())
	if !ok {
		http.Error(w, "missing principal", http.StatusUnauthorized)
		return
	}
	engine := r.PathValue("engine")
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body pluginPutRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if body.Plugins == nil {
		http.Error(w, "plugins is required", http.StatusBadRequest)
		return
	}
	if err := d.Plugins.Replace(r.Context(), principal.Sub, engine, body.Plugins); err != nil {
		writePluginError(w, err)
		return
	}
	list, err := d.Plugins.List(r.Context(), principal.Sub, engine)
	if err != nil {
		writePluginError(w, err)
		return
	}
	d.writePluginList(w, engine, list)
}

func (d Deps) writePluginList(w http.ResponseWriter, engine string, list []plugins.Entry) {
	allowed, anyMkt := d.Plugins.Allowlist().List()
	if allowed == nil {
		allowed = []string{}
	}
	writeJSON(w, pluginListResponse{
		Engine: engine, Plugins: list, AllowedMarketplaces: allowed, AnyMarketplace: anyMkt, Max: pluginspec.MaxEntries,
	})
}

func writePluginError(w http.ResponseWriter, err error) {
	var ve *plugins.ValidationError
	switch {
	case errors.As(err, &ve):
		http.Error(w, ve.Msg, http.StatusUnprocessableEntity)
	case errors.Is(err, plugins.ErrConflict):
		http.Error(w, "your plugin list was changed by another request; reload and try again", http.StatusConflict)
	case errors.Is(err, plugins.ErrUnknownEngine):
		http.Error(w, "unknown engine", http.StatusNotFound)
	default:
		log.Printf("plugins: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// internalPluginsRequest is POST /internal/plugins/list's body.
//
// The liveness proof is MANDATORY and explicit, exactly as on the credential endpoints: the
// caller names which principal stands behind the session — TokenID (an agent-token session) or
// SessionID (the human browser session that launched it), exactly one — and core checks that
// specific principal is live and belongs to AccountID. There is no fallback.
type internalPluginsRequest struct {
	AccountID string `json:"account_id"`
	Engine    string `json:"engine"`
	TokenID   string `json:"token_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type internalPluginsResponse struct {
	Plugins []plugins.Entry `json:"plugins"`
}

// handleInternalListPlugins is POST /internal/plugins/list: the runner server reads an
// account's always-on plugins to hand a new cluster session pod. Same internal key as the
// credential endpoints, same anti-enumeration 404 for a dead principal; returns only the list.
func (d Deps) handleInternalListPlugins(w http.ResponseWriter, r *http.Request) {
	if d.InternalKey == "" {
		http.Error(w, "internal plugins disabled", http.StatusServiceUnavailable)
		return
	}
	if !validInternalKey(r, d.InternalKey) {
		http.Error(w, "invalid internal key", http.StatusUnauthorized)
		return
	}
	if d.Plugins == nil || d.Store == nil {
		http.Error(w, "plugins service unavailable", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body internalPluginsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if body.AccountID == "" || body.Engine == "" {
		http.Error(w, "account_id and engine are required", http.StatusBadRequest)
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
		log.Printf("internal plugins: no live principal for account %s", body.AccountID)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	list, err := d.Plugins.List(r.Context(), body.AccountID, body.Engine)
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeJSON(w, internalPluginsResponse{Plugins: list})
}
