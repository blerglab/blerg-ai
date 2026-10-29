package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Each browser reconciles its own persisted scope, including changes committed
// by another replica. No foreign inventory is read or included in the hint.
func (h *Hub) desktopScopeRows(ctx context.Context, account string) (string, error) {
	if h.desktopPool == nil {
		return "", nil
	}
	rows, err := db.ListDesktops(ctx, h.desktopPool, account, false)
	// Connection generations and status/reasons change during enrollment's
	// reconnect. They are liveness, not a change to the viewer's source access.
	for i := range rows {
		rows[i].Generation = ""
		// Refusals carry actionable diagnostics even without a live connection.
		// Keep those observable; normal connect/disconnect events carry liveness.
		if rows[i].Status != "refused" {
			rows[i].Status = ""
			rows[i].StatusReason = ""
		}
	}
	raw, _ := json.Marshal(rows)
	return string(raw), err
}
func (h *Hub) refreshDesktopScope(ctx context.Context, bc *BrowserConn, pool *pgxpool.Pool, previous string) string {
	scope, err := h.desktopScopeRows(ctx, bc.AccountID)
	if err != nil || scope == previous {
		return previous
	}
	// Only a loss of access needs the privacy reset. Enrollment/credential
	// updates for the same viewer refresh inventory without remounting chat.
	var before, after []db.DesktopRow
	if json.Unmarshal([]byte(previous), &before) != nil || json.Unmarshal([]byte(scope), &after) != nil {
		return previous
	}
	current := make(map[string]bool, len(after))
	for _, row := range after {
		current[row.ID] = true
	}
	for _, row := range before {
		if current[row.ID] {
			continue
		}
		select {
		case bc.send <- []byte(`{"type":"desktop_inventory_invalidated"}`):
		case <-ctx.Done():
			return previous
		}
		break
	}
	sendInitialState(ctx, bc, h, pool)
	return scope
}
func (h *Hub) watchDesktopScope(ctx context.Context, bc *BrowserConn, pool *pgxpool.Pool, initial string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check, done := context.WithTimeout(ctx, 2*time.Second)
			initial = h.refreshDesktopScope(check, bc, pool, initial)
			done()
		}
	}
}
