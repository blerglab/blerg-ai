package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Why a session ended (migration 018). A fixed vocabulary: every value the
// runner writes is one of these constants, and anything else is refused at the
// write boundary — end_reason is shown to people and returned to brokers, so it
// must never carry text a caller or an environment supplied. Adding a reason is
// one constant here, one entry in validEndReasons, and one entry in the
// frontend's END_REASON table.
const (
	// Someone asked for the stop. Which of the two follows from the actor's
	// principal kind (EndReasonForActor).
	EndReasonStoppedByUser  = "stopped_by_user"
	EndReasonStoppedByAgent = "stopped_by_agent"
	// The one-shot auto_stop ended the session after its first turn.
	EndReasonAutoStopped = "auto_stopped"
	// The session's process exited on its own, cleanly or not, with no stop
	// having been asked for.
	EndReasonProcessExited = "process_exited"
	EndReasonProcessFailed = "process_failed"
	// The session never got going (spawn failure, no Job, the pod gave up
	// while starting). error_reason carries the detail.
	EndReasonStartFailed = "start_failed"
	// The reconciler's verdicts (jobreconcile.go). The failing ones pair with
	// the fixed error_reason text written in the same statement.
	EndReasonJobFinished      = "job_finished"
	EndReasonJobFailed        = "job_failed"
	EndReasonJobDisappeared   = "job_disappeared"
	EndReasonNotResumed       = "not_resumed"
	EndReasonDaemonLost       = "daemon_lost"
	EndReasonDaemonUnreported = "daemon_unreported"
)

var validEndReasons = map[string]bool{
	EndReasonStoppedByUser:    true,
	EndReasonStoppedByAgent:   true,
	EndReasonAutoStopped:      true,
	EndReasonProcessExited:    true,
	EndReasonProcessFailed:    true,
	EndReasonStartFailed:      true,
	EndReasonJobFinished:      true,
	EndReasonJobFailed:        true,
	EndReasonJobDisappeared:   true,
	EndReasonNotResumed:       true,
	EndReasonDaemonLost:       true,
	EndReasonDaemonUnreported: true,
}

// ValidEndReason reports whether r is in the vocabulary.
func ValidEndReason(r string) bool { return validEndReasons[r] }

// Actor kinds stored in ended_by_kind: the principal kinds core issues, plus
// the runner's own static contract key.
const (
	EndedByHuman     = "human"
	EndedByAgent     = "agent"
	EndedByService   = "service"
	EndedByRunnerKey = "runner_key"
)

var validEndedByKinds = map[string]bool{
	EndedByHuman: true, EndedByAgent: true, EndedByService: true, EndedByRunnerKey: true,
}

// maxEndedByAccountLen bounds ended_by_account. Core account ids are UUIDs
// (36 chars); the slack is for a future id format, not for arbitrary text.
const maxEndedByAccountLen = 128

// SessionEnd is one attribution: why the session ended and, when an actor did
// it, who. The zero value means "no attribution".
type SessionEnd struct {
	Reason    string
	ByKind    string
	ByAccount string
}

// EndReasonForActor is the reason a stop requested by a principal of this kind
// is recorded under: a person's stop, or an automated caller's.
func EndReasonForActor(kind string) string {
	if kind == EndedByHuman {
		return EndReasonStoppedByUser
	}
	return EndReasonStoppedByAgent
}

// columns validates an attribution and returns the three column values. The
// reason must be in the vocabulary (an unknown one is a programming error, and
// refused rather than stored). An unknown actor kind, or an account id that is
// empty, too long or not a plain identifier, is dropped rather than stored —
// the reason alone is still worth recording.
func (e SessionEnd) columns() (reason string, kind, account *string, err error) {
	if !validEndReasons[e.Reason] {
		return "", nil, nil, fmt.Errorf("unknown end reason %q", e.Reason)
	}
	if !validEndedByKinds[e.ByKind] {
		return e.Reason, nil, nil, nil
	}
	k := e.ByKind
	if plainIdentifier(e.ByAccount) {
		a := e.ByAccount
		return e.Reason, &k, &a, nil
	}
	return e.Reason, &k, nil, nil
}

