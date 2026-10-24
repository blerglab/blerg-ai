package server

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// backgroundTaskPayload assigns origin from the authenticated connection, never
// from engine output. Only reconciliation may target a particular start event.
func backgroundTaskPayload(raw json.RawMessage, lifetime string) (json.RawMessage, bool) {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil || p == nil {
		return nil, false
	}
	var taskID string
	if json.Unmarshal(p["task_id"], &taskID) != nil || !protocol.ValidBackgroundTaskID(taskID) {
		return nil, false
	}
	stripBackgroundTaskMarkers(p)
	if lifetime != "" {
		p["daemon_lifetime"], _ = json.Marshal(lifetime)
	}
	cleaned, err := json.Marshal(p)
	return cleaned, err == nil
}

// A nil, malformed or changed-lifetime report is unknown, never an empty list.
// Current-lifetime starts are protected even when a snapshot races a new start.
func reconcileBackgroundTaskInventory(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, inventory *protocol.BackgroundTaskInventory) {
	if pool == nil || dc.BackgroundTaskLifetime == "" || !protocol.ValidBackgroundTaskInventory(inventory) || inventory.Lifetime != dc.BackgroundTaskLifetime {
		return
	}
	unlock := h.lockDaemonWork(dc.ID)
	changes := reconcileBackgroundTaskInventoryLocked(ctx, h, pool, dc, inventory)
	unlock()
	publishBackgroundTaskReconciliation(ctx, h, pool, changes)
}

// Caller holds this daemon ID's work fence through the ownership check and commit.
func reconcileBackgroundTaskInventoryLocked(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, inventory *protocol.BackgroundTaskInventory) []db.BackgroundTaskReconciliation {
	if pool == nil || dc.BackgroundTaskLifetime == "" || !protocol.ValidBackgroundTaskInventory(inventory) || inventory.Lifetime != dc.BackgroundTaskLifetime {
		return nil
	}
	if h.GetDaemon(dc.ID) != dc {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	bound := dc.BoundSessionID
	if bound == "" {
		bound = runnerBoundSession(dc.Mode, dc.Name)
	}
	changes, err := db.ReconcileBackgroundTasks(ctx, pool, dc.ID, bound, inventory.Lifetime, inventory.Sessions)
	if err != nil {
		log.Printf("background task reconciliation daemon %s: %v", dc.ID, err)
	}
	return changes
}

func publishBackgroundTaskReconciliation(ctx context.Context, h *Hub, pool *pgxpool.Pool, changes []db.BackgroundTaskReconciliation) {
	for _, change := range changes {
		for _, ev := range change.Events {
			fanOutServerEvent(h, ev.SessionID, ev.ClientEventID, ev.Kind, []byte(ev.Payload), ev.Seq, ev.Ts)
		}
		h.BroadcastJSON(protocol.SessionBackgroundChanged{Type: "session_background_changed", SessionID: change.SessionID, BackgroundTasks: change.Count}) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
		if len(change.Events) > 0 {
			noticeOnWorkEnded(ctx, h, pool, change.SessionID)
		}
	}
}

// encoding/json matches struct fields case-insensitively, so reserved aliases
// must be removed before authoritative lowercase fields are assigned.
func stripBackgroundTaskMarkers(p map[string]json.RawMessage) {
	for key := range p {
		if strings.EqualFold(key, "daemon_lifetime") || strings.EqualFold(key, "start_event_id") || strings.EqualFold(key, "origin_start_seq") {
			delete(p, key)
		}
	}
}
