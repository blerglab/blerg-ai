package server

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
)

type childTurnDelivery struct {
	child, kind string
	turn, event int64
}

// BLG-447's failure rule: a real finish or failure always reaches Main exactly once; a transient
// turn_done followed by a self-started turn produces no notice.
//
// childTurnFinishedGrace is how long a queued 'finished' notice waits, once the child shows no
// open background work, before Main is told the worker actually stopped — long enough for a quick
// resume (production evidence, worker 5a7b00c0: the real engine emitted turn_done with a
// background task still open, then started a fresh "agent turn" on its own 1.2s later, continuing
// the same exchange) to be on the record first. childTurnFinishedCeiling is the circuit breaker:
// a 'finished' notice is held, never dropped — the failure rule requires it eventually reach Main
// — but a stale or wrong background_tasks count (migration 143's counter can lag; the engine may
// never resume; the daemon's own wake fallback, claudestream.go's armWakeFallback, may not fire)
// must not hold it forever. Both are measured from child_turn_notices.queued_at, the row's own
// server-side timestamp (migration 145) — not a daemon-reported event ts, which a fast daemon
// clock could use to delay every notice (BLG-447 review) — and queued_at resets whenever the
// row's content is refreshed (the trigger's UPDATE branch, migration 115), so a replaced report
// earns its own fresh grace window.
const (
	childTurnFinishedGrace   = 5 * time.Second
	childTurnFinishedCeiling = 2 * time.Minute
)

// childTurnFinishedReadiness reports whether the queued notice n is ready to send now (ready),
// and if not, how long until it should be rechecked (wait) and why it is being held (reason, for
// logging). Only a kind=childTurnFinished notice is ever held: 'failed' and every other kind
// (asked, approval, discard_unanswered, …) are never held or suppressed — BLG-447 review: those
// never go through this table's trigger either way, except 'failed', which must still reach Main
// immediately. A held notice is NEVER marked delivered and NEVER dropped: it stays in
// child_turn_notices, so the next real turn_done (migration 115's trigger) updates this SAME row
// in place — replaced, not lost — and this function runs again against the fresh content.
//
// Order matters: the grace window applies unconditionally first, giving a quick resume (the
// production pattern) time to even be recorded. A self-started turn already on the record (:76)
// is held with NO ceiling — the trigger will update this same row once that turn's own real
// completion lands, so forcing delivery here would only ever send stale content (BLG-447 review
// round 2: a self-started turn or background job longer than childTurnFinishedCeiling is exactly
// the case this card is about; the ceiling must never fire a false 'finished' while one is known
// to be running). Open background work (:68) is the one condition the ceiling actually guards —
// a stale or wrong background_tasks count, or a wake that never fires — and that ceiling starts
// only once the worker itself is idle (no running status, no turn in progress), counted from the
// session's own last status_changed:idle, not from queued_at: a worker that is still genuinely
// busy must never be timed out just because it has been busy for a while.
func (a *API) childTurnFinishedReadiness(ctx context.Context, tx pgx.Tx, child *db.SessionRow, n childTurnDelivery) (ready bool, wait time.Duration, reason string, err error) {
	if n.kind != childTurnFinished {
		return true, 0, "", nil
	}
	var queuedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT queued_at FROM child_turn_notices WHERE child_id=$1 AND turn_seq=$2`, n.child, n.turn).Scan(&queuedAt); err != nil {
		return false, 0, "", err
	}
	if elapsed := time.Since(queuedAt); elapsed < childTurnFinishedGrace {
		return false, childTurnFinishedGrace - elapsed, "grace window, watching for a self-started turn", nil
	}
	var resumed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=$1 AND e.seq>$2
	 AND (e.kind='turn_done' OR (e.kind='status_changed' AND e.payload->>'status'='running')))`, n.child, n.event).Scan(&resumed); err != nil {
		return false, 0, "", err
	}
	if resumed {
		// No ceiling: the self-started turn's own completion (migration 115's trigger) will
		// refresh this row in place, with a fresh queued_at and its own fresh grace window.
		// Forcing delivery of THIS stale content would violate the failure rule (exactly once).
		return false, childTurnFinishedCeiling, "a self-started turn is already on the record", nil
	}
	if child.BackgroundTasks > 0 {
		idleSince, idle, err := a.childIdleSince(ctx, tx, n.child, queuedAt)
		if err != nil {
			return false, 0, "", err
		}
		if !idle {
			return false, childTurnFinishedGrace, "open background work, still running", nil
		}
		if remaining := childTurnFinishedCeiling - time.Since(idleSince); remaining > 0 {
			return false, remaining, "open background work", nil
		}
		return true, 0, "ceiling: held idle 2m+ with background work still open; delivering regardless", nil
	}
	return true, 0, "", nil
}

