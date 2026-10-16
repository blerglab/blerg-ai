package db

// Tool changes (docs/design/mcp-tool-changes.md §3.3): what a person reviews when an MCP server's
// tool definition drifts from a session's pin, when it offers a tool the launch never saw, or when
// it stops offering a pinned tool. One pending row per (session, connection, tool), enforced by
// the partial unique index mcp_tool_changes_pending; the gateway records idempotently and the
// review routes decide. Nothing here holds a definition: those live in mcp_tool_definitions by hash.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Kinds of change.
const (
	ToolChangeChanged = "changed"
	ToolChangeNew     = "new"
	ToolChangeRemoved = "removed"
)

// States of a change.
const (
	ToolChangePending    = "pending"
	ToolChangeAccepted   = "accepted"
	ToolChangeDeclined   = "declined"
	ToolChangeSuperseded = "superseded"
	ToolChangeExpired    = "expired"
)

// ToolChangeExpiry is how long a change stays pending; afterwards the tool stays off and the same
// hash is not asked again in that session.
const ToolChangeExpiry = 7 * 24 * time.Hour

// Caps on pending changes, so a hostile server cannot fill the page (spec §3.3). Higher than the
// proposals' caps because one server release can add many tools.
const (
	DefaultMaxPendingChangesPerSession = 50
	DefaultMaxPendingChangesPerAccount = 200
)

// MaxOpenToolChangesListed bounds the unpaged open list.
const MaxOpenToolChangesListed = 500

var (
	// ErrToolChangeNotFound: no such change, malformed id, or another account's. The three are
	// deliberately the same.
	ErrToolChangeNotFound = errors.New("tool change not found")
	// ErrToolChangeState: the change exists but is not pending.
	ErrToolChangeState = errors.New("tool change is not pending")
	// ErrToolChangeCursor: a malformed page cursor.
	ErrToolChangeCursor = errors.New("invalid cursor")
)

// ToolChange is a row of mcp_tool_changes. OldHash is "" for new, NewHash "" for removed.
type ToolChange struct {
	ID             string
	AccountID      string
	SessionID      string
	ConnectionID   string
	ConnectionName string
	Tool           string
	Kind           string
	OldHash        string
	NewHash        string
	Mode           string
	Muted          bool
	State          string
	CreatedAt      time.Time
	DecidedAt      *time.Time
	DecidedBy      *string
}

// ToolChangeLimits caps pending rows per session and per account; zero means no cap.
type ToolChangeLimits struct{ Session, Account int }

// RecordOutcome says what RecordToolChange did. Exactly one of Inserted, AlreadyPending, Blocked
// and Capped is true.
type RecordOutcome struct {
	Inserted       bool // a new pending row (ID set)
	AlreadyPending bool // the same (kind, new_hash) was already pending (ID set)
	Blocked        bool // this hash was declined or expired in this session: never asked again
	Capped         bool // a cap is reached; nothing recorded
	ID             string
}

const toolChangeColumns = `id::text, account_id, session_id::text, connection_id::text, connection_name, tool, kind,
	COALESCE(old_hash, ''), COALESCE(new_hash, ''), mode, muted, state, created_at, decided_at, decided_by`

