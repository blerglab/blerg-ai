package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func daemonAuthorityPin(dc *DaemonConn) (db.DaemonAuthority, bool) {
	if dc.DesktopBinding != nil {
		b := dc.DesktopBinding
		return db.DaemonAuthority{DaemonID: dc.ID, OwnerID: b.OwnerID, Revision: b.Revision, Generation: dc.DesktopGeneration}, true
	}
	if dc.LegacyDesktop {
		return db.DaemonAuthority{DaemonID: dc.ID, Revision: dc.LegacyRevision, Generation: dc.DesktopGeneration, Legacy: true}, true
	}
	return db.DaemonAuthority{}, false
}

func reconcilePinnedBackground(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, pin db.DaemonAuthority, inv *protocol.BackgroundTaskInventory) error {
	if pool == nil || dc.BackgroundTaskLifetime == "" || !protocol.ValidBackgroundTaskInventory(inv) || inv.Lifetime != dc.BackgroundTaskLifetime {
		return nil
	}
	bound := dc.BoundSessionID
	if bound == "" {
		bound = runnerBoundSession(dc.Mode, dc.Name)
	}
	changes, err := db.ReconcileBackgroundTasksPinned(ctx, pool, pin, bound, inv.Lifetime, inv.Sessions)
	publishBackgroundTaskReconciliation(ctx, h, pool, changes)
	return err
}

