package server

import (
	"context"
	"log"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Usage window history: a periodic sample of a daemon's reported weekly ("7d") usage window,
// kept only long enough to draw Insights Spending's burndown (the current window's trace, and
// the previous window faintly). Everything else about usage_limits stays in-memory only; this is
// the one window worth persisting, because a subscription plan's weekly budget is what the
// owner asked the burndown to track (BLG-462). Other window ids (5h, month, spend windows) are
// not sampled: the sidebar meter and the "capped" reactive window already cover them live.

const (
	usageSampleWindow       = "7d"
	usageSampleKeepDuration = 15 * 24 * time.Hour // the current and previous 7d window, plus slack
)

// checkUsageSample records dc's latest "7d" windows, outside the read pump and input locks (same
// shape as checkUsageNotices).
func (h *Hub) checkUsageSample(dc *DaemonConn) {
	h.mu.RLock()
	fn := h.usageSample
	h.mu.RUnlock()
	if fn != nil {
		fn(dc)
	}
}

// onUsageSample is the registered hub.usageSample hook: fire-and-forget, like onUsageNotices.
func (a *API) onUsageSample(dc *DaemonConn) {
	if a.dbPool == nil || dc == nil || dc.Mode == "runner" {
		return
	}
	go a.recordUsageSamples(context.Background(), dc)
}

// recordUsageSamples stores a minute-rounded reading of dc's "7d" windows (one per provider) and
// prunes anything older than two windows.
func (a *API) recordUsageSamples(ctx context.Context, dc *DaemonConn) {
	if a.dbPool == nil {
		return
	}
	now := time.Now().UTC()
	sampledAt := now.Truncate(time.Minute)
	for _, u := range dc.UsageLimits() {
		for _, w := range u.Windows {
			if w.ID != usageSampleWindow || w.ResetsAt.IsZero() || !w.ResetsAt.After(now) {
				continue
			}
			windowStart := w.ResetsAt.Add(-7 * 24 * time.Hour)
			if err := db.RecordUsageWindowSample(ctx, a.dbPool, dc.ID, u.Provider, w.ID, w.UsedPercent, windowStart, w.ResetsAt, sampledAt); err != nil {
				log.Printf("usage window sample: %v", err)
			}
		}
	}
	if err := db.PruneUsageWindowSamples(ctx, a.dbPool, dc.ID, usageSampleKeepDuration); err != nil {
		log.Printf("usage window sample prune: %v", err)
	}
}
