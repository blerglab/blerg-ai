package server

import (
	"context"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (dc *DaemonConn) SetSessionPluginEngines(engines []string) {
	dc.liveMu.Lock()
	defer dc.liveMu.Unlock()
	dc.sessionPluginEngines = nil
	seen := map[string]bool{}
	for _, e := range engines {
		if pluginspec.SupportsEngine(e) && !seen[e] {
			dc.sessionPluginEngines = append(dc.sessionPluginEngines, e)
			seen[e] = true
		}
	}

}
func (dc *DaemonConn) CanSessionPluginsFor(engine string) bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	if engine == "" {
		return len(dc.sessionPluginEngines) > 0
	}
	for _, e := range dc.sessionPluginEngines {
		if e == engine {
			return true
		}
	}
	return false
}

func replaySessionPlugins(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn) {
	if pool == nil || !dc.CanSessionPluginsFor("") || dc.RuntimeTokenID != "" || h.GetDaemon(dc.ID) != dc {
		return
	}
	requests, err := db.QueuedSessionPlugins(ctx, pool, dc.ID)
	if err != nil {
		return
	}
	for i := range requests {
		h.dispatchSessionPlugin(ctx, pool, &requests[i])
	}
}
func handleSessionPluginsResult(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, msg protocol.SessionPluginsResult) {
	if pool == nil || !validSessionID(msg.RequestID) || !validSessionID(msg.SessionID) || dc.RuntimeTokenID != "" {
		return
	}
	state, message := msg.State, ""
	switch state {
	case "queued":
		return
	case "applied":
		message = "Plugin applied; the next turn resumes with the updated plugin snapshot."
	case "failed":
		message = "The plugin could not be applied. The previous plugin snapshot was retained."
	case "unsupported":
		state = "failed"
		message = "This runtime cannot apply plugins to a running session."
	default:
		return
	}
	unlock := h.lockDaemonWork(dc.ID)
	defer unlock()
	if h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(msg.SessionID) != dc {
		return
	}
	p, err := db.SessionPluginProposalForResult(ctx, pool, msg.RequestID)
	if err != nil || p.SessionID != msg.SessionID || p.URLSnapshot != "session-plugins:"+dc.ID {
		return
	}
	row, err := db.GetSession(ctx, pool, msg.SessionID)
	if err != nil || row == nil || row.DaemonID != dc.ID || !ownsSession(p.AccountID, row) {
		return
	}
	// No daemon-provided error text reaches the owner, logs or proposal result.
	_ = db.UpdateSessionPluginResult(ctx, pool, p.AccountID, p.ID, p.SessionID, dc.ID, strings.TrimSpace(state), message)
}
