package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// Each browser reconciles its own persisted scope, including changes committed
// by another replica. No foreign inventory is read or included in the hint.
func (h *Hub) desktopScopeRows(ctx context.Context, account string) (string, error) {
	if h.desktopPool == nil {
		return "", nil
	}
	rows, err := db.ListDesktops(ctx, h.desktopPool, account, false)
	raw, _ := json.Marshal(rows)
	return string(raw), err
}
func (h *Hub) refreshDesktopScope(ctx context.Context, bc *BrowserConn, pool *pgxpool.Pool, previous string) string {
	scope, err := h.desktopScopeRows(ctx, bc.AccountID)
	if err != nil || scope == previous {
		return previous
	}
	select {
	case bc.send <- []byte(`{"type":"desktop_inventory_invalidated"}`):
	case <-ctx.Done():
		return previous
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
