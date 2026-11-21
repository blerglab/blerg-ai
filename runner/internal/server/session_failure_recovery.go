package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reconcile old alerts too, including sessions omitted by the failure epoch and
// archived sessions. The message close and History mark share one transaction.
func resolveSessionFailureAlerts(ctx context.Context, h *Hub, pool *pgxpool.Pool, only string) error {
	if pool == nil {
		return nil
	}
	rows, err := pool.Query(ctx, `SELECT DISTINCT f.session_id::text FROM session_failure_alerts f JOIN messages m ON m.id=f.message_id WHERE f.recovered_at IS NULL AND ($1='' OR f.session_id::text=$1)`, only)
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
	for _, id := range ids {
		if err = resolveSessionFailure(ctx, h, pool, id); err != nil {
			return err
		}
	}
	return nil
}

func resolveSessionFailure(ctx context.Context, h *Hub, pool *pgxpool.Pool, id string) error {
	previous, err := db.GetSession(ctx, pool, id)
	if err != nil || previous == nil {
		return err
	}
	_, retained := failureWork(ctx, pool, previous)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, id); err != nil {
		return err
	}
	row, err := db.GetSession(ctx, tx, id)
	if err != nil || row == nil {
		return err
	}
	discarded := row.ArchivedAt != nil
	recovered := row.EndedAt == nil && row.PausedReason == "" && (row.Status == "running" || row.Status == "idle" || row.Status == "waiting")
	rows, err := tx.Query(ctx, `SELECT f.message_id::text,f.turn_seq,f.occurrence,m.status FROM session_failure_alerts f JOIN messages m ON m.id=f.message_id WHERE f.session_id=$1 AND f.recovered_at IS NULL ORDER BY f.created_at,f.message_id`, id)
	if err != nil {
		return err
	}
	type alert struct {
		id                 string
		turn               int64
		occurrence, status string
	}
	var alerts []alert
	for rows.Next() {
		var n alert
		if err = rows.Scan(&n.id, &n.turn, &n.occurrence, &n.status); err != nil {
			break
		}
		alerts = append(alerts, n)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	type notice struct {
		mid, eid string
		raw      []byte
		seq      int64
		at       time.Time
	}
	var notices []notice
	seen := map[int64]bool{}
	for _, n := range alerts {
		text := ""
		switch {
		case discarded:
			text = "Session discarded; its failure alert was resolved."
		case !retained && recovered:
			text = "Session recovered; its failure alert was resolved."
		case seen[n.turn]:
			text = "Duplicate session failure alert resolved."
		}
		seen[n.turn] = true
		if text == "" {
			continue
		}
		if discarded || !retained && recovered {
			if _, e := tx.Exec(ctx, `UPDATE session_failure_alerts SET recovered_at=now() WHERE message_id=$1`, n.id); e != nil {
				return e
			}
		}
		if n.status == "withdrawn" {
			continue
		}
		tag, e := tx.Exec(ctx, `UPDATE messages SET status='withdrawn',answer=NULL,answered_at=NULL WHERE id=$1 AND status<>'withdrawn'`, n.id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		eid := messageEventID(n.id, "failure-resolved:"+n.occurrence)
		raw, e := json.Marshal(agent.MarkPayload{ID: eid, Kind: "milestone", By: "agent", PostedBy: "agent", Text: text})
		if e != nil {
			return e
		}
		at := time.Now()
		seq, _, e := db.AppendAgentEventTx(ctx, tx, id, eid, "mark", string(raw), at)
		if e != nil {
			return e
		}
		notices = append(notices, notice{n.id, eid, raw, seq, at})
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for _, n := range notices {
		fanOutServerEvent(h, id, n.eid, "mark", n.raw, n.seq, n.at)
		m, e := db.GetMessage(ctx, pool, n.mid)
		if e != nil {
			return e
		}
		if m == nil {
			continue
		}
		h.SignalAskAnswered(n.mid)
		h.BroadcastJSONPerAccount(func(account string) any { //nolint:contextcheck // browser delivery has an independent bounded context after the durable resolution
			if row.Private && !ownsSession(account, row) {
				return nil
			}
			return protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*m)}
		})
	}
	return nil
}
