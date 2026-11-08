package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
	"github.com/jackc/pgx/v5/pgxpool"
)

const childFailureEscalationAfter = 3 * time.Minute

// personWorkNoun mirrors the frontend role: origin takes precedence over parent links.
func personWorkNoun(row *db.SessionRow) string {
	if row.CronID != nil || row.StartedByKind == "cron" {
		return "Run"
	}
	switch row.StartedByKind {
	case "session", "agent", "runner_key":
		return "Worker"
	default:
		if row.ParentSessionID == nil {
			return "Workspace"
		}
		return "Channel"
	}
}

// Work outcomes come from the durable finalization journal, never from exit code.
// An absent receipt is unknown, not proof that commits were delivered or lost.
func failureWork(ctx context.Context, pool *pgxpool.Pool, row *db.SessionRow) (string, bool) {
	if clusterPodInterrupted(row) {
		history, err := db.GetSessionNativeHistory(ctx, pool, row.ID)
		if err != nil {
			failurelog.Default.Event("runner:pod:retention", slog.LevelError, "component", "runner", "operation", "pod_termination", "stage", "retention", "session_id", row.ID, "cause", failurelog.Cause(err))
			return "Retained workspace status is unavailable. Check its recovery record before resuming.", false
		}
		recovery := "No retained workspace volume is recorded; uncommitted files may be lost."
		retained := history != nil && history.DetachedAt == nil && history.ReclaimedAt == nil
		if retained {
			recovery = "A retained workspace volume may hold uncommitted files and unpublished commits; resume restores that workspace."
		}
		return recovery + " Resume with: blerg-runner session send " + row.ID + " \"Resume and inspect retained work before continuing.\"", retained
	}
	w, err := db.GetSessionWorkspace(ctx, pool, row.ID)
	if err != nil {
		return "Work delivery is unconfirmed. Check its work.", false
	}
	if w != nil && w.Manifest != nil {
		if len(w.Results) == 0 {
			if problem := (&API{dbPool: pool}).lastTurnSyncProblem(ctx, row.ID); problem != "" {
				return "Work delivery failed: " + quotedLine(problem, 160) + ". Check its workspace recovery record.", true
			}
			return "Work delivery is unconfirmed. Check its workspace.", false
		}
		var parts []string
		retained := false
		for _, r := range w.Results {
			switch {
			// A live cluster publishes its checkout here before clone. Only
			// a stopped/paused runtime or an explicit delivery error makes it retention.
			case r.RecoveryLocation != "" && (row.EndedAt != nil || row.Status == "error" || row.PausedReason != "" || r.SyncError != "" || r.PushError != ""):
				retained = true
				parts = append(parts, "Work retained at "+quotedLine(r.RecoveryLocation, 240)+".")
			case r.SyncError != "" || r.PushError != "" || (r.Pending && row.EndedAt != nil):
				retained = true
				parts = append(parts, "Work delivery failed for "+quotedName(r.MemberID)+". Check its workspace recovery record.")
			case r.Pending:
				parts = append(parts, "Work delivery is pending.")
			case r.Branch != "":
				parts = append(parts, "Commits delivered to "+quotedLine(r.Branch, 120)+".")
			default:
				parts = append(parts, "Work delivery is unconfirmed.")
			}
		}
		return strings.Join(parts, " "), retained
	}
	if problem := (&API{dbPool: pool}).lastTurnSyncProblem(ctx, row.ID); problem != "" {
		return "Work delivery failed: " + quotedLine(problem, 160) + ". Check its recovery record.", true
	}
	if derefOrEmpty(row.Runtime) == "daemon" {
		return "Work remains in its working directory.", false
	}
	return "Work delivery is unconfirmed. Check " + workbranch.Effective(row.ID, row.WorkBranch) + " and its recovery record.", false
}

