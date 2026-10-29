package server

import (
	"context"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// attachmentCleanup authorizes deletion only from an authenticated desktop's
// bounded candidate list. A failed lookup never means an unknown session.
func attachmentCleanup(ctx context.Context, pool *pgxpool.Pool, daemonID string, req protocol.AttachmentCleanupRequest) protocol.AttachmentCleanupResult {
	result := protocol.AttachmentCleanupResult{Type: "attachment_cleanup_result", RequestID: req.RequestID}
	if pool == nil || daemonID == "" || len(req.SessionIDs) > 128 {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, id := range req.SessionIDs {
		if len(id) == 0 || len(id) > 128 {
			continue
		}
		row, err := db.GetSession(ctx, pool, id)
		if err != nil {
			continue
		}
		if row == nil || (row.DaemonID == daemonID && (db.TerminallyEnded(row) || row.ArchivedAt != nil)) {
			result.Removable = append(result.Removable, id)
		}
	}
	return result
}
