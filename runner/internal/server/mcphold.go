package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// handleReleaseHeldPrompt only releases an existing engine's wait. It cannot start or resume
// a session. Public visibility never confers permission to control another person's first prompt.
func handleReleaseHeldPrompt(ctx context.Context, h *Hub, bc *BrowserConn, pool *pgxpool.Pool, msg protocol.ReleaseHeldPrompt) *APIError {
	if pool == nil || !validSessionID(msg.SessionID) {
		return handleReleaseHeldPromptUnlocked(ctx, h, bc, pool, msg)
	}
	row, err := db.GetSession(ctx, pool, msg.SessionID)
	if err != nil {
		return apiErrorf(503, "Session ownership is unavailable.")
	}
	if row == nil {
		return apiErrorf(404, "Session not found.")
	}
	if !ownsSession(bc.AccountID, row) {
		return apiErrorf(403, "Only the session owner can release its first prompt.")
	}
	var problem *APIError
	err = withCancelledLimitResume(ctx, pool, msg.SessionID, func() error {
		return withParentInputGuard(ctx, pool, msg.SessionID, func() error { problem = handleReleaseHeldPromptUnlocked(ctx, h, bc, pool, msg); return nil })
	})
	if errors.Is(err, db.ErrDelegationEnded) {
		return apiErrorf(409, "session was discarded or its parent ended")
	}
	if err != nil {
		return apiErrorf(503, "session control is busy; retry shortly")
	}
	return problem
}

func handleReleaseHeldPromptUnlocked(ctx context.Context, h *Hub, bc *BrowserConn, pool *pgxpool.Pool, msg protocol.ReleaseHeldPrompt) *APIError {
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