// plainIdentifier accepts an opaque id token — what a core account id is (a
// UUID) — and nothing else: letters, digits, '-' and '_'. No '@' or '.', so an
// email address (or any other contact detail) can never be stored as one, and
// no markup or control text reaches a reader.
func plainIdentifier(s string) bool {
	if s == "" || len(s) > maxEndedByAccountLen {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// PendingEndGrace is how long a stop recorded at request time survives the
// session still being reported alive. A kill normally lands in seconds, and the
// dying session may well report a last status on its way out (an agent whose
// turn is cancelled goes idle before it exits) — that must not erase the stop.
// A session still alive a minute later did not stop, and whatever ends it
// later is not this request.
const PendingEndGrace = 60 * time.Second

// pendingEndStale is the SQL for "the attribution on this row is an old,
// unfulfilled stop request": recorded before the grace window, on a row that
// has not ended. Kept in one place so every writer applies the same rule.
const pendingEndStale = `(end_recorded_at IS NULL OR end_recorded_at < now() - interval '60 seconds')`

// liveStatusesSQL / terminalStatusesSQL are the status sets the attribution
// rules turn on (see isLiveStatus / terminalSessionStatus in the server).
const (
	liveStatusesSQL     = `('running', 'waiting', 'idle')`
	terminalStatusesSQL = `('stopped', 'ended', 'error')`
)

// RecordSessionEnd attributes a session's end without changing its status, and
// reports whether it did. First write wins: a session that already has a
// reason keeps it.
//
// It exists for the ends that are REQUESTED before they happen. A stop sends
// kill_session to the daemon and the status only turns terminal when the
// daemon's session_ended comes back; recording the stop first is what lets
// that exit be read as "stopped by you" rather than "exited on its own".
//
// A row that has already ended ("stopped"/"ended" — including rows that ended
// before reasons were recorded, with a NULL reason) is never attributed after
// the fact. "error" is deliberately not excluded: a daemon-owned session in
// "error" is the revivable kind (an engine failure the daemon still hosts),
// and stopping it is a stop like any other.
func RecordSessionEnd(ctx context.Context, conn *pgxpool.Pool, sessionID string, end SessionEnd) (bool, error) {
	if conn == nil {
		return false, nil
	}
	reason, kind, account, err := end.columns()
	if err != nil {
		return false, err
	}
	tag, err := conn.Exec(ctx, `
		UPDATE sessions
		   SET end_reason       = $2,
		       ended_by_kind    = $3,
		       ended_by_account = $4,
		       end_recorded_at  = now()
		 WHERE id = $1
		   AND end_reason IS NULL
		   AND status NOT IN ('stopped', 'ended')
	`, sessionID, reason, kind, account)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// EndSessionStatus is UpdateSessionStatus for a write that ends the session:
// the status, ended_at (as given, exactly like UpdateSessionStatus) and the
// attribution land in one statement, and an attribution already on the row (a
// stop recorded when it was requested) is kept rather than overwritten.
func EndSessionStatus(ctx context.Context, conn *pgxpool.Pool, sessionID, status string, endedAt *time.Time, end SessionEnd) error {
	reason, kind, account, err := end.columns()
	if err != nil {
		return err
	}
	tag, err := conn.Exec(ctx, `
		UPDATE sessions
		   SET status            = $2,
		       ended_at          = $3,
		       status_changed_at = now(),
		       ended_by_kind     = CASE WHEN end_reason IS NULL THEN $5 ELSE ended_by_kind END,
		       ended_by_account  = CASE WHEN end_reason IS NULL THEN $6 ELSE ended_by_account END,
		       end_recorded_at   = CASE WHEN end_reason IS NULL THEN now() ELSE end_recorded_at END,
		       end_reason        = coalesce(end_reason, $4)
		 WHERE id = $1
	`, sessionID, status, endedAt, reason, kind, account)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("not found: %w", pgx.ErrNoRows)
	}
	return nil
}

// RetractSessionEnd undoes one RecordSessionEnd whose stop never reached the
// daemon, and reports whether it did. It is a compare-and-swap on exactly what
// that call wrote, on a row that has still not ended: an attribution written by
// anyone else in between (another stop, the session ending for real) is left
// alone.
func RetractSessionEnd(ctx context.Context, conn *pgxpool.Pool, sessionID string, end SessionEnd) (bool, error) {
	if conn == nil {
		return false, nil
	}
	reason, kind, account, err := end.columns()
	if err != nil {
		return false, err
	}
	tag, err := conn.Exec(ctx, `
		UPDATE sessions
		   SET end_reason = NULL, ended_by_kind = NULL, ended_by_account = NULL, end_recorded_at = NULL
		 WHERE id = $1
		   AND end_reason = $2
		   AND ended_by_kind IS NOT DISTINCT FROM $3
		   AND ended_by_account IS NOT DISTINCT FROM $4
		   AND status NOT IN `+terminalStatusesSQL+`
	`, sessionID, reason, kind, account)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ExpirePendingSessionEnd drops a stop that was requested more than
// PendingEndGrace ago on a session its daemon still reports alive. A status
// change into a live state does this inside UpdateSessionStatus; this covers
// the session that is simply still there, heartbeat after heartbeat, with no
// transition to hang it on. Reports whether anything was dropped.
func ExpirePendingSessionEnd(ctx context.Context, conn *pgxpool.Pool, sessionID string) (bool, error) {
	if conn == nil {
		return false, nil
	}
	tag, err := conn.Exec(ctx, `
		UPDATE sessions
		   SET end_reason = NULL, ended_by_kind = NULL, ended_by_account = NULL, end_recorded_at = NULL
		 WHERE id = $1
		   AND end_reason IS NOT NULL
		   AND status NOT IN `+terminalStatusesSQL+`
		   AND `+pendingEndStale+`
	`, sessionID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
