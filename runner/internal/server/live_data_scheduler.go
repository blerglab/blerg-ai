package server

import (
	"context"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/livepoll"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// RunLivePolling is deliberately inert under the production approval gate.
// There is no environment switch or agent endpoint that enables it.
func (a *API) RunLivePolling(ctx context.Context, installation, runner string, desktop bool, policy netguard.Policy) {
	if !a.liveDataPollingEnabled {
		_ = boundFailure("live_data_disabled")
		return
	}
	if a.liveDataAuthority == nil || a.dbPool == nil {
		_ = boundFailure("live_data_unavailable")
		return
	}
	a.hub.liveDataPollingEnabled.Store(a.liveDataPollingEnabled)
	gateway := mcpgw.New(mcpgw.Config{DB: a.dbPool, Policy: policy})
	attempts := livepoll.Attempts{Pool: a.dbPool}
	verifier := &livepoll.Verifier{Enabled: a.liveDataPollingEnabled, Desktop: desktop, Installation: installation, Runner: runner, Authority: a.liveDataAuthority, Attempts: attempts, Provider: gateway.LiveProvider(a.liveDataAuthority)}
	scheduler := &livepoll.Scheduler{Enabled: a.liveDataPollingEnabled, Store: attempts, Minter: a.liveDataAuthority, Executor: verifier}
	if err := scheduler.Run(ctx); err != nil && ctx.Err() == nil {
		_ = boundFailure("live_scheduler_unavailable")
	}
}
