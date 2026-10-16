package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SessionInputMaxClientIDBytes = 256
	SessionInputMaxBytes         = 256 << 10
	SessionInputMaxPending       = 64
	SessionInputMaxPendingBytes  = 4 << 20
)

var (
	ErrSessionInputTooLarge = errors.New("message is too large")
	ErrSessionInputID       = errors.New("message id is invalid")
	ErrSessionInputConflict = errors.New("message id was already used for different content")
	ErrSessionInputQueue    = errors.New("too many pending messages")
	ErrSessionInputEnded    = errors.New("session has ended")
)

type SessionInput struct {
	Frame string
	Event AgentEventRow
}

// AcceptSessionInput commits both the outbox entry and its transcript acceptance together.
func AcceptSessionInput(ctx context.Context, pool *pgxpool.Pool, sessionID, clientID, frame, eventID, payload string) (int64, bool, error) {
	if clientID == "" || len(clientID) > SessionInputMaxClientIDBytes {
		return 0, false, ErrSessionInputID
	}
	if len(frame) > SessionInputMaxBytes || len(payload) > SessionInputMaxBytes {
		return 0, false, ErrSessionInputTooLarge
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The session lock serializes queue bounds and races with the session ending.
	var status string
	var endedAt *time.Time
	var endReason *string
	if err = tx.QueryRow(ctx, `SELECT status,ended_at,end_reason FROM sessions WHERE id=$1 FOR UPDATE`, sessionID).Scan(&status, &endedAt, &endReason); err != nil {
		return 0, false, err
	}
	var existingSeq int64
	var same bool
	err = tx.QueryRow(ctx, `SELECT event_seq,frame=$3::jsonb FROM session_inputs WHERE session_id=$1 AND client_id=$2`, sessionID, clientID, frame).Scan(&existingSeq, &same)
	if err == nil {
		if !same {
			return 0, false, ErrSessionInputConflict
		}
		return existingSeq, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	if endedAt != nil || (status == "stopped" || status == "ended") && endReason != nil {
		return 0, false, ErrSessionInputEnded
	}
	var count, size int64
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(octet_length(frame::text)),0) FROM session_inputs WHERE session_id=$1 AND settled_at IS NULL`, sessionID).Scan(&count, &size); err != nil {
		return 0, false, err
	}
	if count >= SessionInputMaxPending || size+int64(len(frame)) > SessionInputMaxPendingBytes {
		return 0, false, ErrSessionInputQueue
	}
	tag, err := tx.Exec(ctx, `INSERT INTO session_inputs(session_id,client_id,frame,event_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, sessionID, clientID, frame, eventID)
	if err != nil {
		return 0, false, err
	}
	if tag.RowsAffected() == 0 {
		return 0, false, nil
	}
	seq, _, err := appendAgentEvent(ctx, tx, sessionID, eventID, "input_accepted", payload, time.Now())
	if err != nil {
		return 0, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE session_inputs SET event_seq=$3 WHERE session_id=$1 AND client_id=$2`, sessionID, clientID, seq); err != nil {
		return 0, false, err
	}
	return seq, true, tx.Commit(ctx)
}

// FailSessionInput atomically records why an input cannot run and settles its outbox entry.
func FailSessionInput(ctx context.Context, pool *pgxpool.Pool, sessionID, clientID, eventID, payload string) (int64, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE session_inputs SET settled_at=now() WHERE session_id=$1 AND client_id=$2 AND settled_at IS NULL`, sessionID, clientID)
	if err != nil || tag.RowsAffected() == 0 {
		return 0, false, err
	}
	seq, fresh, err := appendAgentEvent(ctx, tx, sessionID, eventID, "error", payload, time.Now())
	if err != nil {
		return 0, false, err
	}
	return seq, fresh, tx.Commit(ctx)
}

// HasPendingSessionInputs is a cheap check before PendingSessionInputs.
func HasPendingSessionInputs(ctx context.Context, pool *pgxpool.Pool, sessionID string) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_inputs WHERE session_id=$1 AND settled_at IS NULL)`, sessionID).Scan(&ok)
	return ok, err
}

func PendingSessionInputs(ctx context.Context, pool *pgxpool.Pool, sessionID string) ([]SessionInput, error) {
	rows, err := pool.Query(ctx, `SELECT i.frame::text,e.session_id::text,e.seq,e.client_event_id::text,e.kind,e.payload::text,e.ts FROM session_inputs i JOIN agent_events e ON e.session_id=i.session_id AND e.seq=i.event_seq WHERE i.session_id=$1 AND i.settled_at IS NULL ORDER BY i.event_seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionInput
	for rows.Next() {
		var r SessionInput
		if err = rows.Scan(&r.Frame, &r.Event.SessionID, &r.Event.Seq, &r.Event.ClientEventID, &r.Event.Kind, &r.Event.Payload, &r.Event.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func SettleSessionInput(ctx context.Context, pool *pgxpool.Pool, sessionID, clientID string) error {
	_, err := pool.Exec(ctx, `UPDATE session_inputs SET settled_at=COALESCE(settled_at,now()) WHERE session_id=$1 AND client_id=$2`, sessionID, clientID)
	return err
}
