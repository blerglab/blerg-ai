package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livepoll"
)

func boundSessionEnded(s *db.SessionRow) bool {
	return s.Status == "ended" || s.Status == "stopped" || s.EndedAt != nil
}

// RunBoundAskReconciliation delivers durable lifecycle cancellations after the
// session transaction has committed. Epochs survive restart and end/resume or
// transfer/back transitions. Answer-time locking enforces refusal immediately;
// delivery may lag while a question is locked, without delaying session end.
func (a *API) RunBoundAskReconciliation(ctx context.Context) {
	if a.dbPool == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		batch, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := (livepoll.Attempts{Pool: a.dbPool}).Maintain(batch)
		if err == nil {
			_, err = a.dbPool.Exec(batch, `UPDATE messages SET status='answered',answer='',answered_at=clock_timestamp() WHERE id IN (SELECT message_id FROM bound_asks WHERE status='open' AND expires_at<=clock_timestamp() AND subject->>'kind'='live_binding')`)
		}
		if err == nil {
			err = a.reconcileBoundAskLifecycle(batch)
		}
		if err == nil {
			err = a.reconcileLiveDecisions(batch)
		}
		cancel()
		if err != nil {
			failurelog.Default.Event("bound_ask_reconcile", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *API) reconcileBoundAskLifecycle(ctx context.Context) error {
	rows, err := a.dbPool.Query(ctx, `SELECT b.message_id::text FROM bound_asks b JOIN sessions s ON s.id=b.workspace_id
 WHERE b.status='open' AND (b.session_epoch<>s.bound_ask_epoch OR s.status IN ('ended','stopped') OR s.ended_at IS NOT NULL OR s.archived_at IS NOT NULL OR s.discard_requested_at IS NOT NULL)
 ORDER BY b.message_id LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = a.reconcileBoundAskCancellation(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (a *API) reconcileBoundAskCancellation(ctx context.Context, id string) error {
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var open bool
	if err = tx.QueryRow(ctx, `SELECT status='open' FROM bound_asks WHERE message_id=$1 FOR UPDATE`, id).Scan(&open); err != nil {
		return err
	}
	if !open {
		return nil
	}
	if err = a.recordBoundCancellation(ctx, tx, id, "session_invalidated"); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	failurelog.Default.Event("bound_ask_canceled", slog.LevelInfo, "component", "runner", "cause", "session_invalidated")
	a.deliverBoundCancellation(ctx, id)
	return nil
}
