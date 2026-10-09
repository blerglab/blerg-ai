package db

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Row types ────────────────────────────────────────────────────────────────

// SessionRow is the result of listing sessions (includes joined daemon name).
type SessionRow struct {
	ID          string
	DaemonID    string
	DaemonName  string
	Status      string
	ProjectPath string
	Repo        string
	Title       *string
	Model       *string
	Engine      *string
	Effort      *string
	StartedAt   time.Time
	EndedAt     *time.Time
	Unread      bool
	Starred     bool
	// SpawningAccountID is the verified account id (identity.Principal.Sub)
	// of the human who requested this session, set by HandlePostSessions
	// right after InsertSession. Nil for sessions created before this column
	// existed, or spawned by a path that doesn't carry an account identity
	// (e.g. runner-brokered sessions). resumeClusterSession reads it back so
	// a resumed session still uses the spawning account's personal
	// credential instead of falling back to the shared operator Secret.
	SpawningAccountID *string
	// Runtime is how the session was launched ("daemon" | "docker" | "cluster");
	// SkipPermissions whether engine permission prompts were bypassed. Both are
	// posture facts the UI states plainly (R5) — nil/false for rows older than
	// migration 012. ErrorReason is the daemon's spawn/run failure text (Task 6).
	Runtime         *string
	SkipPermissions bool
	ErrorReason     *string
	// Kind is "tmux" or "agent" (migration 008, NOT NULL DEFAULT 'tmux'), read
	// back alongside Runtime so the sessions API can say what a session is as
	// well as where it runs.
	Kind string
	// TokenID is the agent token that started the session (migration 013),
	// carried to blerg-core on credential fetches so core can check the token
	// is still live. CallbackURL/CallbackSecret are the completion webhook;
	// the secret is server-side only — it signs the delivery and must never
	// be returned by an endpoint or written to a log. GitURL is the clone URL
	// the session actually ran with.
	TokenID        *string
	CallbackURL    *string
	CallbackSecret *string
	GitURL         *string
	// AutoStop is the one-shot flag (migration 017): the runner stops this
	// session itself once its first turn is done, instead of leaving it
	// waiting for input nobody is going to send. False for every session
	// started without it, which is the interactive default.
	AutoStop bool
	// EndReason / EndedByKind / EndedByAccount are the end attribution
	// (migration 018, session_end.go): why the session ended and, when an actor
	// ended it, that actor's principal kind and account id. All nil for a live
	// session and for rows that ended before the columns existed.
	EndReason      *string
	EndedByKind    *string
	EndedByAccount *string
	// Private (migration 022): the session is visible only to
	// SpawningAccountID. See internal/server/privacy.go.
	Private bool
	// CronID (migration 023) is the cron that started the session, nil for every
	// other session. The UI badges it and the scheduler counts and stops by it.
	CronID *string
	// StartedByKind / StartedByName: who started the session (migration 037): "agent" (an agent
	// token, Name = the owner's label for it), "runner_key", "cron", or "" for the person.
	StartedByKind string
	StartedByName string
	// Interaction (migration 038): "interactive" (a person reads the chat as the session works)
	// or "unattended" (nobody does). "" on a row started before the column existed; the server
	// reads that through one rule (server.effectiveInteraction), never directly.
	Interaction string
}

// IdempotencyRow is one runner_idempotency record: which session a
// (scope, key) pair produced, and a hash of the request that produced it.
type IdempotencyRow struct {
	Scope       string
	Key         string
	RequestHash string
	SessionID   string
	CreatedAt   time.Time
}

// SessionEventRow is one row from session_events.
type SessionEventRow struct {
	ID        int64
	SessionID string
	Type      string
	Data      string
	Seq       int64
	CreatedAt time.Time
}

// PushSubscriptionRow is one row from push_subscriptions.
type PushSubscriptionRow struct {
	ID        string
	Endpoint  string
	P256dh    string
	Auth      string
	CreatedAt time.Time
}

// DaemonRow is the result of looking up a daemon.
type DaemonRow struct {
	ID        string
	Name      string
	Mode      string
	ReposRoot string
	Status    string
}

// ─── Daemons ──────────────────────────────────────────────────────────────────

// UpsertDaemon inserts a daemon record or updates name/mode/repos_root on
// conflict with the given id.
func UpsertDaemon(ctx context.Context, conn *pgxpool.Pool, id, name, mode, reposRoot string) error {
	_, err := conn.Exec(ctx, `
		INSERT INTO daemons (id, name, mode, repos_root)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE
		  SET name       = EXCLUDED.name,
		      mode       = EXCLUDED.mode,
		      repos_root = EXCLUDED.repos_root
	`, id, name, mode, reposRoot)
	return err
}

// SetDaemonStatus updates the status and last_seen_at timestamp for a daemon.
func SetDaemonStatus(ctx context.Context, conn *pgxpool.Pool, id, status string) error {
	tag, err := conn.Exec(ctx, `
		UPDATE daemons
		   SET status       = $2,
		       last_seen_at = now()
		 WHERE id = $1
	`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("not found: %w", pgx.ErrNoRows)
	}
	return nil
}

