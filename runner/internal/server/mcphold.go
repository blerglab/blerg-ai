package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// handleReleaseHeldPrompt only releases an existing engine's wait. It cannot start or resume
// a session. Public visibility never confers permission to control another person's first prompt.
func handleReleaseHeldPrompt(ctx context.Context, h *Hub, bc *BrowserConn, pool *pgxpool.Pool, msg protocol.ReleaseHeldPrompt) *APIError {
	if pool == nil {
		return &APIError{Status: http.StatusServiceUnavailable, Message: "Session ownership is unavailable."}
	}
	if !validSessionID(msg.SessionID) {
		return &APIError{Status: http.StatusNotFound, Message: "Session not found."}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	row, err := db.GetSession(ctx, pool, msg.SessionID)
	if err != nil {
		return &APIError{Status: http.StatusServiceUnavailable, Message: "Session ownership is unavailable."}
	}
	if row == nil {
		return &APIError{Status: http.StatusNotFound, Message: "Session not found."}
	}
	if !ownsSession(bc.AccountID, row) {
		return &APIError{Status: http.StatusForbidden, Message: "Only the session owner can release its first prompt."}
	}
	switch row.Status {
	case "ended", "stopped", "error":
		return &APIError{Status: http.StatusConflict, Message: "Session has no live engine."}
	}
	dc := h.FindDaemonForSession(msg.SessionID)
	if dc == nil || (dc.BoundSessionID != "" && dc.BoundSessionID != msg.SessionID) {
		return &APIError{Status: http.StatusConflict, Message: "Session has no live daemon."}
	}
	raw, _ := json.Marshal(protocol.ReleaseHeldPrompt{Type: "release_held_prompt", SessionID: msg.SessionID})
	// The write pump also checks the current cluster runtime claim under its launch fence.
	if !h.trySendToDaemon(dc, raw) {
		return &APIError{Status: http.StatusConflict, Message: "Session daemon is unavailable."}
	}
	return nil
}