// childIdleSince reports the server-known time child last became idle (idle, true) — its latest
// status_changed event said 'idle' — or that it is not idle right now (idle, false): the latest
// status_changed said 'running', or something else active. No status_changed event at all falls
// back to queuedAt (the held notice's own transient turn_done, which — per the daemon's own
// architecture — is itself an idle point): a harness or test fixture that never emits status
// events must not be treated as perpetually busy.
func (a *API) childIdleSince(ctx context.Context, tx pgx.Tx, child string, queuedAt time.Time) (time.Time, bool, error) {
	var status string
	var ts time.Time
	err := tx.QueryRow(ctx, `SELECT payload->>'status',ts FROM agent_events WHERE session_id=$1 AND kind='status_changed' ORDER BY seq DESC LIMIT 1`, child).Scan(&status, &ts)
	if errors.Is(err, pgx.ErrNoRows) {
		return queuedAt, true, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if status != "idle" {
		return time.Time{}, false, nil
	}
	return ts, true, nil
}

// childTurnRetryTimers holds one scheduled recheck per held child, so repeated holds across
// reconciliation passes never pile up timers for the same notice (BLG-447 review: one timer per
// held notice, not one per reconcile pass).
type childTurnRetryTimers struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
}

func (t *childTurnRetryTimers) schedule(child string, after time.Duration, fire func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.timers == nil {
		t.timers = map[string]*time.Timer{}
	}
	if existing := t.timers[child]; existing != nil {
		existing.Stop()
	}
	t.timers[child] = time.AfterFunc(after, func() {
		t.clear(child)
		fire()
	})
}

// clear drops child's scheduled timer, if any — once it has fired, or once its notice was
// delivered through some other path (the periodic sweep, an earlier retry) that made this one
// moot, so the map does not keep one stale entry per child ever held (BLG-447 review).
func (t *childTurnRetryTimers) clear(child string) {
	t.mu.Lock()
	delete(t.timers, child)
	t.mu.Unlock()
}

// scheduleChildTurnRetry arms (replacing any earlier one for child) exactly one retry of
// reconcileChildTurns, so a quiet period — nothing else nudging reconciliation — still re-checks
// a held notice close to when its own wait suggests, rather than waiting for an unrelated child's
// turn_done or the periodic sweep (jobreconcile.go, 30s).
func (a *API) scheduleChildTurnRetry(child string, after time.Duration) {
	if after <= 0 {
		after = time.Second
	}
	a.childTurnRetry.schedule(child, after+200*time.Millisecond, func() {
		ctx, cancel := context.WithTimeout(context.Background(), childTurnNoteTimeout)
		defer cancel()
		if err := a.reconcileChildTurn(ctx, child); err != nil {
			log.Printf("child turn delivery retry for %s: %v", child, err)
		}
	})
}

func (a *API) childTurnAnswer(ctx context.Context, child string, through int64) (string, string, string, error) {
	rows, err := a.dbPool.Query(ctx, `SELECT kind,payload::text FROM agent_events WHERE session_id=$1 AND seq<=$2 AND kind IN ('assistant_text','turn_done','error') ORDER BY seq DESC LIMIT $3`, child, through, resultAssistantScanLimit)
	if err != nil {
		return "", "", "", err
	}
	defer rows.Close()
	var events []db.AgentEventRow
	for rows.Next() {
		var e db.AgentEventRow
		if err = rows.Scan(&e.Kind, &e.Payload); err != nil {
			return "", "", "", err
		}
		events = append(events, e)
	}
	if err = rows.Err(); err != nil {
		return "", "", "", err
	}
	text, failure := turnAnswer(events)
	return text, failure, turnSyncProblem(events), nil
}

// Lock each intent until the relay is durably accepted. A crash before marking
// it delivered retries the same client ID through the input receipt outbox.
// reconcileChildTurns sweeps every pending notice in the system: the periodic
// backstop (jobreconcile.go, 30s) that also sweeps channel closes.
func (a *API) reconcileChildTurns(ctx context.Context) error {
	if err := a.reconcileChannelCloses(ctx); err != nil {
		return err
	}
	return a.reconcileChildTurnsFor(ctx, "")
}

// reconcileChildTurn reconciles only child's own queued notices. A turn starting, a background
// task ending, or child's own turn_done are routine, high-frequency signals that only matter when
// child already has a held notice; the alternative — reconcileChildTurns' full pass over every
// pending row for every child in the system — does needless work on every one of them for every
// other child's event (BLG-447 review).
func (a *API) reconcileChildTurn(ctx context.Context, child string) error {
	return a.reconcileChildTurnsFor(ctx, child)
}

