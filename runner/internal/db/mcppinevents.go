package db

// Pin events (docs/design/mcp-tool-changes.md §3.4): the audit of every change to what a session's
// grant pins. A person re-pinned or granted (accept), the auto_accept policy did, a person
// declined, a row expired, a flapping server was throttled. Never holds a definition or an
// argument. A grant write and its event are ONE transaction (RepinToolWithEvent,
// GrantToolWithEvent): a re-pin without its event cannot happen.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Actions.
const (
	PinRepinned  = "repinned"
	PinGranted   = "granted"
	PinDeclined  = "declined"
	PinExpired   = "expired"
	PinThrottled = "throttled"
)

// PinEventRetention is how long events are kept (the credential audit's retention).
const PinEventRetention = 180 * 24 * time.Hour

var pinActions = map[string]bool{PinRepinned: true, PinGranted: true, PinDeclined: true, PinExpired: true, PinThrottled: true}

// PinEvent is a row of mcp_pin_events. Actor is an account id, "policy:auto_accept" or "system".
type PinEvent struct {
	ID             int64
	AccountID      string
	SessionID      string
	ConnectionID   string
	ConnectionName string
	Tool           string
	Action         string
	OldHash        string
	NewHash        string
	Mode           string
	Actor          string
	CreatedAt      time.Time
}

// InsertPinEvent records one event.
func InsertPinEvent(ctx context.Context, q DBTX, e PinEvent) error {
	switch {
	case !pinActions[e.Action]:
		return fmt.Errorf("InsertPinEvent: unknown action %q", e.Action)
	case e.AccountID == "" || !proposalUUID.MatchString(e.SessionID) || !proposalUUID.MatchString(e.ConnectionID) || e.Tool == "" || e.Actor == "":
		return errors.New("InsertPinEvent: account, session, connection, tool and actor are required")
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO mcp_pin_events (account_id, session_id, connection_id, connection_name, tool, action, old_hash, new_hash, mode, actor)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10)`,
		e.AccountID, e.SessionID, e.ConnectionID, e.ConnectionName, e.Tool, e.Action,
		nullIfEmpty(e.OldHash), nullIfEmpty(e.NewHash), nullIfEmpty(e.Mode), e.Actor); err != nil {
		return fmt.Errorf("InsertPinEvent: %w", err)
	}
	return nil
}

const pinEventColumns = `id, account_id, session_id::text, connection_id::text, connection_name, tool, action,
	COALESCE(old_hash, ''), COALESCE(new_hash, ''), COALESCE(mode, ''), actor, created_at`

func queryPinEvents(ctx context.Context, q DBTX, sql string, args ...any) ([]PinEvent, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list pin events: %w", err)
	}
	defer rows.Close()
	out := []PinEvent{}
	for rows.Next() {
		var e PinEvent
		if err := rows.Scan(&e.ID, &e.AccountID, &e.SessionID, &e.ConnectionID, &e.ConnectionName, &e.Tool, &e.Action,
			&e.OldHash, &e.NewHash, &e.Mode, &e.Actor, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("list pin events scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListPinEventsForSession lists the account's events for one session, newest first.
func ListPinEventsForSession(ctx context.Context, q DBTX, accountID, sessionID string, limit int) ([]PinEvent, error) {
	if !proposalUUID.MatchString(sessionID) {
		return []PinEvent{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	return queryPinEvents(ctx, q, `SELECT `+pinEventColumns+` FROM mcp_pin_events
		WHERE account_id = $1 AND session_id = $2::uuid ORDER BY created_at DESC, id DESC LIMIT $3`, accountID, sessionID, limit)
}

// ListPinEventsForConnection lists the account's events for one connection, newest first.
func ListPinEventsForConnection(ctx context.Context, q DBTX, accountID, connectionID string, limit int) ([]PinEvent, error) {
	if !proposalUUID.MatchString(connectionID) {
		return []PinEvent{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	return queryPinEvents(ctx, q, `SELECT `+pinEventColumns+` FROM mcp_pin_events
		WHERE account_id = $1 AND connection_id = $2::uuid ORDER BY created_at DESC, id DESC LIMIT $3`, accountID, connectionID, limit)
}

// PrunePinEvents deletes events older than retention.
func PrunePinEvents(ctx context.Context, pool *pgxpool.Pool, retention time.Duration) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM mcp_pin_events WHERE created_at < now() - make_interval(secs => $1)`, retention.Seconds())
	if err != nil {
		return 0, fmt.Errorf("PrunePinEvents: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RepinToolWithEvent is RepinGrantTool plus its PinEvent in one transaction. When the
// compare-and-set loses nothing is written and false is returned. The caller supplies only
// AccountID, ConnectionName and Actor in ev: the session, connection, tool, hashes and action are
// taken from the write's own arguments, and Mode is the EFFECTIVE mode read back from the row in
// the same transaction (an empty mode keeps the pinned one), so the audit row cannot disagree
// with what was written.
func RepinToolWithEvent(ctx context.Context, pool *pgxpool.Pool, sessionID, connectionID, tool, oldHash, newHash, mode string, ev PinEvent) (bool, error) {
	return withEvent(ctx, pool, ev, func(tx DBTX, ev *PinEvent) (bool, error) {
		ok, err := RepinGrantTool(ctx, tx, sessionID, connectionID, tool, oldHash, newHash, mode)
		if err != nil || !ok {
			return false, err
		}
		var effective *string
		if err := tx.QueryRow(ctx, `SELECT tools->$3->>'mode' FROM session_mcp_grants WHERE session_id = $1 AND connection_id = $2`,
			sessionID, connectionID, tool).Scan(&effective); err != nil {
			return false, fmt.Errorf("RepinToolWithEvent read mode: %w", err)
		}
		ev.SessionID, ev.ConnectionID, ev.Tool, ev.Action = sessionID, connectionID, tool, PinRepinned
		ev.OldHash, ev.NewHash = oldHash, newHash
		ev.Mode = ""
		if effective != nil {
			ev.Mode = *effective
		}
		return true, nil
	})
}

// GrantToolWithEvent is GrantTool plus its PinEvent in one transaction. As with
// RepinToolWithEvent the caller supplies only AccountID, ConnectionName and Actor in ev; the
// session, connection, tool, hash, mode and action come from the write's arguments.
func GrantToolWithEvent(ctx context.Context, pool *pgxpool.Pool, sessionID, connectionID, tool, hash, mode string, ev PinEvent) (bool, error) {
	return withEvent(ctx, pool, ev, func(tx DBTX, ev *PinEvent) (bool, error) {
		ok, err := GrantTool(ctx, tx, sessionID, connectionID, tool, hash, mode)
		if err != nil || !ok {
			return false, err
		}
		ev.SessionID, ev.ConnectionID, ev.Tool, ev.Action = sessionID, connectionID, tool, PinGranted
		ev.OldHash, ev.NewHash, ev.Mode = "", hash, mode
		return true, nil
	})
}

// withEvent runs write, which does the compare-and-set and, when it wins, fills the event from the
// write's own arguments; the event is inserted in the same transaction.
func withEvent(ctx context.Context, pool *pgxpool.Pool, ev PinEvent, write func(tx DBTX, ev *PinEvent) (bool, error)) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("pin write begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	ok, err := write(tx, &ev)
	if err != nil || !ok {
		return false, err
	}
	if err := InsertPinEvent(ctx, tx, ev); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