// Hello retries busy rows for up to its 10-second context without holding the fence.
func reconcilePinnedBackgroundHello(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, pin db.DaemonAuthority, inv *protocol.BackgroundTaskInventory) error {
	if pool == nil || dc.BackgroundTaskLifetime == "" || !protocol.ValidBackgroundTaskInventory(inv) || inv.Lifetime != dc.BackgroundTaskLifetime {
		return nil
	}
	bound := dc.BoundSessionID
	if bound == "" {
		bound = runnerBoundSession(dc.Mode, dc.Name)
	}
	for {
		changes, err := db.ReconcileBackgroundTasksPinnedWait(ctx, pool, pin, bound, inv.Lifetime, inv.Sessions)
		publishBackgroundTaskReconciliation(ctx, h, pool, changes)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "55P03" || ctx.Err() != nil {
			return err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// reconcilePinnedHeartbeat runs only after the socket's authority fence has
// released. Each database mutation revalidates the captured generation.
func reconcilePinnedHeartbeat(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, pin db.DaemonAuthority, hb protocol.DaemonHeartbeat, reported, queued []string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	start := time.Now()
	err := reconcilePinnedBackground(ctx, h, pool, dc, pin, hb.BackgroundTasks)
	logHeartbeatReconcileTiming("background", time.Since(start), err)
	if errors.Is(err, db.ErrDesktopAuthority) || ctx.Err() != nil {
		return
	}
	start = time.Now()
	stopped, err := reconcilePinnedSessions(ctx, h, pool, pin, reported, queued, hb.SessionStates)
	logHeartbeatReconcileTiming("sessions", time.Since(start), err)
	if err != nil || ctx.Err() != nil {
		return
	}
	if h.GetDaemon(dc.ID) != dc {
		return
	}
	killStoppedSessions(dc, stopped)
	scheduleDaemonReplay(ctx, h, pool, dc, reported)
}

func reconcilePinnedSessions(ctx context.Context, h *Hub, pool *pgxpool.Pool, pin db.DaemonAuthority, reported, queued []string, states map[string]string) ([]string, error) {
	active := make(map[string]bool, len(reported))
	for _, id := range reported {
		active[id] = true
	}
	waiting := make(map[string]bool, len(queued))
	for _, id := range queued {
		waiting[id] = true
	}
	sessions, err := db.ListSessionsByDaemon(ctx, pool, pin.DaemonID)
	if err != nil {
		return nil, err
	}
	ids := append(append([]string(nil), reported...), queued...)
	orphans, err := db.OrphanedChildren(ctx, pool, ids)
	if err != nil {
		return nil, err
	}
	orphaned := make(map[string]bool, len(orphans))
	for _, id := range orphans {
		orphaned[id] = true
	}
	var stopped []string
	now := time.Now()
	for _, s := range sessions {
		if !pin.Legacy && (s.SpawningAccountID == nil || *s.SpawningAccountID != pin.OwnerID) {
			continue
		}
		if orphaned[s.ID] {
			fresh, err := db.GetSession(ctx, pool, s.ID)
			if err != nil {
				return stopped, err
			}
			if fresh == nil {
				continue
			}
			s = *fresh
			if s.DiscardRequestedAt != nil {
				applied, err := db.ReconcilePinnedPendingEnd(ctx, pool, pin, s.ID, s.Status, s.EndReason, true)
				if err != nil {
					return stopped, err
				}
				if applied {
					revokeBoardTokens(ctx, s.ID)
					stopped = append(stopped, s.ID)
				}
				continue
			}
			applied, err := db.ReconcileSessionStatus(ctx, pool, pin, s.ID, s.Status, s.EndReason, "ended", &now, systemEnd(db.EndReasonParentEnded), true)
			if err != nil {
				return stopped, err
			}
			if applied {
				revokeBoardTokens(ctx, s.ID)
				h.SessionTerminallyEnded(s.ID)
				stopped = append(stopped, s.ID)
			}
			continue
		}
		if (active[s.ID] || waiting[s.ID]) && stoppedForGood(s.Status, s.EndReason) {
			confirmed, err := db.ConfirmPinnedStoppedSession(ctx, pool, pin, s.ID, s.Status, s.EndReason)
			if err != nil {
				return stopped, err
			}
			if confirmed {
				stopped = append(stopped, s.ID)
			}
			continue
		}
		if active[s.ID] && s.EndReason != nil && !terminalSessionStatus(s.Status) {
			changed, err := db.ReconcilePinnedPendingEnd(ctx, pool, pin, s.ID, s.Status, s.EndReason, false)
			if err != nil {
				return stopped, err
			}
			if changed {
				s.EndReason = nil
			}
		}
		if waiting[s.ID] && !active[s.ID] {
			continue
		}
		if !active[s.ID] && isActiveStatus(s.Status) && now.Sub(s.StartedAt) < unreportedGrace {
			continue
		}
		desired, change := reconcileDesiredStatus(s.Status, active[s.ID], states[s.ID])
		if !change {
			continue
		}
		var ended *time.Time
		var end db.SessionEnd
		if desired == "stopped" {
			ended = &now
			end = systemEnd(db.EndReasonDaemonUnreported)
		}
		applied, err := db.ReconcileSessionStatus(ctx, pool, pin, s.ID, s.Status, s.EndReason, desired, ended, end, desired == "stopped")
		if err != nil {
			return stopped, err
		}
		if !applied {
			continue
		}
		if desired == "stopped" {
			revokeBoardTokens(ctx, s.ID)
		}
		publishPinnedSessionStatus(ctx, h, pool, s.ID, desired, desired == "stopped")
	}
	return stopped, nil
}

func publishPinnedSessionStatus(ctx context.Context, h *Hub, pool *pgxpool.Pool, id, status string, final bool) {
	_ = db.RefreshInsightTiming(ctx, pool, id)
	if isLiveStatus(status) {
		markStartReady(ctx, h, pool, id, false)
	} else if status == "error" || status == "stopped" {
		failStart(ctx, h, pool, id, "The session ended before it was ready", "")
	}
	var unread bool
	var shown sessionEnd
	if row, err := db.GetSession(ctx, pool, id); err == nil && row != nil {
		unread = row.Unread
		shown = rowEnd(row)
	}
	if final && terminalSessionStatus(status) {
		failPendingSessionInputs(ctx, h, pool, id)
		h.NotifyCompletion(id)
	}
	if status != "idle" {
		h.CancelIdleNotice(id)
	}
	//nolint:contextcheck // broadcasts use independent bounded delivery checks
	broadcastWithEnd(h, shown, func(reason string, by *protocol.EndedBy) any {
		return protocol.SessionStateChanged{Type: "session_state_changed", SessionID: id, Status: status, Unread: unread, EndReason: reason, EndedBy: by}
	})
}

func logHeartbeatReconcileTiming(stage string, elapsed time.Duration, err error) {
	attrs := []any{"component", "server", "operation", "daemon_heartbeat_reconcile", "stage", stage, "elapsed_ms", float64(elapsed) / float64(time.Millisecond)}
	if err != nil {
		attrs = append(attrs, "cause", failurelog.Cause(err))
	}
	slog.Debug("heartbeat reconciliation timing", attrs...)
	if elapsed >= 250*time.Millisecond || err != nil {
		failurelog.Default.Event("daemon_heartbeat_reconcile:"+stage, slog.LevelWarn, attrs...)
	}
}