// recordSessionFailure is independent of a live coordinator. Its durable notice
// is never input to the stopped session. The occurrence fences duplicate reports.
func recordSessionFailure(ctx context.Context, h *Hub, pool *pgxpool.Pool, id string) error {
	if pool == nil {
		return nil
	}
	previous, err := db.GetSession(ctx, pool, id)
	if err != nil || previous == nil {
		return err
	}
	work, retained := failureWork(ctx, pool, previous)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Classify under the session lock; a recovery racing a failure writer
	// must not create a new stale alert after recovery has settled it.
	if _, err = tx.Exec(ctx, `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, id); err != nil {
		return err
	}
	row, err := db.GetSession(ctx, tx, id)
	if err != nil || row == nil {
		return err
	}
	if row.ArchivedAt != nil {
		return nil
	}
	reason := derefOrEmpty(row.EndReason)
	abnormal := row.PausedReason != "" || row.Status == "error" || clusterPodInterrupted(row)
	if row.EndedAt != nil {
		switch reason {
		case db.EndReasonStoppedByUser, db.EndReasonStoppedByAgent, db.EndReasonAutoStopped, db.EndReasonJobFinished, db.EndReasonProcessExited:
		default:
			abnormal = true
		}
	}
	if reason == db.EndReasonParentEnded && row.ParentSessionID != nil && !retained {
		parent, err := db.GetSession(ctx, tx, *row.ParentSessionID)
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
	turn, err := db.LatestUserMessageSeq(ctx, tx, id)
	if err != nil {
		return err
	}
	detail := derefOrEmpty(row.ErrorReason)
	if row.PausedReason != "" {
		detail = "paused after a restart"
		var saved string
		_ = tx.QueryRow(ctx, `SELECT COALESCE(paused_detail,'') FROM sessions WHERE id=$1`, id).Scan(&saved)
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
	body := fmt.Sprintf("%s %s %s: %s. %s Check its work.", personWorkNoun(row), quotedName(childTitle(row)), state, quotedLine(detail, 160), work)
	// Public conversations can expose updates. Keep pod diagnostics behind the
	// existing owner-authorized result projection, just like start stages.
	if clusterPodInterrupted(row) {
		body = sessionStoppedNotice + " Read its session result for the cause, retained workspace status, and recovery command."
	}
	if row.PausedReason != "" {
		body = fmt.Sprintf("%s %s paused: %s. %s Check its work before resuming.", personWorkNoun(row), quotedName(childTitle(row)), quotedLine(detail, 160), work)
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_failure_alerts WHERE session_id=$1 AND recovered_at IS NULL)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	occurrence := fmt.Sprintf("failure:%d:%d", turn, time.Now().UnixNano())
	// Reopen a recently resolved alert quietly. A sleep/reconnect storm must
	// not fan out another push for every workspace on every cycle.
	var reuse string
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT f.message_id::text FROM session_failure_alerts f JOIN messages m ON m.id=f.message_id WHERE f.session_id=$1 AND m.status='withdrawn' AND m.created_at>now()-interval '24 hours' ORDER BY f.created_at DESC LIMIT 1),'')`, id).Scan(&reuse); err != nil {
		return err
	}
	if reuse != "" {
		if _, err = tx.Exec(ctx, `UPDATE messages SET kind='update',owner_only=false,no_wait=false,status='answered',body=$2,answer=NULL,answered_at=NULL WHERE id=$1`, reuse, body); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE session_failure_alerts SET occurrence=$2,turn_seq=$3,created_at=now(),recovered_at=NULL,parent_notified=false,escalated=false,person_notified=false,quiet=true,notice_event_id=gen_random_uuid() WHERE message_id=$1`, reuse, occurrence, turn); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		msg, e := db.GetMessage(ctx, pool, reuse)
		if e != nil {
			return e
		}
		if msg != nil {
			broadcastFailureAlert(h, pool, row, *msg, true) //nolint:contextcheck // durable resolution committed; browser delivery is independent
		}
		return nil
	}
	var mid string
	if err = tx.QueryRow(ctx, `INSERT INTO messages(session_id,kind,body,status,owner_only,no_wait) VALUES($1,'update',$2,'answered',false,false) RETURNING id`, id, body).Scan(&mid); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO session_failure_alerts(session_id,occurrence,message_id,parent_session_id,turn_seq) VALUES($1,$2,$3,$4,$5)`, id, occurrence, mid, failureParent(row), turn); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	msg, err := db.GetMessage(ctx, pool, mid)
	if err != nil {
		return err
	}
	status := broadcastFailureAlert(h, pool, row, *msg) //nolint:contextcheck // browser and push delivery outlive the request; the durable outbox retries after interruption
	if status == pushRetry {
		return nil
	}
	_, err = pool.Exec(ctx, `UPDATE session_failure_alerts SET person_notified=true WHERE message_id=$1`, mid)
	return err
}

func broadcastFailureAlert(h *Hub, pool *pgxpool.Pool, row *db.SessionRow, msg db.MessageRow, quiet ...bool) pushDeliveryStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var eventID string
	if err := pool.QueryRow(ctx, `SELECT notice_event_id::text FROM session_failure_alerts WHERE message_id=$1`, msg.ID).Scan(&eventID); err != nil {
		return pushRetry
	}
	if err := appendServerAgentEvent(ctx, h, pool, row.ID, eventID, "update", messagingPayload{Body: msg.Body, MessageID: msg.ID}); err != nil {
		return pushRetry
	}
	h.BroadcastJSONPerAccount(func(account string) any {
		if row.Private && !ownsSession(account, row) {
			return nil
		}
		info := messageRowToInfo(msg)
		info.Answerable = false
		return protocol.MessageCreated{Type: "message_created", Message: info, Quiet: !ownsSession(account, row) || len(quiet) > 0 && quiet[0]}
	})
	if len(quiet) > 0 && quiet[0] {
		return pushComplete
	}
	// Keep private failure alerts generic, and resolve their persisted recipient.
	if row.Private {
		return SendPushForSession(pool, row.ID, "Blerg needs you", "A "+strings.ToLower(personWorkNoun(row))+" needs your attention.", "")
	}
	return SendPushForSession(pool, row.ID, personWorkNoun(row)+" needs you", msg.Body, "/sessions/"+row.ID)
}

