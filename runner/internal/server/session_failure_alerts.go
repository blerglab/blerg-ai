package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

const childFailureEscalationAfter = 3 * time.Minute

// Work outcomes come from the durable finalization journal, never from exit code.
// An absent receipt is unknown, not proof that commits were delivered or lost.
func failureWork(ctx context.Context, pool *pgxpool.Pool, row *db.SessionRow) (string, bool) {
	w, err := db.GetSessionWorkspace(ctx, pool, row.ID)
	if err != nil {
		return "Work delivery is unconfirmed. Check the session's work.", false
	}
	if w != nil && w.Manifest != nil {
		if len(w.Results) == 0 {
			if problem := (&API{dbPool: pool}).lastTurnSyncProblem(ctx, row.ID); problem != "" {
				return "Work delivery failed: " + quotedLine(problem, 160) + ". Check the session's workspace recovery record.", true
			}
			return "Work delivery is unconfirmed. Check the session's workspace.", false
		}
		var parts []string
		retained := false
		for _, r := range w.Results {
			switch {
			case r.RecoveryLocation != "":
				retained = true
				parts = append(parts, "Work retained at "+quotedLine(r.RecoveryLocation, 240)+".")
			case r.Pending || r.SyncError != "" || r.PushError != "":
				retained = true
				parts = append(parts, "Work delivery failed for "+quotedName(r.MemberID)+". Check its workspace recovery record.")
			case r.Branch != "":
				parts = append(parts, "Commits delivered to "+quotedLine(r.Branch, 120)+".")
			default:
				parts = append(parts, "Work delivery is unconfirmed.")
			}
		}
		return strings.Join(parts, " "), retained
	}
	if problem := (&API{dbPool: pool}).lastTurnSyncProblem(ctx, row.ID); problem != "" {
		return "Work delivery failed: " + quotedLine(problem, 160) + ". Check the session's recovery record.", true
	}
	if derefOrEmpty(row.Runtime) == "daemon" {
		return "Work remains in the session's working directory.", false
	}
	return "Work delivery is unconfirmed. Check wip/" + row.ID + " and the session's recovery record.", false
}