// GetDaemonByName looks up a daemon by name. Returns nil, nil if not found.
func GetDaemonByName(ctx context.Context, conn *pgxpool.Pool, name string) (*DaemonRow, error) {
	row := conn.QueryRow(ctx, `
		SELECT id, name, mode, repos_root, status
		  FROM daemons
		 WHERE name = $1
	`, name)
	var d DaemonRow
	err := row.Scan(&d.ID, &d.Name, &d.Mode, &d.ReposRoot, &d.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &d, nil
}

// ─── Sessions ─────────────────────────────────────────────────────────────────

// GetSession looks up a single session by ID. Returns nil, nil if not found.
// DaemonName is not populated (no join is performed).
func GetSession(ctx context.Context, conn *pgxpool.Pool, sessionID string) (*SessionRow, error) {
	row := conn.QueryRow(ctx, `
		SELECT id, daemon_id, status, project_path, repo, title, model, NULLIF(engine, ''), effort, started_at, ended_at, unread, starred, spawning_account_id, runtime, skip_permissions, error_reason, token_id, callback_url, callback_secret, git_url, auto_stop, kind, end_reason, ended_by_kind, ended_by_account, private, cron_id::text, started_by_kind, started_by_name, interaction
		  FROM sessions
		 WHERE id = $1
	`, sessionID)
	var r SessionRow
	err := row.Scan(&r.ID, &r.DaemonID, &r.Status, &r.ProjectPath, &r.Repo,
		&r.Title, &r.Model, &r.Engine, &r.Effort, &r.StartedAt, &r.EndedAt, &r.Unread, &r.Starred, &r.SpawningAccountID,
		&r.Runtime, &r.SkipPermissions, &r.ErrorReason,
		&r.TokenID, &r.CallbackURL, &r.CallbackSecret, &r.GitURL, &r.AutoStop, &r.Kind,
		&r.EndReason, &r.EndedByKind, &r.EndedByAccount, &r.Private, &r.CronID, &r.StartedByKind, &r.StartedByName, &r.Interaction)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// SetSessionPrivate sets or clears a session's private flag (migration 022):
// a private session is visible only to its spawning account. Callers go
// through the server's MarkPrivate, which also drops the in-process
// visibility cache. A session that does not exist is pgx.ErrNoRows.
//
// Marking a session private also unbinds it from any ticket: a ticket card is
// visible to everyone on its board, and it names its session.
func SetSessionPrivate(ctx context.Context, conn *pgxpool.Pool, sessionID string, private bool) error {
	tag, err := conn.Exec(ctx, `UPDATE sessions SET private = $2 WHERE id = $1`, sessionID, private)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if private {
		if _, err := conn.Exec(ctx, `UPDATE tickets SET session_id = NULL WHERE session_id = $1`, sessionID); err != nil {
			return err
		}
	}
	return nil
}

// ListPrivateSessionOwners returns every private session and its owning
// account ("" when it has none). The runner server reads it at start-up, and
// again after a failed read, to seed its in-memory record of private sessions.
func ListPrivateSessionOwners(ctx context.Context, conn *pgxpool.Pool) (map[string]string, error) {
	rows, err := conn.Query(ctx, `SELECT id::text, COALESCE(spawning_account_id, '') FROM sessions WHERE private`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, owner string
		if err := rows.Scan(&id, &owner); err != nil {
			return nil, err
		}
		out[id] = owner
	}
	return out, rows.Err()
}

// PrivateSessionOwners returns, for the given session ids, the private ones
// and the account each belongs to ("" when the session has no spawning
// account, which nobody can then see). Ids that are not private, or do not
// exist, are absent from the result.
func PrivateSessionOwners(ctx context.Context, conn *pgxpool.Pool, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := conn.Query(ctx, `
		SELECT id::text, COALESCE(spawning_account_id, '')
		  FROM sessions
		 WHERE private AND id::text = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, owner string
		if err := rows.Scan(&id, &owner); err != nil {
			return nil, err
		}
		out[id] = owner
	}
	return out, rows.Err()
}

// InsertSession creates a new session record. ON CONFLICT (id) DO NOTHING makes
// it idempotent: when the server pre-creates an assist session row before
// the daemon confirms via session_started, the daemon's arrival is a no-op.
func InsertSession(ctx context.Context, conn *pgxpool.Pool, id, daemonID, status, projectPath, repo, title, model string) error {
	return InsertSessionAs(ctx, conn, id, daemonID, status, projectPath, repo, title, model, SessionOrigin{})
}

// SessionOrigin is what a start knows about a session before its row exists and
// must be written IN the insert, so that the row is never visible without it:
// a session that is private, or that a cron started, must not be listable by
// another account in the window between a plain insert and a follow-up write.
// The zero value adds nothing (an ordinary session).
type SessionOrigin struct {
	// SpawningAccount is the verified account the session is started for. A
	// private session is private TO this account, so it goes in with the flag.
	SpawningAccount string
	// Private makes the session visible to SpawningAccount alone (migration 022).
	Private bool
	// CronID is the cron that started the session (migration 023).
	CronID string
}

// sessionOriginArgs are the extra insert arguments: spawning account, private, cron id.
func (o SessionOrigin) args() (account any, private bool, cronID any) {
	if o.SpawningAccount != "" {
		account = o.SpawningAccount
	}
	if o.CronID != "" {
		cronID = o.CronID
	}
	return account, o.Private, cronID
}

// InsertSessionAs is InsertSession that also writes the session's origin in the
// same statement. On a conflict (a revived session) an origin only ever adds:
// a private session stays private and a known account or cron is not erased.
func InsertSessionAs(ctx context.Context, conn *pgxpool.Pool, id, daemonID, status, projectPath, repo, title, model string, o SessionOrigin) error {
	account, private, cronID := o.args()
	// Upsert: a resumed cluster session re-sends session_started for a row
	// that already exists — revive it (status/daemon) instead of erroring.
	_, err := conn.Exec(ctx, `
		INSERT INTO sessions (id, daemon_id, status, project_path, repo, title, model,
		                      spawning_account_id, private, cron_id)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9, $10::uuid)
		ON CONFLICT (id) DO UPDATE
		  SET daemon_id         = EXCLUDED.daemon_id,
		      spawning_account_id = COALESCE(EXCLUDED.spawning_account_id, sessions.spawning_account_id),
		      private           = sessions.private OR EXCLUDED.private,
		      cron_id           = COALESCE(EXCLUDED.cron_id, sessions.cron_id),
		      status            = EXCLUDED.status,
		      -- Where the host actually put the session wins over the row the
		      -- server pre-created: a "No repository" scratch folder whose name
		      -- was taken is created under a fresh one (daemon
		      -- createScratchFolder). An empty value never erases a known one.
		      project_path      = COALESCE(NULLIF(EXCLUDED.project_path, ''), sessions.project_path),
		      repo              = COALESCE(NULLIF(EXCLUDED.repo, ''), sessions.repo),
		      ended_at          = NULL,
		      error_reason      = NULL,
		      -- A revived session has not ended: whatever ended it before
		      -- (a stop, an exit) no longer describes it (migration 018).
		      end_reason        = NULL,
		      ended_by_kind     = NULL,
		      ended_by_account  = NULL,
		      end_recorded_at   = NULL,
		      -- A revived session starts its clocks again: the reconciler's
		      -- windows (resume expiry, missing-Job grace, lost-daemon sweep)
		      -- all measure from these, and a session that has just come back
		      -- must not be judged on how long it was gone.
		      status_changed_at = now(),
		      daemon_lost_at    = NULL
	`, id, daemonID, status, projectPath, repo, title, model, account, private, cronID)
	return err
}

// InsertClusterSession is InsertSession for a cluster-runtime session: it
// writes runtime='cluster' in the SAME statement as the row.
//
// That is not a tidiness point. The reconciler finds sessions to close out by
// `WHERE runtime = 'cluster'`, so a session whose runtime landed in a separate
// follow-up write — one that can fail, or simply not have happened yet when the
// pod dies — is invisible to it: it sits at "starting" forever, status and
// result report a session still about to begin, and the completion webhook
// never fires. One statement, one truth.
//
// skip_permissions is deliberately not set here: cluster sessions never bypass
// the engine's prompts, and the column defaults to false.
func InsertClusterSession(ctx context.Context, conn *pgxpool.Pool, id, daemonID, status, projectPath, repo, title, model string) error {
	return InsertClusterSessionAs(ctx, conn, id, daemonID, status, projectPath, repo, title, model, SessionOrigin{})
}

// InsertClusterSessionAs is InsertClusterSession that also writes the session's
// origin (private, cron, spawning account) in the same statement; see
// SessionOrigin and InsertSessionAs.
func InsertClusterSessionAs(ctx context.Context, conn *pgxpool.Pool, id, daemonID, status, projectPath, repo, title, model string, o SessionOrigin) error {
	account, private, cronID := o.args()
	_, err := conn.Exec(ctx, `
		INSERT INTO sessions (id, daemon_id, status, project_path, repo, title, model, runtime,
		                      spawning_account_id, private, cron_id)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), 'cluster', $8, $9, $10::uuid)
		ON CONFLICT (id) DO UPDATE
		  SET daemon_id         = EXCLUDED.daemon_id,
		      spawning_account_id = COALESCE(EXCLUDED.spawning_account_id, sessions.spawning_account_id),
		      private           = sessions.private OR EXCLUDED.private,
		      cron_id           = COALESCE(EXCLUDED.cron_id, sessions.cron_id),
		      status            = EXCLUDED.status,
		      runtime           = 'cluster',
		      ended_at          = NULL,
		      error_reason      = NULL,
		      end_reason        = NULL,
		      ended_by_kind     = NULL,
		      ended_by_account  = NULL,
		      end_recorded_at   = NULL,
		      status_changed_at = now(),
		      daemon_lost_at    = NULL
	`, id, daemonID, status, projectPath, repo, title, model, account, private, cronID)
	return err
}

// DeleteSession removes a session row outright. board_tokens rows cascade
// (FK ON DELETE CASCADE), so a token minted for it dies with it. Used only
// for a spawn that never reached a daemon; a session that ran is ended, not
// deleted. Deleting a missing row is a no-op.
func DeleteSession(ctx context.Context, conn *pgxpool.Pool, id string) error {
	_, err := conn.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

// CreateAssistSession pre-creates an assist session row with status "starting",
// board_id, optional ticket_id, and assist=true. Must be called before
// MintBoardToken and SetTicketSession so their FKs resolve immediately.
// ON CONFLICT (id) DO NOTHING makes it idempotent: a daemon resend of
// session_started for the same ID is a harmless no-op.
func CreateAssistSession(ctx context.Context, conn *pgxpool.Pool, sessionID, daemonID, boardID string, ticketID *string, repo, title string) error {
	_, err := conn.Exec(ctx, `
		INSERT INTO sessions (id, daemon_id, status, project_path, repo, title, board_id, ticket_id, assist)
		VALUES ($1, $2, 'starting', '', $3, NULLIF($4, ''), $5, $6, true)
		ON CONFLICT (id) DO NOTHING
	`, sessionID, daemonID, repo, title, boardID, ticketID)
	return err
}

// UpdateSessionStatus updates the status (and optionally ended_at) of a session.
//
// A move INTO a live status also drops the end attribution (migration 018) in
// the same statement, when it no longer describes the session: always when the
// session is coming back from an ending (a revive), and otherwise only when it
// is a stop requested more than PendingEndGrace ago — a session that kept
// going did not stop, but one that reports a last status while the kill is
// still landing did. One statement, so an ending that lands concurrently can
// never have its fresh reason erased by a separate clear.
func UpdateSessionStatus(ctx context.Context, conn *pgxpool.Pool, id, status string, endedAt *time.Time) error {
	tag, err := conn.Exec(ctx, `
		UPDATE sessions
		   SET status            = $2,
		       ended_at          = $3,
		       status_changed_at = now(),
		       end_reason        = CASE WHEN `+clearEndOnLive+` THEN NULL ELSE end_reason END,
		       ended_by_kind     = CASE WHEN `+clearEndOnLive+` THEN NULL ELSE ended_by_kind END,
		       ended_by_account  = CASE WHEN `+clearEndOnLive+` THEN NULL ELSE ended_by_account END,
		       end_recorded_at   = CASE WHEN `+clearEndOnLive+` THEN NULL ELSE end_recorded_at END
		 WHERE id = $1
	`, id, status, endedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("not found: %w", pgx.ErrNoRows)
	}
	return nil
}

// clearEndOnLive is UpdateSessionStatus's rule for dropping an attribution
// ($2 is the new status; the other columns are the row's values before the
// update).
const clearEndOnLive = `($2 IN ` + liveStatusesSQL + ` AND (status IN ` + terminalStatusesSQL + ` OR ` + pendingEndStale + `))`

// SetSessionError persists the daemon-reported reason a spawn or run failed
// (Task 6 / trial blocker 2), so a bare "error" status isn't the only thing
// left behind — the row remembers why.
func SetSessionError(ctx context.Context, conn *pgxpool.Pool, sessionID, reason string) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET error_reason = $2 WHERE id = $1`, sessionID, reason)
	return err
}

