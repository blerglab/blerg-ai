package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The caller snapshots open questions before stop and holds the child's input
// fence. Stop's empty automatic closure is not an answer; a real reply or a
// dismissal still wins. Keep the original session so private questions cannot
// be copied into a public parent. No migration is needed: status is plain text.
func (a *API) withdrawDiscardQuestions(ctx context.Context, child string, ids []string) error {
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, err := tx.Query(ctx, `UPDATE messages SET status='withdrawn', answer=NULL, answered_at=NULL,child_parent_notified=true
 WHERE session_id=$1 AND kind='ask' AND NOT dismissed
 AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=messages.id)
 AND (status='open' OR (id::text=ANY($2::text[]) AND answer=''))
 RETURNING id::text`, child, ids)
	if err != nil {
		return err
	}
	var withdrawn []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		withdrawn = append(withdrawn, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(messagingPayload{Body: fmt.Sprintf("Session discarded: %d unanswered question(s) withdrawn. No answer was given.", len(withdrawn))})
	if err != nil {
		return err
	}
	eid, at := messageEventID(child, "discard-unanswered-questions"), time.Now()
	var seq int64
	var fresh bool
	if len(withdrawn) > 0 {
		seq, fresh, err = db.AppendAgentEventTx(ctx, tx, child, eid, "update", string(raw), at)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for _, id := range withdrawn {
		row, readErr := db.GetMessage(ctx, a.dbPool, id)
		if readErr != nil {
			return readErr
		}
		if row == nil {
			return fmt.Errorf("withdrawn question missing")
		}
		a.waiters.signal(id)
		a.hub.SignalAskAnswered(id)
		a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)}) //nolint:contextcheck // browser notification outlives its request
	}
	if fresh {
		fanOutServerEvent(a.hub, child, eid, "update", raw, seq, at)
	}
	if len(withdrawn) > 0 {
		a.noteChildTurn(ctx, child, childDiscardUnanswered, fmt.Sprintf("%d unanswered question(s) were withdrawn; nobody answered them", len(withdrawn)))
	}
	return nil
}