// recordSessionFailure is independent of a live parent. Its durable owner-only
// question uses the existing Needs-you and dismiss contracts; it is never input
// to the stopped session. The unique occurrence also fences duplicate reports.
func recordSessionFailure(ctx context.Context, h *Hub, pool *pgxpool.Pool, id string) error {
	if pool == nil {
		return nil
	}
	row, err := db.GetSession(ctx, pool, id)
	if err != nil || row == nil {
		return err
	}
	work, retained := failureWork(ctx, pool, row)
	reason := derefOrEmpty(row.EndReason)
	abnormal := row.PausedReason != "" || row.Status == "error"
	if row.EndedAt != nil {
		switch reason {
		case db.EndReasonStoppedByUser, db.EndReasonStoppedByAgent, db.EndReasonAutoStopped, db.EndReasonJobFinished, db.EndReasonProcessExited:
		default:
			abnormal = true
		}
	}
	if reason == db.EndReasonParentEnded && row.ParentSessionID != nil && !retained {
		parent, err := db.GetSession(ctx, pool, *row.ParentSessionID)
		if err != nil {
			return err
		}
		if parent != nil && derefOrEmpty(parent.EndReason) == db.EndReasonStoppedByUser {
			return nil
		}
	}
	if reason == db.EndReasonProcessExited && derefOrEmpty(row.ErrorReason) != "" {
		abnormal = true
	}
	if !abnormal && !retained {
		return nil
	}
	if reason == db.EndReasonStoppedByUser && !retained {
		return nil
	}
	turn, err := db.LatestUserMessageSeq(ctx, pool, id)
	if err != nil {
		return err
	}
	occurrence := fmt.Sprintf("%s:%d", row.PausedReason, turn)
	if row.EndedAt != nil {
		occurrence = fmt.Sprintf("ended:%d", turn)
	} else if row.Status == "error" {
		occurrence = fmt.Sprintf("error:%d", turn)
	}
	detail := derefOrEmpty(row.ErrorReason)
	if row.PausedReason != "" {
		detail = "paused after a restart"
		var saved string
		_ = pool.QueryRow(ctx, `SELECT COALESCE(paused_detail,'') FROM sessions WHERE id=$1`, id).Scan(&saved)
		if saved != "" {
			detail += ": " + saved
		}
	}
	if detail == "" {
		detail = strings.ReplaceAll(reason, "_", " ")
	}
	if detail == "" {
		detail = "work needs review"
	}
	state := "needs attention"
	if row.EndedAt != nil {
		state = "stopped"
	} else if row.Status == "error" {
		state = "failed"
	}
	body := fmt.Sprintf("Session %s %s: %s. %s Check its work, then dismiss this alert.", quotedName(childTitle(row)), state, quotedLine(detail, 160), work)
	if row.PausedReason != "" {
		body = fmt.Sprintf("Session %s paused: %s. %s Check its work before resuming, then dismiss this alert.", quotedName(childTitle(row)), quotedLine(detail, 160), work)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is harmless
	// Lock only this session, so concurrent terminal effects cannot create orphan asks.
	if _, err = tx.Exec(ctx, `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, id); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_failure_alerts WHERE session_id=$1 AND occurrence=$2)`, id, occurrence).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var mid string
	if err = tx.QueryRow(ctx, `INSERT INTO messages(session_id,kind,body,status,owner_only,no_wait) VALUES($1,'ask',$2,'open',true,true) RETURNING id`, id, body).Scan(&mid); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO session_failure_alerts(session_id,occurrence,message_id,parent_session_id,turn_seq) VALUES($1,$2,$3,$4,$5)`, id, occurrence, mid, row.ParentSessionID, turn); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	msg, err := db.GetMessage(ctx, pool, mid)
	if err != nil {
		return err
	}
	broadcastFailureAlert(h, pool, row, *msg) //nolint:contextcheck // browser and push delivery outlive the request; the durable outbox retries after interruption
	_, err = pool.Exec(ctx, `UPDATE session_failure_alerts SET person_notified=true WHERE message_id=$1`, mid)
	return err
}

func broadcastFailureAlert(h *Hub, pool *pgxpool.Pool, row *db.SessionRow, msg db.MessageRow) {
	h.BroadcastJSONPerAccount(func(account string) any {
		if row.Private && !ownsSession(account, row) {
			return nil
		}
		info := messageRowToInfo(msg)
		info.Answerable = account != "" && account == derefOrEmpty(row.SpawningAccountID)
		return protocol.MessageCreated{Type: "message_created", Message: info, Quiet: !info.Answerable}
	})
	// Push subscriptions currently have no owner. Keep the existing privacy boundary:
	// private sessions get a generic push with no session title, reason or location.
	if row.Private {
		SendPush(pool, "Blerg needs you", "A session needs your attention.", "")
	} else {
		SendPush(pool, "Session needs you", msg.Body, "/sessions/"+row.ID)
	}
}

func failureAlertMessage(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	var found bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_failure_alerts WHERE message_id=$1)`, id).Scan(&found)
	return found, err
}

// Reconciliation catches every terminal writer, including starts that failed,
// missing runtimes and restarts. The migration epoch prevents historical floods.
func (a *API) reconcileSessionFailures(ctx context.Context, now time.Time) error {
	rows, err := a.dbPool.Query(ctx, `SELECT id::text FROM sessions WHERE archived_at IS NULL AND
 (ended_at >= (SELECT since FROM session_failure_alert_epoch) OR ((paused_reason<>'' OR status='error') AND status_changed_at >= (SELECT since FROM session_failure_alert_epoch))
 OR EXISTS(SELECT 1 FROM session_workspaces w, jsonb_array_elements(w.results) r WHERE w.session_id=sessions.id AND (r->>'pending'='true' OR COALESCE(r->>'sync_error','')<>'' OR COALESCE(r->>'push_error','')<>''))
 OR EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=sessions.id AND e.kind='error' AND e.payload->>'code'='child_sync' AND e.ts >= (SELECT since FROM session_failure_alert_epoch)))`)
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
		if err = recordSessionFailure(ctx, a.hub, a.dbPool, id); err != nil {
			return err
		}
	}
	if err := a.deliverPendingFailureAlerts(ctx); err != nil {
		return err
	}
	return a.escalateSessionFailures(ctx, now)
}