// ClearSessionError wipes a session's error_reason. Called whenever a session's
// status leaves "error" — a transient WS drop (laptop sleep, network blip,
// daemon restart) marks a session "error" and reconcileSessions later revives
// it to "running" once the daemon reconnects; without this, the stale reason
// from the earlier disconnect would linger and mislead every later reader
// (the session card, SessionDetail, and Task 15's status endpoint) into
// showing a permanent failure banner for a session that is actually healthy.
func ClearSessionError(ctx context.Context, conn *pgxpool.Pool, sessionID string) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET error_reason = NULL WHERE id = $1`, sessionID)
	return err
}

// ListSessions returns all sessions joined with their daemon's name, ordered
// by started_at descending.
func ListSessions(ctx context.Context, conn *pgxpool.Pool) ([]SessionRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT s.id, s.daemon_id, d.name, s.status, s.project_path, s.repo,
		       s.title, s.model, NULLIF(s.engine, ''), s.effort, s.started_at, s.ended_at, s.unread, s.starred,
		       s.runtime, s.skip_permissions, s.error_reason, s.kind,
		       s.end_reason, s.ended_by_kind, s.ended_by_account, s.spawning_account_id, s.private, s.cron_id::text,
		       s.started_by_kind, s.started_by_name, s.interaction
		  FROM sessions s
		  JOIN daemons d ON d.id = s.daemon_id
		 ORDER BY s.started_at DESC
	`)
	if err != nil {
		return nil, err
	}
	return scanSessionRows(rows)
}

