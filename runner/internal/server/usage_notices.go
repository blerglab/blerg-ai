package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const usageNoticeSource = "usage_limit"

// Usage checks run outside the read pump and input locks, just like child-turn relays.
func (h *Hub) checkUsageNotices(dc *DaemonConn, session string) {
	h.mu.RLock()
	fn := h.usageNotices
	h.mu.RUnlock()
	if fn != nil {
		fn(dc, session)
	}
}

func (dc *DaemonConn) canUsageNotices() bool {
	if dc == nil {
		return false
	}
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.usageNotices
}

func (a *API) onUsageNotices(dc *DaemonConn, session string) {
	if a.dbPool == nil || !dc.canUsageNotices() || dc.Mode == "runner" || len(dc.UsageLimits()) == 0 {
		return
	}
	go func() {
		a.noteUsageLimits(context.Background(), dc, session)
	}()
}

// noteUsageLimits sends newly reached thresholds once per session/window. A turn-start
// catch-up summarizes the current state once and covers every lower threshold. No
// person-facing notice, Needs you entry or push is created.
func (a *API) noteUsageLimits(ctx context.Context, dc *DaemonConn, session string) {
	if a.dbPool == nil || !dc.canUsageNotices() || dc.Mode == "runner" {
		return
	}
	a.usageNoticesMu.Lock()
	defer a.usageNoticesMu.Unlock()
	// A queued callback can outlive its connection. It must not mutate warning
	// instances belonging to the replacement connection before recipient checks.
	if a.hub.GetDaemon(dc.ID) != dc {
		return
	}
	// Lock wait time must not consume the delivery timeout.
	timeout := a.usageNoticeTimeout
	if timeout <= 0 {
		timeout = childTurnNoteTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	thresholds, err := a.usageNoticeThresholds(ctx)
	if err != nil {
		log.Printf("usage notices settings: %v", err)
		return
	}
	now := time.Now()
	limits := dc.UsageLimits()
	if err := db.PruneUsageNotices(ctx, a.dbPool, dc.ID); err != nil {
		return
	}
	for _, u := range limits {
		for _, w := range u.Windows {
			if w.ID == "capped" || (!w.ResetsAt.IsZero() && !w.ResetsAt.After(now)) {
				continue
			}
			if err := db.ObserveUsageNoticeWindow(ctx, a.dbPool, dc.ID, u.Provider, w.ID, w.ResetsAt, w.UsedPercent < float64(thresholds[0]), usageResetTolerance(w.ID)); err != nil {
				log.Printf("usage notice window: %v", err)
				return
			}
		}
	}
	ids := []string{session}
	if session == "" {
		ids, err = db.UsageNoticeCandidates(ctx, a.dbPool, dc.ID)
		if err != nil {
			return
		}
	}
	for _, id := range ids {
		// Reload policy facts not carried by list queries, and reject stale/disconnected owners.
		row, err := db.GetSession(ctx, a.dbPool, id)
		if err != nil || row == nil || row.Kind != "agent" || isChildSession(row) || parentEnded(row) || row.ArchivedAt != nil || row.DiscardRequestedAt != nil || a.hub.FindDaemonForSession(row.ID) != dc || a.hub.ownedHostDaemon(ctx, derefOrEmpty(row.SpawningAccountID), dc.ID) != dc {
			continue
		}
		children, err := db.ListChildSessions(ctx, a.dbPool, row.ID)
		if err != nil {
			continue
		}
		running, same := 0, 0
		engine := derefOrEmpty(row.Engine)
		if engine == "" {
			engine = "claude"
		}
		for _, c := range children {
			if c.Status == "running" && c.EndedAt == nil {
				running++
				if c.Engine == engine || (c.Engine == "" && engine == "claude") {
					same++
				}
			}
		}
		if row.Status != "running" && running == 0 {
			continue
		}
		for _, u := range limits {
			if u.Provider != engine {
				continue
			}
			for _, w := range u.Windows {
				if w.ID == "capped" || (!w.ResetsAt.IsZero() && !w.ResetsAt.After(now)) {
					continue
				}
				level, reached := -1, 0
				for i, t := range thresholds {
					if w.UsedPercent >= float64(t) {
						level, reached = i, t
					}
				}
				if level < 0 || level >= len(thresholds) {
					continue
				}
				previous, instance, err := db.UsageNoticeState(ctx, a.dbPool, row.ID, dc.ID, engine, w.ID, w.ResetsAt)
				if err != nil || previous >= reached {
					continue
				}
				var crossed []string
				for _, t := range thresholds[:level] {
					if t > previous {
						crossed = append(crossed, fmt.Sprintf("%d%%", t))
					}
				}
				key := fmt.Sprintf("%s/%s/%d", row.ID, instance, reached)
				clientID := fmt.Sprintf("usage-notice-%x", sha256.Sum256([]byte(key)))
				text := usageNoticeText(engine, w, same, level, reached, dc, now)
				if len(crossed) > 0 {
					text += " Also crossed " + strings.Join(crossed, ", ") + "; this notice covers those thresholds."
				}
				principal := runnerPrincipal{Kind: "human", Sub: derefOrEmpty(row.SpawningAccountID)}
				var problem *APIError
				if dc.CanInputReceipts() {
					problem = a.sendRelay(ctx, principal, row.ID, text, usageNoticeSource, clientID)
				} else {
					problem = a.sendRelay(ctx, principal, row.ID, text, usageNoticeSource)
				}
				if problem != nil {
					log.Printf("usage notice %s: %s", row.ID, problem.Message)
					continue
				}
				if err := db.SetUsageNoticeThreshold(ctx, a.dbPool, row.ID, dc.ID, engine, w.ID, instance, reached); err != nil {
					log.Printf("usage notice state %s: %v", row.ID, err)
				}
			}
		}
	}
}

func usageNoticeText(engine string, w protocol.UsageWindow, children, level, threshold int, dc *DaemonConn, now time.Time) string {
	reset := "unknown"
	if !w.ResetsAt.IsZero() {
		reset = w.ResetsAt.UTC().Format(time.RFC3339)
	}
	guidance := []string{"Prefer other engines for new children and reviews.", "Start nothing new on this engine; let running work finish.", "Finish the current step and save state."}[level]
	var headroom []string
	for _, e := range budgetFor(dc, now).Engines {
		if e.Engine == engine || !e.Available {
			continue
		}
		known := false
		for _, other := range e.Windows {
			if !other.ResetsAt.IsZero() && !other.ResetsAt.After(now) {
				continue
			}
			headroom = append(headroom, fmt.Sprintf("%s %s: %s%% headroom", e.Engine, other.ID, usagePercent(100-other.UsedPercent)))
			known = true
		}
		if !known {
			note := e.Note
			if note == "" {
				note = "usage unknown"
			}
			headroom = append(headroom, e.Engine+": "+note)
		}
	}
	if len(headroom) == 0 {
		headroom = append(headroom, "no other engines with reported headroom")
	}
	childNoun := "sessions"
	if children == 1 {
		childNoun = "session"
	}
	return fmt.Sprintf("Usage limit: %s %s is %s%% used (threshold %d%%); resets %s. Other engines: %s. This session has %d running child %s on %s. %s", engine, w.ID, usagePercent(w.UsedPercent), threshold, reset, strings.Join(headroom, "; "), children, childNoun, engine, guidance)
}

// Reported resets are observations, not exact window identities. Small shifts keep
// the original anchor; only a substantial forward move can re-arm high usage.
func usageResetTolerance(window string) time.Duration {
	if strings.HasPrefix(window, "7d_") { // model-specific weekly windows: 7d_opus, 7d_sonnet, 7d_fable
		return 12 * time.Hour
	}
	switch window {
	case "5h":
		return time.Hour
	case "7d", "spend-week":
		return 12 * time.Hour
	case "month":
		return 24 * time.Hour
	default:
		return time.Hour
	}
}

func usagePercent(percent float64) string {
	return strconv.FormatFloat(math.Round(percent*10)/10, 'f', -1, 64)
}
