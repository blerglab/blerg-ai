package server

import (
	"math"
	"sort"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Usage limits (daemon → server "usage_limits"): each workstation daemon
// reports, per provider, how much of the account's limit windows is used.
// The server keeps the latest per (daemon, provider) in memory on the
// connection — nothing is stored — and pushes the daemon's set to browsers
// (daemon_limits_changed); initial_state carries it in DaemonInfo. A daemon
// says it all again on every connect. A cluster pod reports none.

// usageProviders and usageWindowIDs are what a report may name.
var (
	usageProviders = map[string]bool{"codex": true, "claude": true}
	usageWindowIDs = map[string]bool{"5h": true, "7d": true, "capped": true}
)

// usageResetHorizon bounds a believable reset time: the longest window is a week.
const usageResetHorizon = 31 * 24 * time.Hour

// sanitizeUsageLimits checks a daemon's report: false for a provider this
// server does not know. Unknown and repeated windows are dropped, percentages
// clamped to 0–100, a reset in the far past or future forgotten, a fetched_at
// in the future set to now.
func sanitizeUsageLimits(u protocol.UsageLimits, now time.Time) (protocol.UsageLimits, bool) {
	if !usageProviders[u.Provider] {
		return protocol.UsageLimits{}, false
	}
	out := protocol.UsageLimits{Provider: u.Provider, Windows: []protocol.UsageWindow{}, FetchedAt: u.FetchedAt.UTC()}
	if out.FetchedAt.IsZero() || out.FetchedAt.After(now) {
		out.FetchedAt = now.UTC()
	}
	seen := map[string]bool{}
	for _, w := range u.Windows {
		if !usageWindowIDs[w.ID] || seen[w.ID] {
			continue
		}
		seen[w.ID] = true
		pct := w.UsedPercent
		if math.IsNaN(pct) || pct < 0 {
			pct = 0
		}
		pct = math.Min(pct, 100)
		reset := w.ResetsAt.UTC()
		if reset.Before(now.Add(-usageResetHorizon)) || reset.After(now.Add(usageResetHorizon)) {
			reset = time.Time{}
		}
		out.Windows = append(out.Windows, protocol.UsageWindow{ID: w.ID, UsedPercent: pct, ResetsAt: reset})
	}
	return out, true
}

// SetUsageLimits keeps a (sanitised) report as the provider's latest; an
// empty one withdraws the provider.
func (dc *DaemonConn) SetUsageLimits(u protocol.UsageLimits) {
	dc.liveMu.Lock()
	defer dc.liveMu.Unlock()
	if len(u.Windows) == 0 {
		delete(dc.usageLimits, u.Provider)
		return
	}
	if dc.usageLimits == nil {
		dc.usageLimits = map[string]protocol.UsageLimits{}
	}
	dc.usageLimits[u.Provider] = u
}

// UsageLimits is the daemon's latest report per provider, by provider name;
// never nil.
func (dc *DaemonConn) UsageLimits() []protocol.UsageLimits {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	out := make([]protocol.UsageLimits, 0, len(dc.usageLimits))
	for _, u := range dc.usageLimits {
		u.Windows = append([]protocol.UsageWindow{}, u.Windows...)
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// handleUsageLimits takes a daemon's usage_limits and tells browsers.
func handleUsageLimits(h *Hub, dc *DaemonConn, msg protocol.UsageLimits) {
	if dc.Mode == "runner" {
		return // a cluster session pod: not a machine anyone picks
	}
	clean, ok := sanitizeUsageLimits(msg, time.Now())
	if !ok {
		return
	}
	dc.SetUsageLimits(clean)
	h.BroadcastJSON(protocol.DaemonLimitsChanged{
		Type:        "daemon_limits_changed",
		DaemonID:    dc.ID,
		UsageLimits: dc.UsageLimits(),
	})
}

// nilIfEmpty lets DaemonInfo omit usage_limits when there are none.
func nilIfEmpty(u []protocol.UsageLimits) []protocol.UsageLimits {
	if len(u) == 0 {
		return nil
	}
	return u
}
