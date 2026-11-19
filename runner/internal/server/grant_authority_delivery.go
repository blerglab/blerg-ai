package server

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Serialize the nonblocking enqueue with transition replacement using the
// publisher advisory lock, never a session row lock. A replay snapshot may
// contain a suspension that recovery has already superseded in the outbox.
func sendGrantAuthorityNotice(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, msg protocol.AgentUserMessage, raw []byte) (bool, error) {
	// Plan preparation can perform slow DB/core work. Finish it before taking
	// even the narrow publisher fence; then recheck the durable notice below.
	raw, finish := h.prepareDaemonInput(ctx, dc, raw)
	sent := false
	if finish != nil {
		defer func() { finish(sent) }()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "grant-authority:"+msg.SessionID); err != nil {
		return false, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_inputs WHERE session_id=$1 AND client_id=$2 AND settled_at IS NULL)`, msg.SessionID, msg.ClientID).Scan(&pending); err != nil {
		return false, err
	}
	if !pending {
		return true, tx.Commit(ctx)
	}
	sent = h.enqueueToDaemon(dc, raw)
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return sent, nil
}
