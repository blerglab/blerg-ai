package server

// One-shot sessions (agent contract v1): `auto_stop`.
//
// An agent session started with a single prompt answers it and then waits for
// a message that, for a fire-and-forget caller, is never going to come. The
// lifecycle stays `running`, `result.terminal` stays false and the completion
// webhook never fires — the caller has to notice the turn ended and stop the
// session itself. A start with `auto_stop: true` says "this is one task": the
// runner stops the session as soon as the first turn_done after the prompt has
// been recorded, which is the same thing POST …/stop does (kill the runtime,
// status `ended`, tokens revoked, webhook delivered).
//
// The trigger lives where turn_done is PERSISTED (HandleAgentEvent), not where
// the agent emits it: a turn that was never recorded is not one a caller can
// read back, and the stop must follow the transcript the result reports.

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// turnDoneKind is the agent-loop event kind marking the end of a turn
// (internal/agent/events.go: TurnDonePayload).
const turnDoneKind = "turn_done"

// autoStopTimeout bounds one auto-stop: the claim plus the best-effort stop
// behind it. It runs inline on the daemon's read pump (see below), so it can
// never be allowed to wait on a k8s API call for as long as that call might
// take.
const autoStopTimeout = 20 * time.Second

// autoStopOnTurnDone ends a one-shot session whose turn has just finished.
//
// Everything that decides whether to act is in one conditional UPDATE
// (db.ClaimSessionAutoStop): the flag is on, the session is not already over,
// and no other caller has claimed it. So a second turn_done — a re-delivered
// event, another turn, or the same event seen by two replicas — falls through
// without stopping anything, and a session already stopped by hand is left
// alone. A session that never asked for this is never claimed.
//
// It runs on the event path's own goroutine, like the completion notifier it
// ends up calling, and that ordering is load-bearing rather than incidental: a
// turn_done is followed immediately by status_changed{idle}, and only because
// the row already says "ended" by the time that arrives does the state machine
// refuse it (isValidTransition knows no outgoing transition from a terminal
// status). Off the event goroutine, the two would race and a one-shot session
// could be dragged back to idle a moment after it ended.
//
// The work is one claim plus a best-effort stop, but the stop talks to the
// k8s API, so it is bounded: a slow or wedged Job delete must not hold the
// daemon's read pump for a minute. Past the deadline the session is already
// claimed and marked ended — only the cleanup behind it is cut short, which is
// what a best-effort stop is.
func (a *API) autoStopOnTurnDone(sessionID string) {
	if a == nil || a.dbPool == nil || sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), autoStopTimeout)
	defer cancel()
	err := withLimitResumeActionLock(ctx, a.dbPool, sessionID, func(conn *pgxpool.Conn) error {
		pending, err := db.HasPendingLimitResume(ctx, conn, sessionID)
		if err != nil {
			return err
		}
		if pending {
			return nil
		}
		// A person may have queued a replacement message just before the
		// interrupted turn's delayed turn_done arrives. Until that message's
		// user_message supplies its new turn ID, keep this old completion from
		// stopping the queued work. The fence is short and applies only to an
		// enabled preference; an explicit disable/interrupt still ends normally.
		retry, err := db.GetLimitResume(ctx, conn, sessionID)
		if err != nil {
			return err
		}
		if retry.Enabled && retry.State == "armed" && retry.TurnID != "" &&
			retry.TurnID == retry.CancelledTurnID && retry.CancelledAt != nil &&
			time.Since(*retry.CancelledAt) >= 0 && time.Since(*retry.CancelledAt) < 2*time.Minute {
			return nil
		}
		claimed, err := db.ClaimSessionAutoStop(ctx, a.dbPool, sessionID)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}
		cancelErr := db.CancelLimitResume(ctx, conn, sessionID)
		// Once claimed, cleanup must run even if the auxiliary retry row could
		// not be cleared: the session has already been marked ended.
		a.stopSessionUnlocked(ctx, sessionID, systemEnd(db.EndReasonAutoStopped))
		return cancelErr
	})
	if err != nil {
		log.Printf("auto-stop %s: %v", sessionID, err)
	}
}