func scanToolChange(row pgx.Row) (*ToolChange, error) {
	var c ToolChange
	if err := row.Scan(&c.ID, &c.AccountID, &c.SessionID, &c.ConnectionID, &c.ConnectionName, &c.Tool, &c.Kind,
		&c.OldHash, &c.NewHash, &c.Mode, &c.Muted, &c.State, &c.CreatedAt, &c.DecidedAt, &c.DecidedBy); err != nil {
		return nil, err
	}
	return &c, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RecordToolChange records drift for one tool, idempotently, in one transaction under a
// per-session advisory lock:
//
//  1. a hash the person declined, or that expired, in this session is never recorded again
//     (Blocked);
//  2. a pending row with the same kind and hash is left alone (AlreadyPending);
//  3. a pending row with another kind or hash is superseded;
//  4. the caps are checked (Capped): past a cap nothing is recorded and nothing is superseded;
//  5. the row is inserted, muted when the person declined an earlier hash of this tool here.
//
// Concurrent calls for one session are serialised by the advisory lock, so two different hashes
// recorded at once leave exactly one pending row: the second call supersedes the first's row.
// The partial unique index is the backstop for a writer outside this function.
func RecordToolChange(ctx context.Context, pool *pgxpool.Pool, c ToolChange, lim ToolChangeLimits) (RecordOutcome, error) {
	var out RecordOutcome
	switch {
	case c.AccountID == "" || !proposalUUID.MatchString(c.SessionID) || !proposalUUID.MatchString(c.ConnectionID) || c.Tool == "":
		return out, errors.New("RecordToolChange: account, session, connection and tool are required")
	case c.Kind != ToolChangeChanged && c.Kind != ToolChangeNew && c.Kind != ToolChangeRemoved:
		return out, fmt.Errorf("RecordToolChange: unknown kind %q", c.Kind)
	case c.Kind != ToolChangeRemoved && c.NewHash == "":
		return out, errors.New("RecordToolChange: a changed or new tool needs its new hash")
	case c.Kind == ToolChangeRemoved && c.NewHash != "":
		return out, errors.New("RecordToolChange: a removed tool has no new hash")
	}
	if c.Mode != "allow" {
		c.Mode = "propose"
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("RecordToolChange begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('mcp_tool_changes:' || $1))`, c.SessionID); err != nil {
		return out, fmt.Errorf("RecordToolChange lock: %w", err)
	}
	if c.Kind != ToolChangeRemoved {
		var blocked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mcp_tool_changes
			WHERE session_id = $1::uuid AND connection_id = $2::uuid AND tool = $3 AND new_hash = $4 AND state IN ('declined','expired'))`,
			c.SessionID, c.ConnectionID, c.Tool, c.NewHash).Scan(&blocked); err != nil {
			return out, fmt.Errorf("RecordToolChange blocked: %w", err)
		}
		if blocked {
			out.Blocked = true
			return out, tx.Commit(ctx)
		}
	}
	var pendingID, pendingKind, pendingHash string
	err = tx.QueryRow(ctx, `SELECT id::text, kind, COALESCE(new_hash, '') FROM mcp_tool_changes
		WHERE session_id = $1::uuid AND connection_id = $2::uuid AND tool = $3 AND state = 'pending'`,
		c.SessionID, c.ConnectionID, c.Tool).Scan(&pendingID, &pendingKind, &pendingHash)
	switch {
	case err == nil && pendingKind == c.Kind && pendingHash == c.NewHash:
		out.AlreadyPending, out.ID = true, pendingID
		return out, tx.Commit(ctx)
	case err == nil:
		if _, err := tx.Exec(ctx, `UPDATE mcp_tool_changes SET state = 'superseded', decided_at = now(), decided_by = 'system:superseded'
			WHERE id = $1::uuid AND state = 'pending'`, pendingID); err != nil {
			return out, fmt.Errorf("RecordToolChange supersede: %w", err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return out, fmt.Errorf("RecordToolChange pending lookup: %w", err)
	}
	var muted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mcp_tool_changes
		WHERE session_id = $1::uuid AND connection_id = $2::uuid AND tool = $3 AND state = 'declined')`,
		c.SessionID, c.ConnectionID, c.Tool).Scan(&muted); err != nil {
		return out, fmt.Errorf("RecordToolChange muted: %w", err)
	}
	if lim.Session > 0 || lim.Account > 0 {
		var inSession, inAccount int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM mcp_tool_changes WHERE session_id = $1::uuid AND state = 'pending'),
			(SELECT count(*) FROM mcp_tool_changes WHERE account_id = $2 AND state = 'pending')`,
			c.SessionID, c.AccountID).Scan(&inSession, &inAccount); err != nil {
			return out, fmt.Errorf("RecordToolChange count: %w", err)
		}
		if (lim.Session > 0 && inSession >= lim.Session) || (lim.Account > 0 && inAccount >= lim.Account) {
			// Return without committing: the deferred Rollback undoes step 3's supersede, so a
			// pending card is never closed with nothing recorded in its place.
			out.Capped = true
			return out, nil
		}
	}
	var id *string
	err = tx.QueryRow(ctx, `
		INSERT INTO mcp_tool_changes (account_id, session_id, connection_id, connection_name, tool, kind, old_hash, new_hash, mode, muted)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (session_id, connection_id, tool) WHERE state = 'pending' DO NOTHING
		RETURNING id::text`,
		c.AccountID, c.SessionID, c.ConnectionID, c.ConnectionName, c.Tool, c.Kind,
		nullIfEmpty(c.OldHash), nullIfEmpty(c.NewHash), c.Mode, muted).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Unreachable while every insert for a session runs under the session's advisory lock
		// (taken above): a pending row appearing between the lookup and the insert would be a
		// lock bug. Say so rather than report a row of unknown hash as pending.
		return out, errors.New("RecordToolChange: lost a pending-row race under the session lock")
	case err != nil:
		return out, fmt.Errorf("RecordToolChange insert: %w", err)
	default:
		out.Inserted, out.ID = true, *id
	}
	return out, tx.Commit(ctx)
}

// SupersedePendingToolChange closes the tool's pending change (the live hash returned to the pin,
// or a removed tool reappeared). It reports whether there was one.
func SupersedePendingToolChange(ctx context.Context, q DBTX, sessionID, connectionID, tool string) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE mcp_tool_changes SET state = 'superseded', decided_at = now(), decided_by = 'system:superseded'
		WHERE session_id = $1::uuid AND connection_id = $2::uuid AND tool = $3 AND state = 'pending'`, sessionID, connectionID, tool)
	if err != nil {
		return false, fmt.Errorf("SupersedePendingToolChange: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// GetOwnedToolChange reads one of the account's changes, or ErrToolChangeNotFound.
func GetOwnedToolChange(ctx context.Context, q DBTX, accountID, id string) (*ToolChange, error) {
	if !proposalUUID.MatchString(id) {
		return nil, ErrToolChangeNotFound
	}
	c, err := scanToolChange(q.QueryRow(ctx, `SELECT `+toolChangeColumns+` FROM mcp_tool_changes WHERE id = $1::uuid AND account_id = $2`, id, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrToolChangeNotFound
	}
	return c, err
}

func queryToolChanges(ctx context.Context, q DBTX, sql string, args ...any) ([]ToolChange, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list tool changes: %w", err)
	}
	defer rows.Close()
	out := []ToolChange{}
	for rows.Next() {
		c, err := scanToolChange(rows)
		if err != nil {
			return nil, fmt.Errorf("list tool changes scan: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListOpenToolChanges lists the account's pending changes, unmuted first then newest first,
// optionally for one session ("" for all), bounded by MaxOpenToolChangesListed.
func ListOpenToolChanges(ctx context.Context, q DBTX, accountID, sessionID string) ([]ToolChange, error) {
	if sessionID != "" && !proposalUUID.MatchString(sessionID) {
		return []ToolChange{}, nil
	}
	return queryToolChanges(ctx, q, `SELECT `+toolChangeColumns+` FROM mcp_tool_changes
		WHERE account_id = $1 AND state = 'pending' AND ($2 = '' OR session_id = $2::uuid)
		ORDER BY muted, created_at DESC, id DESC LIMIT $3`, accountID, sessionID, MaxOpenToolChangesListed)
}

// ListDecidedToolChanges lists decided changes newest first, one page after cursor ("" first);
// next is the cursor for the following page ("" on the last). The cursor has the proposals' shape.
func ListDecidedToolChanges(ctx context.Context, q DBTX, accountID, sessionID, cursor string, limit int) ([]ToolChange, string, error) {
	if sessionID != "" && !proposalUUID.MatchString(sessionID) {
		return []ToolChange{}, "", nil
	}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	var before, beforeID any
	if cursor != "" {
		t, id, err := parseDecidedCursor(cursor)
		if err != nil {
			return nil, "", ErrToolChangeCursor
		}
		before, beforeID = t, id
	}
	rows, err := queryToolChanges(ctx, q, `SELECT `+toolChangeColumns+` FROM mcp_tool_changes
		WHERE account_id = $1 AND state <> 'pending' AND ($2 = '' OR session_id = $2::uuid)
		  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3::timestamptz, $4::uuid))
		ORDER BY created_at DESC, id DESC LIMIT $5`, accountID, sessionID, before, beforeID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
	}
	return rows, next, nil
}

// ToolChangeHashBlocked reports whether the person declined, or the 7-day window expired, a change
// to this hash for this tool in this session: such a hash is never re-pinned by a policy and never
// asked again.
func ToolChangeHashBlocked(ctx context.Context, q DBTX, sessionID, connectionID, tool, hash string) (bool, error) {
	if !proposalUUID.MatchString(sessionID) || !proposalUUID.MatchString(connectionID) || tool == "" || hash == "" {
		return false, nil
	}
	var blocked bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mcp_tool_changes
		WHERE session_id = $1::uuid AND connection_id = $2::uuid AND tool = $3 AND new_hash = $4 AND state IN ('declined','expired'))`,
		sessionID, connectionID, tool, hash).Scan(&blocked)
	if err != nil {
		return false, fmt.Errorf("ToolChangeHashBlocked: %w", err)
	}
	return blocked, nil
}

// CountPendingToolChanges is how many changes wait for a decision from the account (the badge):
// unmuted, and not `removed` (informational, nothing to decide).
func CountPendingToolChanges(ctx context.Context, q DBTX, accountID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM mcp_tool_changes WHERE account_id = $1 AND state = 'pending' AND NOT muted AND kind <> 'removed'`, accountID).Scan(&n)
	return n, err
}

// CountPendingToolChangesForSession is the same count for one session.
func CountPendingToolChangesForSession(ctx context.Context, q DBTX, accountID, sessionID string) (int, error) {
	if !proposalUUID.MatchString(sessionID) {
		return 0, nil
	}
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM mcp_tool_changes WHERE account_id = $1 AND session_id = $2::uuid AND state = 'pending' AND NOT muted AND kind <> 'removed'`,
		accountID, sessionID).Scan(&n)
	return n, err
}

// transitionToolChange moves an owner's pending change to a decided state atomically.
func transitionToolChange(ctx context.Context, q DBTX, accountID, id, to, decidedBy string) (*ToolChange, error) {
	if !proposalUUID.MatchString(id) {
		return nil, ErrToolChangeNotFound
	}
	c, err := scanToolChange(q.QueryRow(ctx, `UPDATE mcp_tool_changes SET state = $3, decided_at = now(), decided_by = $4
		WHERE id = $1::uuid AND account_id = $2 AND state = 'pending' RETURNING `+toolChangeColumns, id, accountID, to, decidedBy))
	if errors.Is(err, pgx.ErrNoRows) {
		var one int
		if err := q.QueryRow(ctx, `SELECT 1 FROM mcp_tool_changes WHERE id = $1::uuid AND account_id = $2`, id, accountID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrToolChangeNotFound
		} else if err != nil {
			return nil, err
		}
		return nil, ErrToolChangeState
	}
	return c, err
}

// AcceptToolChange is the accept's compare-and-set: pending -> accepted for the owner. Run it in
// the same transaction as the grant write it records.
func AcceptToolChange(ctx context.Context, q DBTX, accountID, id, decidedBy string) (*ToolChange, error) {
	return transitionToolChange(ctx, q, accountID, id, ToolChangeAccepted, decidedBy)
}

// DeclineToolChange moves a pending change to declined. It writes no pin event; use the
// WithEvent wrapper unless you are inside a transaction that writes it.
func DeclineToolChange(ctx context.Context, q DBTX, accountID, id, decidedBy string) (*ToolChange, error) {
	return transitionToolChange(ctx, q, accountID, id, ToolChangeDeclined, decidedBy)
}

// ExpireToolChange closes a pending change whose session is gone (the accept's 410). It writes no
// pin event; use the WithEvent wrapper unless you are inside a transaction that writes it.
func ExpireToolChange(ctx context.Context, q DBTX, accountID, id string) (*ToolChange, error) {
	return transitionToolChange(ctx, q, accountID, id, ToolChangeExpired, "system:expired")
}

// DeclineToolChangeWithEvent declines the owner's pending change and writes its `declined` pin
// event in one transaction.
func DeclineToolChangeWithEvent(ctx context.Context, pool *pgxpool.Pool, accountID, id, decidedBy string) (*ToolChange, error) {
	return transitionWithEvent(ctx, pool, PinDeclined, decidedBy, func(tx DBTX) (*ToolChange, error) {
		return DeclineToolChange(ctx, tx, accountID, id, decidedBy)
	})
}

// ExpireToolChangeWithEvent expires the owner's pending change (its session ended) and writes its
// `expired` pin event (actor "system") in one transaction.
func ExpireToolChangeWithEvent(ctx context.Context, pool *pgxpool.Pool, accountID, id string) (*ToolChange, error) {
	return transitionWithEvent(ctx, pool, PinExpired, "system", func(tx DBTX) (*ToolChange, error) {
		return ExpireToolChange(ctx, tx, accountID, id)
	})
}

func transitionWithEvent(ctx context.Context, pool *pgxpool.Pool, action, actor string, transition func(tx DBTX) (*ToolChange, error)) (*ToolChange, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("tool change %s begin: %w", action, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	c, err := transition(tx)
	if err != nil {
		return nil, err
	}
	if err := InsertPinEvent(ctx, tx, PinEvent{AccountID: c.AccountID, SessionID: c.SessionID, ConnectionID: c.ConnectionID,
		ConnectionName: c.ConnectionName, Tool: c.Tool, Action: action, OldHash: c.OldHash, NewHash: c.NewHash, Mode: c.Mode, Actor: actor}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("tool change %s commit: %w", action, err)
	}
	return c, nil
}

// ExpirePendingToolChanges expires changes pending longer than maxAge (spec: 7 days) and writes
// one `expired` pin event per row, in one statement. The tool stays off; the gateway's refusal
// text reverts to "start a new session" and the hash is not asked again in that session.
func ExpirePendingToolChanges(ctx context.Context, pool *pgxpool.Pool, maxAge time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `
		WITH x AS (
		  UPDATE mcp_tool_changes SET state = 'expired', decided_at = now(), decided_by = 'system:expired'
		   WHERE state = 'pending' AND created_at < now() - make_interval(secs => $1)
		  RETURNING account_id, session_id, connection_id, connection_name, tool, old_hash, new_hash, mode
		)
		INSERT INTO mcp_pin_events (account_id, session_id, connection_id, connection_name, tool, action, old_hash, new_hash, mode, actor)
		SELECT account_id, session_id, connection_id, connection_name, tool, 'expired', old_hash, new_hash, mode, 'system' FROM x`,
		maxAge.Seconds())
	if err != nil {
		return 0, fmt.Errorf("ExpirePendingToolChanges: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PruneDecidedToolChanges deletes decided changes older than retention after their decision.
func PruneDecidedToolChanges(ctx context.Context, pool *pgxpool.Pool, retention time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM mcp_tool_changes
		WHERE state <> 'pending' AND decided_at < now() - make_interval(secs => $1)`, retention.Seconds())
	if err != nil {
		return 0, fmt.Errorf("PruneDecidedToolChanges: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ToolChangeDecidedCursor is exported for the API's paging tests.
func ToolChangeDecidedCursor(c *ToolChange) string {
	return c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + strings.TrimSpace(c.ID)
}
