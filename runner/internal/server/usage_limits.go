package server

import (
	"math"
	"slices"
	"sort"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Usage limits (daemon → server "usage_limits"): each workstation daemon
// reports, per provider, how much of the account's limit windows is used.
// The server keeps the latest per (daemon, provider) in memory on the
// connection — nothing is stored — and pushes the daemon's set to browsers
// (daemon_limits_changed); initial_state carries it in DaemonInfo. A daemon
// says it all again on every connect. A cluster pod reports none.

// usageResetHorizon bounds a believable reset time, including monthly windows.
const usageResetHorizon = 31 * 24 * time.Hour

// sanitizeUsageLimits checks a daemon's report: false for a provider this
// server does not know. Unknown notes, unknown, repeated and expired windows are
// dropped, percentages clamped to 0–100, a reset in the far future forgotten,
// and a fetched_at in the future set to now.
func sanitizeUsageLimits(u protocol.UsageLimits, now time.Time) (protocol.UsageLimits, bool) {
	descriptor, registered := engines.Lookup(u.Provider)
	if u.Provider == "" || !registered || len(descriptor.UsageWindows) == 0 {
		return protocol.UsageLimits{}, false
	}
	out := protocol.UsageLimits{Provider: u.Provider, Windows: []protocol.UsageWindow{}, FetchedAt: u.FetchedAt.UTC()}
	if out.FetchedAt.IsZero() || out.FetchedAt.After(now) {
		out.FetchedAt = now.UTC()
	}
	seen := map[string]bool{}
	for _, w := range u.Windows {
		if !slices.Contains(descriptor.UsageWindows, w.ID) || seen[w.ID] {
			continue
		}
		seen[w.ID] = true
		if !w.ResetsAt.IsZero() && !w.ResetsAt.After(now) {
			continue // The old window is unknown until the provider reports its successor.
		}
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
	// The notes known; a note means nothing beside windows.
	if (u.Note == usageNoteNoWindows || u.Note == usageNoteNotRead) && len(out.Windows) == 0 {
		out.Note = u.Note
	}
	return out, true
}

// The notes a report with no windows may carry, kept (not a withdrawal) so the sidebar can say
// them: the provider answered and its account has no limit window (usageNoteNoWindows), or the
// daemon does not read this provider's limits (usageNoteNotRead: turned off, no sign-in it can
// read, the engine not installed).
const (
	usageNoteNoWindows = "no_windows"
	usageNoteNotRead   = "not_read"
)

// SetUsageLimits keeps a (sanitised) report as the provider's latest; an
// empty one withdraws the provider, unless it notes that there are none.
func (dc *DaemonConn) SetUsageLimits(u protocol.UsageLimits) {
	dc.liveMu.Lock()
	defer dc.liveMu.Unlock()
	if len(u.Windows) == 0 && u.Note == "" {
		delete(dc.usageLimits, u.Provider)
		return
	}
	if dc.usageLimits == nil {
		dc.usageLimits = map[string]protocol.UsageLimits{}
	}
	dc.usageLimits[u.Provider] = u
}

// UsageLimits is the daemon's latest report per provider, by provider name,
// with expired windows omitted even when the next report has not arrived.
// Never nil.
func (dc *DaemonConn) UsageLimits() []protocol.UsageLimits {
	return dc.usageLimitsAt(time.Now())
}

func (dc *DaemonConn) usageLimitsAt(now time.Time) []protocol.UsageLimits {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	out := make([]protocol.UsageLimits, 0, len(dc.usageLimits))
	for _, u := range dc.usageLimits {
		windows := make([]protocol.UsageWindow, 0, len(u.Windows))
		for _, w := range u.Windows {
			if w.ResetsAt.IsZero() || w.ResetsAt.After(now) {
				windows = append(windows, w)
			}
		}
		u.Windows = windows
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
	h.checkUsageNotices(dc, "")
	h.broadcastOwnedDesktop(dc, func() any {
		return protocol.DaemonLimitsChanged{
			OwnershipRevision: dc.ownershipRevision(), ConnectionGeneration: dc.DesktopGeneration,
			Type:        "daemon_limits_changed",
			DaemonID:    dc.ID,
			UsageLimits: dc.UsageLimits(),
		}
	})
}

// nilIfEmpty lets DaemonInfo omit usage_limits when there are none.
func nilIfEmpty(u []protocol.UsageLimits) []protocol.UsageLimits {
	if len(u) == 0 {
		return nil
	}
	return u
}
