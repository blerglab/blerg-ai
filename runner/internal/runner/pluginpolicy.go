package runner

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// startPodPluginPolicy establishes current Core authority before socket updates
// can grant permissions. A fresh pod has no durable local revision; a cached
// runner replica or launch environment cannot supply its initial authority.
func startPodPluginPolicy(ctx context.Context, cfg Config, apply func(pluginspec.Policy) error) func() {
	ctx, cancel := context.WithCancel(ctx)
	refresh := func() bool {
		requestCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		if cfg.ServerHTTP == "" || cfg.DaemonToken == "" {
			return false
		}
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, cfg.ServerHTTP+"/api/session/plugin-marketplace-policy?session_id="+url.QueryEscape(cfg.SessionID), nil)
		if err != nil {
			return false
		}
		req.Header.Set("Authorization", "Bearer "+cfg.DaemonToken)
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<10+1))
		var msg protocol.PluginMarketplacePolicy
		if err != nil || len(raw) > 32<<10 || json.Unmarshal(raw, &msg) != nil || msg.Type != "plugin_marketplace_policy" || msg.SessionID != cfg.SessionID || msg.Policy == nil {
			return false
		}
		return apply(*msg.Policy) == nil
	}
	if refresh() {
		cancel()
		return func() {}
	}
	log.Print("runner: current marketplace policy unavailable; refusing plugin installs until bootstrap succeeds")
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if refresh() {
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