// ListSessionsByStatus returns sessions with a particular status.
func ListSessionsByStatus(ctx context.Context, conn *pgxpool.Pool, status string) ([]SessionRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT s.id, s.daemon_id, d.name, s.status, s.project_path, s.repo,
		       s.title, s.model, NULLIF(s.engine, ''), s.effort, s.started_at, s.ended_at, s.unread, s.starred,
		       s.runtime, s.skip_permissions, s.error_reason, s.kind,
		       s.end_reason, s.ended_by_kind, s.ended_by_account, s.spawning_account_id, s.private, s.cron_id::text,
		       s.started_by_kind, s.started_by_name, s.interaction
		  FROM sessions s
		  JOIN daemons d ON d.id = s.daemon_id
		 WHERE s.status = $1
		 ORDER BY s.started_at DESC
	`, status)
	if err != nil {
		return nil, err
	}
	return scanSessionRows(rows)
}

// ReconcileSessionRow is the slice of a session the reconciler needs: what it
// is doing, how long it has been doing it, and enough to broadcast a change.
// A narrow row rather than a SessionRow because the reconciler asks a narrow
// question and runs on a timer against every live session.
//
// LastChange is when the session last transitioned (falling back to its start
// for rows written before status_changed_at existed) — the clock every one of
// these timeouts is really measured against.
type ReconcileSessionRow struct {
	ID         string
	Status     string
	Unread     bool
	LastChange time.Time
}

func scanReconcileRows(rows pgx.Rows) ([]ReconcileSessionRow, error) {
	defer rows.Close()
	var out []ReconcileSessionRow
	for rows.Next() {
		var r ReconcileSessionRow
		if err := rows.Scan(&r.ID, &r.Status, &r.Unread, &r.LastChange); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListClusterSessionsToReconcile returns the cluster-runtime sessions whose row
// says they are still going. These are the only sessions whose Job the
// reconciler needs to ask about: a session the pod itself closed out is already
// terminal, and a daemon session has no Job.
//
// waiting/idle are deliberately excluded — those statuses only exist because a
// live pod reported them, so the Job cannot have ended unnoticed. "disconnected"
// IS included, but for the opposite reason: those sessions are resumable, and
// the reconciler's job there is to leave them alone until the resume window
// has passed rather than to read anything into the Job's state.
func ListClusterSessionsToReconcile(ctx context.Context, conn *pgxpool.Pool) ([]ReconcileSessionRow, error) {
	if conn == nil {
		return nil, nil
	}
	rows, err := conn.Query(ctx, `
		SELECT id, status, unread, greatest(started_at, coalesce(status_changed_at, started_at))
		  FROM sessions
		 WHERE runtime = 'cluster'
		   AND status IN ('starting', 'running', 'disconnected')
		 ORDER BY started_at DESC
	`)
	if err != nil {
		return nil, err
	}
	return scanReconcileRows(rows)
}

// ListLostDaemonSessions returns sessions whose daemon disconnected before the
// given cutoff and has not come back — the desktop counterpart to a finished
// Job. Cluster sessions are excluded: their liveness is the Job's story, told
// above.
func ListLostDaemonSessions(ctx context.Context, conn *pgxpool.Pool, cutoff time.Time) ([]ReconcileSessionRow, error) {
	if conn == nil {
		return nil, nil
	}
	rows, err := conn.Query(ctx, `
		SELECT id, status, unread, greatest(started_at, coalesce(status_changed_at, started_at))
		  FROM sessions
		 WHERE daemon_lost_at IS NOT NULL
		   AND daemon_lost_at < $1
		   AND coalesce(runtime, '') <> 'cluster'
		 ORDER BY daemon_lost_at
	`, cutoff)
	if err != nil {
		return nil, err
	}
	return scanReconcileRows(rows)
}

// SetSessionDaemonLost records that the session's daemon dropped its
// connection. Set alongside the disconnect-induced "error" status, which is
// revivable — this column is what lets the reconciler tell a daemon that is
// coming back from one that is not.
func SetSessionDaemonLost(ctx context.Context, conn *pgxpool.Pool, sessionID string) error {
	if conn == nil {
		return nil
	}
	_, err := conn.Exec(ctx, `UPDATE sessions SET daemon_lost_at = now() WHERE id = $1`, sessionID)
	return err
}

// SessionAwaitsDaemon reports whether the session was marked lost by its daemon's disconnect and
// has neither been revived nor finalised since (daemon_lost_at is set): it is waiting for its
// daemon to come back. False for a session that does not exist.
func SessionAwaitsDaemon(ctx context.Context, conn *pgxpool.Pool, sessionID string) (bool, error) {
	if conn == nil {
		return false, nil
	}
	var awaits bool
	err := conn.QueryRow(ctx, `SELECT daemon_lost_at IS NOT NULL FROM sessions WHERE id = $1`, sessionID).Scan(&awaits)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return awaits, err
}

// TouchSessionStatusChanged restarts a session's status clock without changing
// its status. A resume is the case that needs it: the row stays "disconnected"
// while the replacement pod boots, and without this the reconciler would keep
// measuring the resume-expiry and missing-Job windows from the moment the
// session was lost — and could expire a session that is coming back right now.
func TouchSessionStatusChanged(ctx context.Context, conn *pgxpool.Pool, sessionID string) error {
	if conn == nil {
		return nil
	}
	_, err := conn.Exec(ctx, `UPDATE sessions SET status_changed_at = now() WHERE id = $1`, sessionID)
	return err
}

// FinishLostDaemonSession closes out a session whose daemon never came back,
// and reports whether it actually did.
//
// Every condition in the WHERE clause is load-bearing: the row must still be
// marked lost (a revive clears daemon_lost_at — the session is alive again),
// still lost since before the cutoff, and still in the disconnect's "error"
// state. So a session revived between the sweep's SELECT and this UPDATE is
// left alone, and a concurrent sweep (another replica) finds nothing to do.
//
// The reason is written in the same statement, and daemon_lost_at cleared in
// it, so the row the webhook then reads is already final — the payload can
// never race a second write — and no later sweep can pick the session up again.
func FinishLostDaemonSession(ctx context.Context, conn *pgxpool.Pool, sessionID, reason string, cutoff time.Time) (bool, error) {
	if conn == nil {
		return false, nil
	}
	var updated int
	err := conn.QueryRow(ctx, `
		UPDATE sessions
		   SET status            = 'error',
		       ended_at          = now(),
		       status_changed_at = now(),
		       error_reason      = $2,
		       end_reason        = coalesce(end_reason, 'daemon_lost'),
		       daemon_lost_at    = NULL
		 WHERE id = $1
		   AND daemon_lost_at IS NOT NULL
		   AND daemon_lost_at < $3
		   AND status = 'error'
		RETURNING 1
	`, sessionID, reason, cutoff).Scan(&updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ClearSessionDaemonLost forgets a lost daemon — because it came back (the
// session was revived) or because the session has now been finalised and must
// not be swept a second time.
func ClearSessionDaemonLost(ctx context.Context, conn *pgxpool.Pool, sessionID string) error {
	if conn == nil {
		return nil
	}
	_, err := conn.Exec(ctx, `UPDATE sessions SET daemon_lost_at = NULL WHERE id = $1`, sessionID)
	return err
}

// FinishClusterSession marks a session terminal on the strength of its Job's
// state, and reports whether it actually did. The WHERE clause is the whole
// point: between the reconciler listing a session and deciding about it, the
// pod may have reported its own outcome, and a cluster-side guess must never
// overwrite what the session said about itself. An empty reason clears the
// error text (a clean exit explains itself).
//
// staleBefore, when non-nil, additionally requires that the session has not
// transitioned since that instant. The 24 h resume expiry needs it: the
// reconciler reads a session's last transition, decides the resume window has
// passed, and only then writes — and in between, a message can resume the
// session, which restarts the clock (TouchSessionStatusChanged). Without this
// condition that resume would be closed out by a decision taken before it
// happened. Every other case passes nil: they are judgements about the Job,
// not about how long the row has sat still.
func FinishClusterSession(ctx context.Context, conn *pgxpool.Pool, sessionID, status, reason, endReason string, staleBefore *time.Time) (bool, error) {
	if conn == nil {
		return false, nil
	}
	if !ValidEndReason(endReason) {
		return false, fmt.Errorf("unknown end reason %q", endReason)
	}
	var updated int
	err := conn.QueryRow(ctx, `
		UPDATE sessions
		   SET status            = $2,
		       ended_at          = now(),
		       status_changed_at = now(),
		       error_reason      = NULLIF($3, ''),
		       end_reason        = coalesce(end_reason, $5)
		 WHERE id = $1
		   AND status IN ('starting', 'running', 'disconnected')
		   AND ($4::timestamptz IS NULL
		        OR coalesce(status_changed_at, started_at) < $4::timestamptz)
		RETURNING 1
	`, sessionID, status, reason, staleBefore, endReason).Scan(&updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListSessionsByDaemon returns all sessions for a specific daemon, ordered
// by started_at descending.
func ListSessionsByDaemon(ctx context.Context, conn *pgxpool.Pool, daemonID string) ([]SessionRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT s.id, s.daemon_id, d.name, s.status, s.project_path, s.repo,
		       s.title, s.model, NULLIF(s.engine, ''), s.effort, s.started_at, s.ended_at, s.unread, s.starred,
		       s.runtime, s.skip_permissions, s.error_reason, s.kind,
		       s.end_reason, s.ended_by_kind, s.ended_by_account, s.spawning_account_id, s.private, s.cron_id::text,
		       s.started_by_kind, s.started_by_name, s.interaction
		  FROM sessions s
		  JOIN daemons d ON d.id = s.daemon_id
		 WHERE s.daemon_id = $1
		 ORDER BY s.started_at DESC
	`, daemonID)
	if err != nil {
		return nil, err
	}
	return scanSessionRows(rows)
}