// reconcileChildTurnsFor is the shared body: onlyChild == "" sweeps every pending row (ordered
// child by child, each blocked independently on its own first unresolved row); a non-empty
// onlyChild scopes both the query and the lock-bounded loop to that one child.
func (a *API) reconcileChildTurnsFor(ctx context.Context, onlyChild string) error {
	a.childTurnDeliveryMu.Lock()
	defer a.childTurnDeliveryMu.Unlock()
	var rows pgx.Rows
	var err error
	if onlyChild == "" {
		rows, err = a.dbPool.Query(ctx, `SELECT child_id::text,kind,turn_seq,event_seq FROM child_turn_notices WHERE NOT delivered ORDER BY child_id,event_seq`)
	} else {
		rows, err = a.dbPool.Query(ctx, `SELECT child_id::text,kind,turn_seq,event_seq FROM child_turn_notices WHERE NOT delivered AND child_id=$1 ORDER BY event_seq`, onlyChild)
	}
	if err != nil {
		return err
	}
	var pending []childTurnDelivery
	for rows.Next() {
		var n childTurnDelivery
		if err = rows.Scan(&n.child, &n.kind, &n.turn, &n.event); err != nil {
			break
		}
		pending = append(pending, n)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	blocked := map[string]bool{}
	for _, n := range pending {
		if blocked[n.child] {
			continue
		}
		tx, err := a.dbPool.Begin(ctx)
		if err != nil {
			return err
		}
		var turn int64
		err = tx.QueryRow(ctx, `SELECT turn_seq,kind,event_seq FROM child_turn_notices WHERE child_id=$1 AND turn_seq=$2 AND NOT delivered FOR UPDATE SKIP LOCKED`, n.child, n.turn).Scan(&turn, &n.kind, &n.event)
		if err != nil {
			_ = tx.Rollback(ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				blocked[n.child] = true
				continue
			}
			return err
		}
		child, err := db.GetSession(ctx, tx, n.child)
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		obsolete := child == nil || child.ArchivedAt != nil || child.ParentSessionID == nil
		if !obsolete {
			parent, e := db.GetSession(ctx, tx, *child.ParentSessionID)
			if e != nil {
				_ = tx.Rollback(ctx)
				return e
			}
			obsolete = parent == nil || parentEnded(parent) || parent.Kind != "agent" || parent.Status == "error"
			if !obsolete && derefOrEmpty(parent.Runtime) == runnerRuntimeCluster && a.hub.FindDaemonForSession(parent.ID) == nil {
				_ = tx.Rollback(ctx)
				continue // retain one coalesced intent until the parent reconnects
			}
		}
		if !obsolete {
			ready, wait, reason, err := a.childTurnFinishedReadiness(ctx, tx, child, n)
			if err != nil {
				_ = tx.Rollback(ctx)
				return err
			}
			if !ready {
				_ = tx.Rollback(ctx)
				blocked[n.child] = true
				log.Printf("child turn notice held for %s turn %d: %s (recheck in %s)", n.child, n.turn, reason, wait)
				a.scheduleChildTurnRetry(n.child, wait) //nolint:contextcheck // the retry fires after this call's own ctx may be done; it opens its own bounded one
				continue
			}
			if reason != "" {
				log.Printf("child turn notice forced for %s turn %d: %s", n.child, n.turn, reason)
			}
		}
		if !obsolete && !a.noteChildTurn(ctx, n.child, n.kind, "", n) {
			_ = tx.Rollback(ctx)
			blocked[n.child] = true
			continue
		}
		_, err = tx.Exec(ctx, `UPDATE child_turn_notices SET delivered=true WHERE child_id=$1 AND turn_seq=$2`, n.child, n.turn)
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if err != nil {
			return err
		}
		// Delivered: any scheduled retry for this child is moot now (BLG-447 review).
		a.childTurnRetry.clear(n.child)
	}
	return nil
}

func (a *API) reconcileChannelCloses(ctx context.Context) error {
	// Derive missing intents from durable lifecycle/recommendation facts too:
	// a crash between recording the end and invoking the hub hook loses nothing.
	_, err := a.dbPool.Exec(ctx, `INSERT INTO channel_close_notices(child_id)
 SELECT s.id FROM sessions s WHERE s.delegation_via='person' AND s.parent_session_id IS NOT NULL
 AND (s.ended_at IS NOT NULL OR s.status IN ('ended','stopped'))
 AND EXISTS(SELECT 1 FROM agent_events e WHERE e.session_id=s.parent_session_id AND e.kind='channel_recommendation'
 AND e.payload->>'channel_id'=s.id::text AND e.payload->>'state'='approved') ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	rows, err := a.dbPool.Query(ctx, `SELECT child_id::text FROM channel_close_notices WHERE NOT delivered ORDER BY child_id`)
	if err != nil {
		return err
	}
	var children []string
	for rows.Next() {
		var child string
		if err = rows.Scan(&child); err != nil {
			break
		}
		children = append(children, child)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, child := range children {
		if a.noteChildTurn(ctx, child, "recommended_channel_closed", "") {
			if _, err = a.dbPool.Exec(ctx, `UPDATE channel_close_notices SET delivered=true WHERE child_id=$1`, child); err != nil {
				return err
			}
		}
	}
	return nil
}
