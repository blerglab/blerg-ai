package server

import (
	"context"
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Notes use the same durable delivery flag, outbox and wake budgets as questions.
// Only fixed metadata reaches Main; note bodies remain in explicit result/event reads.
func (a *API) reconcileChildNotes(ctx context.Context) error {
	rows, err := a.dbPool.Query(ctx, `SELECT m.id::text FROM messages m JOIN sessions s ON s.id=m.session_id
 WHERE m.kind='note' AND NOT m.child_parent_notified AND s.parent_session_id IS NOT NULL
 AND COALESCE(s.delegation_via,'') <> 'person' AND s.archived_at IS NULL
 ORDER BY m.created_at,m.id LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var deliveryErr error
	for _, id := range ids {
		deliveryErr = errors.Join(deliveryErr, a.deliverChildNote(ctx, id))
	}
	return deliveryErr
}

func (a *API) deliverChildNote(ctx context.Context, id string) error {
	m, err := db.GetMessage(ctx, a.dbPool, id)
	if err != nil || m == nil {
		return err
	}
	child, err := db.GetSession(ctx, a.dbPool, m.SessionID)
	if err != nil || child == nil {
		return err
	}
	parent, err := db.GetSession(ctx, a.dbPool, derefOrEmpty(child.ParentSessionID))
	if err != nil {
		return err
	}
	if parent == nil || parent.ArchivedAt != nil || parentEnded(parent) {
		_, err := a.dbPool.Exec(ctx, `UPDATE messages SET child_parent_notified=true WHERE id=$1`, id)
		return err
	}
	return a.notifyChildQuestionParent(ctx, child, parent, m)
}