func (a *API) escalateSessionFailures(ctx context.Context, now time.Time) error {
	rows, err := a.dbPool.Query(ctx, `SELECT f.session_id::text,f.occurrence,f.message_id::text,f.parent_session_id::text,f.created_at,f.parent_notified
 FROM session_failure_alerts f JOIN messages m ON m.id=f.message_id JOIN sessions s ON s.id=f.session_id
 WHERE (NOT f.escalated OR NOT f.parent_notified) AND f.parent_session_id IS NOT NULL AND m.status='open' AND s.archived_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=f.session_id AND e.kind='user_message' AND e.seq>f.turn_seq)
 AND NOT EXISTS(SELECT 1 FROM child_result_reads r WHERE r.parent_session_id=f.parent_session_id AND r.child_session_id=f.session_id AND r.read_at>=f.created_at)`)
	if err != nil {
		return err
	}
	type pending struct {
		id, key, mid, parent string
		at                   time.Time
		notified             bool
	}
	var items []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.key, &p.mid, &p.parent, &p.at, &p.notified); err != nil {
			break
		}
		items = append(items, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range items {
		parent, e := db.GetSession(ctx, a.dbPool, p.parent)
		if e != nil {
			return e
		}
		child, e := db.GetSession(ctx, a.dbPool, p.id)
		if e != nil {
			return e
		}
		if child == nil {
			continue
		}
		if !p.notified && parent != nil && !parentEnded(parent) {
			kind := childSessionFailed
			if child.EndedAt == nil {
				kind = childTurnFailed
			}
			if child.PausedReason != "" {
				kind = childRestartPaused
			}
			sent := a.noteChildTurn(ctx, p.id, kind, derefOrEmpty(child.ErrorReason))
			// Relays have stable client IDs and retry on the next pass if unavailable.
			if sent {
				_, e = a.dbPool.Exec(ctx, `UPDATE session_failure_alerts SET parent_notified=true WHERE session_id=$1 AND occurrence=$2`, p.id, p.key)
				if e != nil {
					return e
				}
			}
		}
		unavailable := parent == nil || parentEnded(parent) || parent.PausedReason != "" || parent.Status == "error" || a.hub.FindDaemonForSession(p.parent) == nil
		if !unavailable && now.Sub(p.at) < childFailureEscalationAfter {
			continue
		}
		label := p.parent
		if parent != nil {
			label = childTitle(parent)
		}
		prefix := fmt.Sprintf("Coordinator %s is not responding; child %s failed. ", quotedName(label), quotedName(childTitle(child)))
		// Claim escalation and update the existing Needs-you item atomically.
		tag, e := a.dbPool.Exec(ctx, `WITH claimed AS (UPDATE session_failure_alerts SET escalated=true, person_notified=false WHERE session_id=$1 AND occurrence=$2 AND NOT escalated RETURNING message_id)
 UPDATE messages SET body=$3||body WHERE id IN (SELECT message_id FROM claimed)`, p.id, p.key, prefix)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		m, e := db.GetMessage(ctx, a.dbPool, p.mid)
		if e != nil {
			return e
		}
		if m != nil {
			broadcastFailureAlert(a.hub, a.dbPool, child, *m) //nolint:contextcheck // browser and push delivery have independent bounded contexts
			if _, err := a.dbPool.Exec(ctx, `UPDATE session_failure_alerts SET person_notified=true WHERE message_id=$1`, m.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// The database is also the notification outbox. A crash after committing a
// Needs-you item but before its toast/push is retried at the next reconciliation.
func (a *API) deliverPendingFailureAlerts(ctx context.Context) error {
	rows, err := a.dbPool.Query(ctx, `SELECT f.session_id::text, f.message_id::text FROM session_failure_alerts f JOIN messages m ON m.id=f.message_id WHERE NOT f.person_notified AND m.status='open'`)
	if err != nil {
		return err
	}
	type delivery struct{ sid, mid string }
	var pending []delivery
	for rows.Next() {
		var d delivery
		if err = rows.Scan(&d.sid, &d.mid); err != nil {
			break
		}
		pending = append(pending, d)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range pending {
		row, err := db.GetSession(ctx, a.dbPool, d.sid)
		if err != nil {
			return err
		}
		msg, err := db.GetMessage(ctx, a.dbPool, d.mid)
		if err != nil {
			return err
		}
		if row == nil || msg == nil {
			continue
		}
		broadcastFailureAlert(a.hub, a.dbPool, row, *msg) //nolint:contextcheck // browser and push delivery have independent bounded contexts
		if _, err = a.dbPool.Exec(ctx, `UPDATE session_failure_alerts SET person_notified=true WHERE message_id=$1`, d.mid); err != nil {
			return err
		}
	}
	return nil
}