// ListSessionsByDaemonMode returns all sessions whose owning daemon has the
// given mode (e.g. "runner" for cluster-runtime session pods). Cluster
// sessions can't be listed by a fixed daemon_id: each session pod connects
// as its own ephemeral per-session daemon (name "runner-<session-id>"),
// registered with a fresh UUID on its own hello — the placeholder
// clusterDaemonID row (internal/server/k8sjobs.go) is overwritten the
// moment the real pod's session_started arrives (InsertSession's
// ON CONFLICT DO UPDATE SET daemon_id = EXCLUDED.daemon_id, needed so
// message routing targets the pod's real connection). Mode is the one
// property every cluster-runtime daemon — placeholder and real pods alike —
// shares, since internal/runner/runner.go always sends DaemonMode: "runner".
func ListSessionsByDaemonMode(ctx context.Context, conn *pgxpool.Pool, mode string) ([]SessionRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT s.id, s.daemon_id, d.name, s.status, s.project_path, s.repo,
		       s.title, s.model, NULLIF(s.engine, ''), s.effort, s.started_at, s.ended_at, s.unread, s.starred,
		       s.runtime, s.skip_permissions, s.error_reason, s.kind,
		       s.end_reason, s.ended_by_kind, s.ended_by_account, s.spawning_account_id, s.private, s.cron_id::text,
		       s.started_by_kind, s.started_by_name, s.interaction
		  FROM sessions s
		  JOIN daemons d ON d.id = s.daemon_id
		 WHERE d.mode = $1
		 ORDER BY s.started_at DESC
	`, mode)
	if err != nil {
		return nil, err
	}
	return scanSessionRows(rows)
}

func scanSessionRows(rows pgx.Rows) ([]SessionRow, error) {
	defer rows.Close()
	var result []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(
			&r.ID, &r.DaemonID, &r.DaemonName, &r.Status,
			&r.ProjectPath, &r.Repo, &r.Title, &r.Model, &r.Engine, &r.Effort, &r.StartedAt, &r.EndedAt, &r.Unread, &r.Starred,
			&r.Runtime, &r.SkipPermissions, &r.ErrorReason, &r.Kind,
			&r.EndReason, &r.EndedByKind, &r.EndedByAccount, &r.SpawningAccountID, &r.Private, &r.CronID,
			&r.StartedByKind, &r.StartedByName, &r.Interaction,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// SetSessionUnread sets the unread flag for a session.
func SetSessionUnread(ctx context.Context, conn *pgxpool.Pool, sessionID string, unread bool) error {
	tag, err := conn.Exec(ctx, `UPDATE sessions SET unread = $2 WHERE id = $1`, sessionID, unread)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("not found: %w", pgx.ErrNoRows)
	}
	return nil
}

// SetSessionStarred sets the starred flag for a session.
func SetSessionStarred(ctx context.Context, conn *pgxpool.Pool, sessionID string, starred bool) error {
	tag, err := conn.Exec(ctx, `UPDATE sessions SET starred = $2 WHERE id = $1`, sessionID, starred)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("not found: %w", pgx.ErrNoRows)
	}
	return nil
}

// UpdateSessionTitle updates the title of a session.
func UpdateSessionTitle(ctx context.Context, conn *pgxpool.Pool, sessionID, title string) error {
	tag, err := conn.Exec(ctx, `UPDATE sessions SET title = $2 WHERE id = $1`, sessionID, title)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("not found: %w", pgx.ErrNoRows)
	}
	return nil
}

// UpdateSessionMeta updates the model and/or effort fields of a session.
// Empty strings are ignored (no update for that field).
func UpdateSessionMeta(ctx context.Context, conn *pgxpool.Pool, sessionID, model, effort string) error {
	if model == "" && effort == "" {
		return nil
	}
	_, err := conn.Exec(ctx, `
		UPDATE sessions
		   SET model  = CASE WHEN $2 <> '' THEN $2 ELSE model END,
		       effort = CASE WHEN $3 <> '' THEN $3 ELSE effort END
		 WHERE id = $1
	`, sessionID, model, effort)
	return err
}

// ─── Session events ───────────────────────────────────────────────────────────

// AppendSessionEvent inserts a new event for a session.
func AppendSessionEvent(ctx context.Context, conn *pgxpool.Pool, sessionID, eventType, data string, seq int64) error {
	_, err := conn.Exec(ctx, `
		INSERT INTO session_events (session_id, type, data, seq)
		VALUES ($1, $2, $3, $4)
	`, sessionID, eventType, data, seq)
	return err
}

// GetSessionEvents returns all events for a session ordered by seq ascending.
func GetSessionEvents(ctx context.Context, conn *pgxpool.Pool, sessionID string) ([]SessionEventRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT id, session_id, type, data, seq, created_at
		  FROM session_events
		 WHERE session_id = $1
		 ORDER BY id ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SessionEventRow
	for rows.Next() {
		var r SessionEventRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Type, &r.Data, &r.Seq, &r.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// sessionEventsTailBatchSize is the number of rows fetched per keyset page
// in GetSessionEventsTail. Chosen to keep most sessions (which are well
// under the byte budget) to a single round trip, while bounding both the
// Postgres-side work and the client-side memory of any one query to a small,
// fixed batch for heavy sessions.
const sessionEventsTailBatchSize = 256

// GetSessionEventsTail returns the tail of a session's events, chronological
// (ascending id) order, bounded so the decoded size of `output` events does
// not exceed maxBytes. It walks events newest-first in keyset-paginated
// batches (WHERE session_id = $1 AND id < cursor ORDER BY id DESC LIMIT n),
// accumulating the decoded byte size of `output` rows (data is
// base64-encoded — see AppendSessionEvent); non-output rows don't count
// toward the budget but are still included if they fall within the returned
// range. The row that crosses the budget is included whole, then walking
// stops without fetching further batches or draining older rows. If the
// session's total output is under budget, all events are returned.
func GetSessionEventsTail(ctx context.Context, conn *pgxpool.Pool, sessionID string, maxBytes int) ([]SessionEventRow, error) {
	var newestFirst []SessionEventRow
	var total int
	cursor := int64(1<<63 - 1) // math.MaxInt64: no upper bound on the first page

	for {
		batch, err := fetchSessionEventsPage(ctx, conn, sessionID, cursor, sessionEventsTailBatchSize)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}

		done := false
		for _, r := range batch {
			newestFirst = append(newestFirst, r)

			if r.Type == "output" {
				decoded, err := base64.StdEncoding.DecodeString(r.Data)
				if err != nil {
					return nil, fmt.Errorf("decode event %d data: %w", r.ID, err)
				}
				total += len(decoded)
			}
			if total >= maxBytes {
				done = true
				break
			}
		}
		if done {
			break
		}

		cursor = batch[len(batch)-1].ID
		if len(batch) < sessionEventsTailBatchSize {
			// Reached the oldest event for this session; no more pages.
			break
		}
	}

	// Reverse to chronological (ascending) order.
	result := make([]SessionEventRow, len(newestFirst))
	for i, r := range newestFirst {
		result[len(newestFirst)-1-i] = r
	}
	return result, nil
}

// fetchSessionEventsPage fetches one newest-first page of a session's
// events with id < cursor, up to limit rows.
func fetchSessionEventsPage(ctx context.Context, conn *pgxpool.Pool, sessionID string, cursor int64, limit int) ([]SessionEventRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT id, session_id, type, data, seq, created_at
		  FROM session_events
		 WHERE session_id = $1
		   AND id < $2
		 ORDER BY id DESC
		 LIMIT $3
	`, sessionID, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var page []SessionEventRow
	for rows.Next() {
		var r SessionEventRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Type, &r.Data, &r.Seq, &r.CreatedAt); err != nil {
			return nil, err
		}
		page = append(page, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return page, nil
}

// ─── Push subscriptions ───────────────────────────────────────────────────────

// UpsertPushSubscription inserts or updates a push subscription by endpoint.
func UpsertPushSubscription(ctx context.Context, conn *pgxpool.Pool, endpoint, p256dh, auth string) error {
	_, err := conn.Exec(ctx, `
		INSERT INTO push_subscriptions (endpoint, p256dh, auth)
		VALUES ($1, $2, $3)
		ON CONFLICT (endpoint) DO UPDATE
		  SET p256dh = EXCLUDED.p256dh,
		      auth   = EXCLUDED.auth
	`, endpoint, p256dh, auth)
	return err
}

// ListPushSubscriptions returns all push subscriptions.
func ListPushSubscriptions(ctx context.Context, conn *pgxpool.Pool) ([]PushSubscriptionRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT id, endpoint, p256dh, auth, created_at
		  FROM push_subscriptions
		 ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []PushSubscriptionRow
	for rows.Next() {
		var r PushSubscriptionRow
		if err := rows.Scan(&r.ID, &r.Endpoint, &r.P256dh, &r.Auth, &r.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ─── Messages (session → user) ────────────────────────────────────────────────

// MessageRow is a session→user message. `answer` is overloaded: nil (an update,
// nothing to reply to), "" (closed/expired/session-ended), or real text (a reply).
type MessageRow struct {
	ID         string
	SessionID  string
	Kind       string // "update" | "ask" | "note"
	Body       string
	Status     string // "open" | "answered"
	Answer     *string
	CreatedAt  time.Time
	AnsweredAt *time.Time
}

// MessageBoardID returns the board the message's session is bound to, or ""
// when that session has no board. Returns pgx.ErrNoRows when the message does
// not exist.
func MessageBoardID(ctx context.Context, conn *pgxpool.Pool, messageID string) (string, error) {
	var boardID *string
	err := conn.QueryRow(ctx, `
		SELECT s.board_id
		  FROM messages m
		  JOIN sessions s ON s.id = m.session_id
		 WHERE m.id = $1`, messageID).Scan(&boardID)
	if err != nil {
		return "", err
	}
	if boardID == nil {
		return "", nil
	}
	return *boardID, nil
}

// CreateMessage inserts a message and returns its id. An "update" has nothing to
// reply to, so it is created already-answered; "ask"/"note" are created "open".
func CreateMessage(ctx context.Context, conn *pgxpool.Pool, sessionID, kind, body string) (string, error) {
	status := "open"
	if kind == "update" {
		status = "answered"
	}
	var id string
	err := conn.QueryRow(ctx, `
		INSERT INTO messages (session_id, kind, body, status, answered_at)
		VALUES ($1, $2, $3, $4, CASE WHEN $4 = 'answered' THEN now() ELSE NULL END)
		RETURNING id
	`, sessionID, kind, body, status).Scan(&id)
	return id, err
}

func scanMessageRows(rows pgx.Rows) ([]MessageRow, error) {
	defer rows.Close()
	var out []MessageRow
	for rows.Next() {
		var m MessageRow
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Kind, &m.Body, &m.Status,
			&m.Answer, &m.CreatedAt, &m.AnsweredAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const messageCols = `id, session_id, kind, body, status, answer, created_at, answered_at`

// GetMessage returns one message, or (nil, nil) if not found.
func GetMessage(ctx context.Context, conn *pgxpool.Pool, id string) (*MessageRow, error) {
	rows, err := conn.Query(ctx, `SELECT `+messageCols+` FROM messages WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	list, err := scanMessageRows(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// ListMessages returns every open message plus the most-recent `limit` overall,
// deduped, newest-first — the roll-up feed.
func ListMessages(ctx context.Context, conn *pgxpool.Pool, limit int) ([]MessageRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT `+messageCols+` FROM messages
		 WHERE status = 'open'
		    OR id IN (SELECT id FROM messages ORDER BY created_at DESC LIMIT $1)
		 ORDER BY created_at DESC
	`, limit)
	if err != nil {
		return nil, err
	}
	return scanMessageRows(rows)
}

// ListMessagesFor is ListMessages as one account may see it: messages of a
// private session other than the account's own are left out BEFORE the
// recent-history limit is applied, so another account's chatter cannot crowd
// this account's feed. An empty account sees no private session at all.
func ListMessagesFor(ctx context.Context, conn *pgxpool.Pool, limit int, account string) ([]MessageRow, error) {
	rows, err := conn.Query(ctx, `
		WITH v AS (
			SELECT m.* FROM messages m
			  JOIN sessions s ON s.id = m.session_id
			 WHERE NOT s.private
			    OR (s.spawning_account_id IS NOT NULL AND s.spawning_account_id = $2)
		)
		SELECT `+messageCols+` FROM v
		 WHERE status = 'open'
		    OR id IN (SELECT id FROM v ORDER BY created_at DESC LIMIT $1)
		 ORDER BY created_at DESC
	`, limit, account)
	if err != nil {
		return nil, err
	}
	return scanMessageRows(rows)
}

// ListMessagesBySession returns one session's messages, newest-first.
func ListMessagesBySession(ctx context.Context, conn *pgxpool.Pool, sessionID string) ([]MessageRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT `+messageCols+` FROM messages WHERE session_id = $1 ORDER BY created_at DESC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	return scanMessageRows(rows)
}

// AnswerMessage sets a message's answer and marks it answered, but only if it is
// still open. Returns whether a row changed (false = already answered/closed, or
// not found) so callers can 409 a double-reply.
func AnswerMessage(ctx context.Context, conn *pgxpool.Pool, id, answer string) (bool, error) {
	tag, err := conn.Exec(ctx, `
		UPDATE messages SET answer = $2, status = 'answered', answered_at = now()
		 WHERE id = $1 AND status = 'open'
	`, id, answer)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// CloseOpenMessagesForSession closes (answer=”) a session's still-open messages —
// used when the session ends, since a dead session can neither receive an `ask`
// answer nor have a `note` injected. Returns the rows it closed so the caller can
// broadcast them.
func CloseOpenMessagesForSession(ctx context.Context, conn *pgxpool.Pool, sessionID string) ([]MessageRow, error) {
	rows, err := conn.Query(ctx, `
		UPDATE messages SET answer = '', status = 'answered', answered_at = now()
		 WHERE session_id = $1 AND status = 'open'
		 RETURNING `+messageCols+`
	`, sessionID)
	if err != nil {
		return nil, err
	}
	return scanMessageRows(rows)
}

// ─── Agent events ─────────────────────────────────────────────────────────────

// AgentEventRow is one row from agent_events.
type AgentEventRow struct {
	SessionID     string
	Seq           int64
	ClientEventID string
	Kind          string
	Payload       string
	Ts            time.Time
}

// AppendAgentEvent persists an agent event, assigning the next per-session
// seq atomically. Idempotent on client_event_id: a duplicate returns the
// existing row's seq with inserted=false. A concurrent-appender collision on
// (session_id, seq) is retried a few times.
func AppendAgentEvent(ctx context.Context, conn *pgxpool.Pool, sessionID, clientEventID, kind, payload string) (int64, bool, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		var seq int64
		err := conn.QueryRow(ctx, `
			INSERT INTO agent_events (session_id, seq, client_event_id, kind, payload)
			VALUES ($1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM agent_events WHERE session_id = $1), $2, $3, $4)
			ON CONFLICT (session_id, client_event_id) DO NOTHING
			RETURNING seq
		`, sessionID, clientEventID, kind, payload).Scan(&seq)
		if err == nil {
			return seq, true, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// Duplicate client_event_id — return the existing seq.
			err = conn.QueryRow(ctx, `
				SELECT seq FROM agent_events WHERE session_id = $1 AND client_event_id = $2
			`, sessionID, clientEventID).Scan(&seq)
			if err != nil {
				return 0, false, err
			}
			return seq, false, nil
		}
		// unique(session_id, seq) race with a concurrent appender — retry.
		lastErr = err
	}
	return 0, false, fmt.Errorf("append agent event: %w", lastErr)
}

// ListAgentEvents returns events with seq > afterSeq in seq order, capped at
// limit (0 → 200, max 500).
func ListAgentEvents(ctx context.Context, conn *pgxpool.Pool, sessionID string, afterSeq int64, limit int) ([]AgentEventRow, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := conn.Query(ctx, `
		SELECT session_id, seq, client_event_id, kind, payload::text, ts
		  FROM agent_events
		 WHERE session_id = $1 AND seq > $2
		 ORDER BY seq
		 LIMIT $3
	`, sessionID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentEventRow
	for rows.Next() {
		var r AgentEventRow
		if err := rows.Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAgentEventsBefore returns up to limit events of the session with seq below
// beforeSeq, oldest first: the newest such events, so beforeSeq 0 means the very
// end of the transcript. more says whether earlier events remain.
func ListAgentEventsBefore(ctx context.Context, conn *pgxpool.Pool, sessionID string, beforeSeq int64, limit int) (rows []AgentEventRow, more bool, err error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	// One extra row says whether anything older is left.
	res, err := conn.Query(ctx, `
		SELECT session_id, seq, client_event_id, kind, payload::text, ts
		  FROM agent_events
		 WHERE session_id = $1 AND ($2 = 0 OR seq < $2)
		 ORDER BY seq DESC
		 LIMIT $3
	`, sessionID, beforeSeq, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer res.Close()
	for res.Next() {
		var r AgentEventRow
		if err := res.Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts); err != nil {
			return nil, false, err
		}
		rows = append(rows, r)
	}
	if err := res.Err(); err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		more = true
		rows = rows[:limit]
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, more, nil
}

// SetSessionEngine records a session's CLI engine ("" (claude) | "codex" | "hermes").
func SetSessionEngine(ctx context.Context, conn *pgxpool.Pool, sessionID, engine string) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET engine = $2 WHERE id = $1`, sessionID, engine)
	return err
}

// SetSessionKind records a session's kind ("tmux" | "agent").
func SetSessionKind(ctx context.Context, conn *pgxpool.Pool, sessionID, kind string) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET kind = $2 WHERE id = $1`, sessionID, kind)
	return err
}

// SetSessionSpawningAccount records the verified account id (identity.Principal.Sub)
// that requested a cluster-runtime session, so a later resume
// (resumeClusterSession) can look up the same account's personal credential.
func SetSessionSpawningAccount(ctx context.Context, conn *pgxpool.Pool, sessionID, accountID string) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET spawning_account_id = $2 WHERE id = $1`, sessionID, accountID)
	return err
}

// SetSessionTokenID records the agent token (identity.Principal.Sub of an
// agent-kind token) that started this session. It travels with every
// credential fetch so blerg-core can check the token is still live — an
// account id alone is not proof the caller may still act for that account.
// Never set for human or runner-key starts, which have no token of their own.
func SetSessionTokenID(ctx context.Context, conn *pgxpool.Pool, sessionID, tokenID string) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET token_id = NULLIF($2, '') WHERE id = $1`, sessionID, tokenID)
	return err
}

// SetSessionStartedBy records who started a session (migration 037): the start's principal
// kind and, for an agent token, the owner's label for it. Empty strings leave the columns as
// they are (a person's start writes nothing).
func SetSessionStartedBy(ctx context.Context, conn *pgxpool.Pool, sessionID, kind, name string) error {
	if kind == "" {
		return nil
	}
	_, err := conn.Exec(ctx, `UPDATE sessions SET started_by_kind = $2, started_by_name = $3 WHERE id = $1`, sessionID, kind, name)
	return err
}

// SetSessionInteraction records a session's interaction mode (migration 038): "interactive"
// or "unattended", as the server resolved it at start. A cluster resume reads it back from the
// row. An empty mode writes nothing.
func SetSessionInteraction(ctx context.Context, conn *pgxpool.Pool, sessionID, mode string) error {
	if mode == "" {
		return nil
	}
	_, err := conn.Exec(ctx, `UPDATE sessions SET interaction = $2 WHERE id = $1`, sessionID, mode)
	return err
}

// SetSessionCallback stores the completion webhook for a session. The secret
// is stored raw because it must sign the delivery (HMAC-SHA256); it is never
// returned by any endpoint and never logged.
func SetSessionCallback(ctx context.Context, conn *pgxpool.Pool, sessionID, callbackURL, callbackSecret string) error {
	_, err := conn.Exec(ctx, `
		UPDATE sessions
		   SET callback_url    = NULLIF($2, ''),
		       callback_secret = NULLIF($3, '')
		 WHERE id = $1
	`, sessionID, callbackURL, callbackSecret)
	return err
}

// SetSessionAutoStop records that this session is one-shot: the runner ends it
// itself once its first turn is done (migration 017).
func SetSessionAutoStop(ctx context.Context, conn *pgxpool.Pool, sessionID string, autoStop bool) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET auto_stop = $2 WHERE id = $1`, sessionID, autoStop)
	return err
}

// ClaimSessionAutoStop atomically claims the right to auto-stop a session,
// returning true to exactly one caller.
//
// The claim IS the transition to "ended": doing it in one conditional UPDATE
// is what makes a second turn_done — a re-delivered event, a session that
// somehow produces another turn, or two runner replicas seeing the same one —
// a no-op rather than a second stop, and what skips a session that is already
// over (stopped by hand, failed, or finished some other way). A session that
// never asked to be one-shot is never claimed at all.
//
// ended_at is stamped here for the same reason: a row that says "ended" with
// no end time would be a lie told by the claim itself. The rest of the stop
// (killing the runtime, the completion webhook, revoking the session's tokens)
// follows the claim and is best-effort, exactly as it is for an explicit stop.
func ClaimSessionAutoStop(ctx context.Context, conn *pgxpool.Pool, sessionID string) (bool, error) {
	if conn == nil {
		return false, nil
	}
	var claimed int
	err := conn.QueryRow(ctx, `
		UPDATE sessions
		   SET status            = 'ended',
		       ended_at          = now(),
		       status_changed_at = now(),
		       end_reason        = coalesce(end_reason, 'auto_stopped')
		 WHERE id = $1
		   AND auto_stop
		   AND status NOT IN ('ended', 'stopped', 'error')
		RETURNING 1
	`, sessionID).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ClaimSessionWebhook atomically claims the right to deliver a session's
// completion webhook, returning true to exactly one caller. Several independent
// paths can mark the same session terminal (daemon session_ended, a disconnect
// marked "error", the stop endpoint, reconciliation) and each one notifies, so
// the claim — not the caller — is what makes delivery happen once.
//
// It is a conditional UPDATE rather than a read-then-write because two runner
// replicas can be doing this at the same instant; the row lock the UPDATE takes
// is the whole mutual exclusion. A claim is made BEFORE the POST: a delivery
// that crashes mid-flight is not retried by a later transition, which is the
// right trade for a webhook whose body says "this session is over" — a
// duplicate would be reported to the broker as a second completion.
func ClaimSessionWebhook(ctx context.Context, conn *pgxpool.Pool, sessionID string) (bool, error) {
	if conn == nil {
		return false, nil
	}
	var claimed int
	err := conn.QueryRow(ctx, `
		UPDATE sessions
		   SET webhook_delivered_at = now()
		 WHERE id = $1 AND webhook_delivered_at IS NULL
		RETURNING 1
	`, sessionID).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SetSessionGitURL records the clone URL a session was actually started with,
// so the result endpoint reports what ran rather than re-deriving a URL a
// cross-org override may have replaced.
func SetSessionGitURL(ctx context.Context, conn *pgxpool.Pool, sessionID, gitURL string) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET git_url = NULLIF($2, '') WHERE id = $1`, sessionID, gitURL)
	return err
}

// SetSessionRestrictTools records that a session runs restricted (migration 036): a cron's, or
// one the board started with MCP connections. A cluster resume reads it back.
func SetSessionRestrictTools(ctx context.Context, conn *pgxpool.Pool, sessionID string) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET restrict_tools = true WHERE id = $1`, sessionID)
	return err
}

// GetSessionRestrictTools reports whether a session was started restricted. A session that does
// not exist, or any error, is false.
func GetSessionRestrictTools(ctx context.Context, conn *pgxpool.Pool, sessionID string) bool {
	var v bool
	if err := conn.QueryRow(ctx, `SELECT restrict_tools FROM sessions WHERE id = $1`, sessionID).Scan(&v); err != nil {
		return false
	}
	return v
}

// SetSessionNewRepo marks a session as launched as "New repository" (migration 035).
func SetSessionNewRepo(ctx context.Context, conn *pgxpool.Pool, sessionID string) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET new_repo = true WHERE id = $1`, sessionID)
	return err
}

// GetSessionNewRepo reports whether a session was launched as "New repository". A session that
// does not exist, or any error, is false.
func GetSessionNewRepo(ctx context.Context, conn *pgxpool.Pool, sessionID string) bool {
	var v bool
	if err := conn.QueryRow(ctx, `SELECT new_repo FROM sessions WHERE id = $1`, sessionID).Scan(&v); err != nil {
		return false
	}
	return v
}

// SetSessionPosture records how a session was launched — the runtime it runs
// under and whether engine permission prompts were bypassed. Called right
// after the session row is pre-created, before the spawn_session message
// reaches the daemon, so a crash between the two can't lose the posture
// facts the UI states plainly (R5).
func SetSessionPosture(ctx context.Context, conn *pgxpool.Pool, sessionID, runtime string, skip bool) error {
	_, err := conn.Exec(ctx, `UPDATE sessions SET runtime = $2, skip_permissions = $3 WHERE id = $1`, sessionID, runtime, skip)
	return err
}

// UpdateSessionModelEffort mirrors an agent session's live model/effort state.
func UpdateSessionModelEffort(ctx context.Context, conn *pgxpool.Pool, sessionID, model, effort string) error {
	_, err := conn.Exec(ctx, `
		UPDATE sessions
		   SET model  = NULLIF($2, ''),
		       effort = NULLIF($3, '')
		 WHERE id = $1
	`, sessionID, model, effort)
	return err
}

// ─── Agent data plane ─────────────────────────────────────────────────────────

// AgentMemoryRow is one row from agent_memories.
type AgentMemoryRow struct {
	ID        string
	Project   string
	Name      string
	Kind      string
	Content   string
	Embedding []float32
	UpdatedAt time.Time
}

// AgentRuleRow is one row from agent_rules.
type AgentRuleRow struct {
	ID        string
	Project   string
	Content   string
	Enabled   bool
	CreatedAt time.Time
}

// UpsertAgentMemory writes a memory (upsert by project+name).
func UpsertAgentMemory(ctx context.Context, conn *pgxpool.Pool, project, name, kind, content string, embedding []float32) error {
	_, err := conn.Exec(ctx, `
		INSERT INTO agent_memories (project, name, kind, content, embedding, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (project, name) DO UPDATE
		  SET kind = EXCLUDED.kind, content = EXCLUDED.content,
		      embedding = EXCLUDED.embedding, updated_at = now()
	`, project, name, kind, content, embedding)
	return err
}

// DeleteAgentMemory removes a memory by project+name.
func DeleteAgentMemory(ctx context.Context, conn *pgxpool.Pool, project, name string) error {
	_, err := conn.Exec(ctx, `DELETE FROM agent_memories WHERE project = $1 AND name = $2`, project, name)
	return err
}

// ListAgentMemories returns a project's memories (embeddings included so the
// caller can score similarity in-process).
func ListAgentMemories(ctx context.Context, conn *pgxpool.Pool, project string) ([]AgentMemoryRow, error) {
	rows, err := conn.Query(ctx, `
		SELECT id, project, name, kind, content, embedding, updated_at
		  FROM agent_memories WHERE project = $1 ORDER BY name
	`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentMemoryRow
	for rows.Next() {
		var r AgentMemoryRow
		if err := rows.Scan(&r.ID, &r.Project, &r.Name, &r.Kind, &r.Content, &r.Embedding, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertAgentRule adds a rule; enabled=false until a human approves.
func InsertAgentRule(ctx context.Context, conn *pgxpool.Pool, project, content string) (string, error) {
	var id string
	err := conn.QueryRow(ctx, `
		INSERT INTO agent_rules (project, content) VALUES ($1, $2) RETURNING id
	`, project, content).Scan(&id)
	return id, err
}

// ListAgentRules returns a project's rules, optionally only enabled ones.
func ListAgentRules(ctx context.Context, conn *pgxpool.Pool, project string, enabledOnly bool) ([]AgentRuleRow, error) {
	q := `SELECT id, project, content, enabled, created_at FROM agent_rules WHERE project = $1`
	if enabledOnly {
		q += ` AND enabled`
	}
	q += ` ORDER BY created_at`
	rows, err := conn.Query(ctx, q, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentRuleRow
	for rows.Next() {
		var r AgentRuleRow
		if err := rows.Scan(&r.ID, &r.Project, &r.Content, &r.Enabled, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetAgentRuleEnabled flips a rule's enabled flag (human approval action).
func SetAgentRuleEnabled(ctx context.Context, conn *pgxpool.Pool, id string, enabled bool) error {
	tag, err := conn.Exec(ctx, `UPDATE agent_rules SET enabled = $2 WHERE id = $1`, id, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("not found: %w", pgx.ErrNoRows)
	}
	return nil
}

// DeleteAgentRule removes a rule.
func DeleteAgentRule(ctx context.Context, conn *pgxpool.Pool, id string) error {
	_, err := conn.Exec(ctx, `DELETE FROM agent_rules WHERE id = $1`, id)
	return err
}

// ─── runner_idempotency (agent contract v1) ──────────────────────────────────

// ClaimIdempotencyKey reserves (scope, key) for sessionID/requestHash in one
// statement, so two concurrent retries of the same start cannot both create a
// session. It returns the row that now holds the key:
//
//   - claimed=true  — the caller won it and must go on to start sessionID.
//   - claimed=false — an unexpired row already held it; `existing` is that row,
//     so the caller replays its session (same request_hash) or 409s (different).
//
// A row older than ttl is treated as absent and overwritten in place, which is
// also how expired keys get collected on a busy scope; PruneIdempotency sweeps
// the rest.
func ClaimIdempotencyKey(ctx context.Context, conn *pgxpool.Pool, scope, key, requestHash, sessionID string, ttl time.Duration) (existing *IdempotencyRow, claimed bool, err error) {
	var row IdempotencyRow
	err = conn.QueryRow(ctx, `
		INSERT INTO runner_idempotency (scope, key, request_hash, session_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (scope, key) DO UPDATE
		   SET request_hash = EXCLUDED.request_hash,
		       session_id   = EXCLUDED.session_id,
		       created_at   = now()
		 WHERE runner_idempotency.created_at < now() - $5::interval
		RETURNING scope, key, request_hash, session_id, created_at
	`, scope, key, requestHash, sessionID, ttl.String()).
		Scan(&row.Scope, &row.Key, &row.RequestHash, &row.SessionID, &row.CreatedAt)
	if err == nil {
		return &row, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	// The conflicting row was still fresh: the DO UPDATE's WHERE suppressed
	// the write and RETURNING produced nothing. Read back what holds the key.
	err = conn.QueryRow(ctx, `
		SELECT scope, key, request_hash, session_id, created_at
		  FROM runner_idempotency
		 WHERE scope = $1 AND key = $2
	`, scope, key).Scan(&row.Scope, &row.Key, &row.RequestHash, &row.SessionID, &row.CreatedAt)
	if err != nil {
		// Vanished between the two statements (a concurrent prune): report
		// it as unclaimed with no existing row so the caller can retry.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &row, false, nil
}

// ReleaseIdempotencyKey drops a claim whose start did not survive (the spawn
// failed), so the caller's retry is not answered with a session that never
// existed. Deleting a missing row is a no-op.
func ReleaseIdempotencyKey(ctx context.Context, conn *pgxpool.Pool, scope, key, sessionID string) error {
	_, err := conn.Exec(ctx,
		`DELETE FROM runner_idempotency WHERE scope = $1 AND key = $2 AND session_id = $3`,
		scope, key, sessionID)
	return err
}

// PruneIdempotency deletes records past their retention window. Called
// opportunistically from the start path — there is no scheduler here, and the
// table is small enough that a bounded sweep on write is enough.
func PruneIdempotency(ctx context.Context, conn *pgxpool.Pool, ttl time.Duration) error {
	_, err := conn.Exec(ctx,
		`DELETE FROM runner_idempotency WHERE created_at < now() - $1::interval`, ttl.String())
	return err
}

// ListAgentEventsTail returns a session's newest events of one kind,
// newest-first, capped at limit. It exists for the runner result endpoint's
// "last assistant message": walking the whole transcript forward to find the
// final turn would read every event of a long session for one string.
func ListAgentEventsTail(ctx context.Context, conn *pgxpool.Pool, sessionID, kind string, limit int) ([]AgentEventRow, error) {
	if conn == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := conn.Query(ctx, `
		SELECT session_id, seq, client_event_id, kind, payload::text, ts
		  FROM agent_events
		 WHERE session_id = $1 AND kind = $2
		 ORDER BY seq DESC
		 LIMIT $3
	`, sessionID, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentEventRow
	for rows.Next() {
		var r AgentEventRow
		if err := rows.Scan(&r.SessionID, &r.Seq, &r.ClientEventID, &r.Kind, &r.Payload, &r.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