func failureAlertMessage(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	var found bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_failure_alerts WHERE message_id=$1)`, id).Scan(&found)
	return found, err
}

// Reconciliation catches every terminal writer, including starts that failed,
// missing runtimes and restarts. The migration epoch prevents historical floods.
func (a *API) reconcileSessionFailures(ctx context.Context, now time.Time) error {
	if err := resolveSessionFailureAlerts(ctx, a.hub, a.dbPool, ""); err != nil {
		return err
	}
	rows, err := a.dbPool.Query(ctx, `SELECT id::text FROM sessions WHERE archived_at IS NULL AND
 (ended_at >= (SELECT since FROM session_failure_alert_epoch) OR ((paused_reason<>'' OR status='error' OR (runtime='cluster' AND status='disconnected' AND error_reason LIKE 'stopped:%')) AND COALESCE(status_changed_at,started_at) >= (SELECT since FROM session_failure_alert_epoch))
 OR EXISTS(SELECT 1 FROM session_workspaces w, jsonb_array_elements(w.results) r WHERE w.session_id=sessions.id AND ((r->>'pending'='true' AND sessions.ended_at IS NOT NULL) OR COALESCE(r->>'sync_error','')<>'' OR COALESCE(r->>'push_error','')<>''))
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
	rows, err := a.dbPool.Query(ctx, `SELECT f.session_id::text,f.occurrence,f.message_id::text,f.parent_session_id::text,f.created_at,f.parent_notified,f.quiet
 FROM session_failure_alerts f JOIN messages m ON m.id=f.message_id JOIN sessions s ON s.id=f.session_id
 WHERE (NOT f.escalated OR NOT f.parent_notified) AND f.parent_session_id IS NOT NULL AND f.recovered_at IS NULL AND m.kind='update' AND m.status='answered' AND s.archived_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=f.session_id AND e.kind='user_message' AND e.seq>f.turn_seq)
 AND NOT EXISTS(SELECT 1 FROM child_result_reads r WHERE r.parent_session_id=f.parent_session_id AND r.child_session_id=f.session_id AND r.read_at>=f.created_at)`)
	if err != nil {
		return err
	}
	type pending struct {
		id, key, mid, parent string
		at                   time.Time
		notified, quiet      bool
	}
	var items []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.key, &p.mid, &p.parent, &p.at, &p.notified, &p.quiet); err != nil {
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
		label := "Main"
		if parent != nil && personWorkNoun(parent) != "Workspace" {
			label = childTitle(parent)
		}
		prefix := fmt.Sprintf("%s is not responding; %s failed. ", quotedName(label), quotedName(childTitle(child)))
		// Claim escalation and update the existing notice atomically.
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
			broadcastFailureAlert(a.hub, a.dbPool, child, *m, p.quiet) //nolint:contextcheck // browser and push delivery have independent bounded contexts
			if _, err := a.dbPool.Exec(ctx, `UPDATE session_failure_alerts SET person_notified=true WHERE message_id=$1`, m.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// The database is also the notification outbox. A crash after committing a
// notice but before its toast/push is retried at the next reconciliation.
func (a *API) deliverPendingFailureAlerts(ctx context.Context) error {
	rows, err := a.dbPool.Query(ctx, `SELECT f.session_id::text, f.message_id::text, f.quiet FROM session_failure_alerts f JOIN messages m ON m.id=f.message_id WHERE NOT f.person_notified AND f.recovered_at IS NULL AND m.kind='update' AND m.status='answered'`)
	if err != nil {
		return err
	}
	type delivery struct {
		sid, mid string
		quiet    bool
	}
	var pending []delivery
	for rows.Next() {
		var d delivery
		if err = rows.Scan(&d.sid, &d.mid, &d.quiet); err != nil {
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
		status := broadcastFailureAlert(a.hub, a.dbPool, row, *msg, d.quiet) //nolint:contextcheck // browser and push delivery have independent bounded contexts
		if status == pushRetry {
			continue
		}
		if _, err = a.dbPool.Exec(ctx, `UPDATE session_failure_alerts SET person_notified=true WHERE message_id=$1`, d.mid); err != nil {
			return err
		}
	}
	return nil
}

// Person-started chats report failures to their owner, without relaying content to Main.
func failureParent(row *db.SessionRow) *string {
	if row.DelegationVia == "person" {
		return nil
	}
	return row.ParentSessionID
}
