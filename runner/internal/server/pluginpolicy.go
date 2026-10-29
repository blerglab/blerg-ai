package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func (a *API) currentPluginAllow() pluginspec.Allowlist {
	return a.marketplacePolicy.Allowlist(a.pluginAllow)
}
func (j *JobManager) currentPluginAllow() pluginspec.Allowlist {
	return j.MarketplacePolicy.Allowlist(j.PluginAllow)
}

// StartPluginPolicy refreshes Core authority across runner replicas, then fans it
// out on the existing authenticated daemon channel. It starts before HTTP serving.
// No account data, credentials, plugin contents, or install authority are sent.
func (a *API) StartPluginPolicy(ctx context.Context) error {
	policy, err := pluginspec.NewLivePolicy(publishDataDir("marketplace-policy.json"))
	if err != nil {
		return err
	}
	a.marketplacePolicy = policy
	if a.hub != nil {
		a.hub.marketplacePolicy = policy
		if jm := a.hub.JobManager(); jm != nil {
			jm.MarketplacePolicy = policy
		}
	}
	if a.coreURL == "" || a.coreInternalKey == "" {
		return nil
	}
	a.refreshPluginPolicy(ctx)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.refreshPluginPolicy(ctx)
			}
		}
	}()
	return nil
}
func (a *API) refreshPluginPolicy(ctx context.Context) {
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c := &httpSessionPluginCore{url: a.coreURL, key: a.coreInternalKey, client: coreHTTPClient(nil)}
	p, err := c.marketplacePolicy(requestCtx)
	if err == nil {
		// Unchanged polls, replays and older replicas cannot replace local authority.
		err = a.marketplacePolicy.Apply(p, 15*time.Second)
	}
	a.marketplacePolicy.LogApplyError(p, err)
	a.broadcastPluginPolicy()
}

func (c *httpSessionPluginCore) marketplacePolicy(ctx context.Context) (pluginspec.Policy, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/internal/plugins/marketplace-policy", nil)
	if err != nil {
		return pluginspec.Policy{}, errSessionPluginCore
	}
	req.Header.Set("X-Internal-Key", c.key)
	resp, err := c.client.Do(req)
	if err != nil {
		return pluginspec.Policy{}, errSessionPluginCore
	}
	defer func() { _ = resp.Body.Close() }()
	var p pluginspec.Policy
	dec := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 32<<10))
	if resp.StatusCode != 200 || dec.Decode(&p) != nil || p.Version != 1 || p.Revision < 1 || p.Normalize() != nil {
		return pluginspec.Policy{}, errSessionPluginCore
	}
	p.UpdatedBy = "" // Holders need authority, never the administrator account ID.
	return p, nil
}
func (a *API) broadcastPluginPolicy() {
	if a.hub == nil {
		return
	}
	for _, dc := range a.hub.GetAllDaemons() {
		a.hub.sendPluginPolicy(dc)
	}
}
func (h *Hub) sendPluginPolicy(dc *DaemonConn) {
	if !dc.PluginPolicy {
		return
	}
	msg := protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", SessionID: dc.BoundSessionID, LeaseSeconds: 15}
	if p, ok := h.marketplacePolicy.Snapshot(); ok {
		p.UpdatedBy = ""
		msg.Policy = &p
	}
	raw, _ := json.Marshal(msg)
	// Normal daemon scheduler: never write directly to a socket or block a handshake.
	select {
	case dc.send <- raw:
	default:
	}
}

// HandlePodPluginPolicy bootstraps only the current pod launch. Always read Core:
// a runner replica's cached high-water mark can predate a revoke on another pod.
func (a *API) HandlePodPluginPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fail := func(status int) { writeError(w, status, "current plugin marketplace policy unavailable") }
	if a.dbPool == nil {
		fail(http.StatusServiceUnavailable)
		return
	}
	raw := a.rawBearer(r)
	if raw == "" || a.isDaemonToken(raw) {
		fail(http.StatusUnauthorized)
		return
	}
	token, err := db.ValidateRuntimeToken(r.Context(), a.dbPool, raw)
	if err != nil || token.SessionID != r.URL.Query().Get("session_id") {
		fail(http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	c := &httpSessionPluginCore{url: a.coreURL, key: a.coreInternalKey, client: coreHTTPClient(nil)}
	if c.url == "" || c.key == "" {
		fail(http.StatusServiceUnavailable)
		return
	}
	p, err := c.marketplacePolicy(ctx)
	if err != nil {
		fail(http.StatusServiceUnavailable)
		return
	}
	// Replacement or stop during the Core call revokes bootstrap authority too.
	current, err := db.ValidateRuntimeToken(r.Context(), a.dbPool, raw)
	if err != nil || current.ID != token.ID {
		fail(http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", SessionID: token.SessionID, Policy: &p, LeaseSeconds: 15})
}
