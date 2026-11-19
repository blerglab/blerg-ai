package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Closing an ordinary worker ask never hands its contents to the person. Persist
// the plain outcome with the closure, then wake both HTTP and native waiters.
func (a *API) endWorkerQuestion(ctx context.Context, child *db.SessionRow, id, reason string) error {
	return withLimitResumeActionLock(ctx, a.dbPool, child.ID, func(_ *pgxpool.Conn) error {
		tx, err := a.dbPool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		tag, err := tx.Exec(ctx, `UPDATE messages SET status='withdrawn',answer=NULL,answered_at=NULL,child_parent_notified=true WHERE id=$1 AND status='open' AND NOT child_escalated AND NOT person_required AND NOT (owner_only AND NOT no_wait)`, id)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		body := fmt.Sprintf("Worker %s stopped waiting for Main: %s. No answer was given. Do not proceed with work that needs this answer.", quotedName(childTitle(child)), reason)
		raw, err := json.Marshal(messagingPayload{Body: body, MessageID: id})
		if err != nil {
			return err
		}
		eid, at := messageEventID(id, "main-unavailable"), time.Now()
		seq, fresh, err := db.AppendAgentEventTx(ctx, tx, child.ID, eid, "update", string(raw), at)
		if err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		a.waiters.signal(id)
		a.hub.SignalAskAnswered(id)
		row, err := db.GetMessage(ctx, a.dbPool, id)
		if err != nil {
			return err
		}
		if row != nil {
			a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)}) //nolint:contextcheck // scoped browser delivery outlives request
		}
		if fresh {
			fanOutServerEvent(a.hub, child.ID, eid, "update", raw, seq, at)
		}
		return nil
	})
}
