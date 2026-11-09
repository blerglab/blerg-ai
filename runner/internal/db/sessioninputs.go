package db

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
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
func AcceptSessionInput(ctx context.Context, pool *pgxpool.Pool, sessionID, clientID, frame, eventID, payload string, discussion ...string) (int64, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	seq, fresh, err := AcceptSessionInputTx(ctx, tx, sessionID, clientID, frame, eventID, payload, discussion...)
	if err != nil {
		return 0, false, err
	}
	return seq, fresh, tx.Commit(ctx)
}

// AcceptSessionInputTx lets a domain mutation share the durable input transaction.
// Typed ordinary chat may supply discussion text; other frames remain opaque.
func AcceptSessionInputTx(ctx context.Context, tx pgx.Tx, sessionID, clientID, frame, eventID, payload string, discussion ...string) (int64, bool, error) {
	if clientID == "" || len(clientID) > SessionInputMaxClientIDBytes {
		return 0, false, ErrSessionInputID
	}
	if len(frame) > SessionInputMaxBytes || len(payload) > SessionInputMaxBytes {
		return 0, false, ErrSessionInputTooLarge
	}
	// The session lock serializes queue bounds and races with the session ending.
	var status string
	var endedAt *time.Time
	var endReason *string
	if err := tx.QueryRow(ctx, `SELECT status,ended_at,end_reason FROM sessions WHERE id=$1 FOR UPDATE`, sessionID).Scan(&status, &endedAt, &endReason); err != nil {
		return 0, false, err
	}
	var existingSeq int64
	var same bool
	err := tx.QueryRow(ctx, `SELECT event_seq,frame=$3::jsonb FROM session_inputs WHERE session_id=$1 AND client_id=$2`, sessionID, clientID, frame).Scan(&existingSeq, &same)
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
	if len(discussion) > 0 && discussion[0] != "" {
		// Discussion handoff is optional metadata. A failed bound lookup must not
		// roll back the person's ordinary conversation input and its receipt.
		handoff, err := tx.Begin(ctx)
		if err != nil {
			return 0, false, err
		}
		if _, err = handoff.Exec(ctx, `UPDATE messages SET cli_discussion=$2,question_turn='main' WHERE session_id=$1 AND kind='ask' AND status='open' AND question_session_posted AND NOT no_wait AND (NOT owner_only OR EXISTS(SELECT 1 FROM bound_asks WHERE message_id=messages.id))`, sessionID, discussion[0]); err != nil {
			if rollbackErr := handoff.Rollback(ctx); rollbackErr != nil {
				return 0, false, rollbackErr
			}
			failurelog.Default.Event("question_discussion_handoff", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
		} else if err := handoff.Commit(ctx); err != nil {
			return 0, false, err
		}
	}

	return seq, true, nil
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

// RecordSettledSessionInput records clientID as already settled, for an input that has no outbox
// entry of its own: one delivered live to a host without input receipts, or one given up on. Either
// way it is then never queued or delivered again under that id. With errorPayload, an "error" event
// saying why is appended in the same transaction (seq is its sequence). fresh is false, and nothing
// is appended, when the id was already recorded.
func RecordSettledSessionInput(ctx context.Context, pool *pgxpool.Pool, sessionID, clientID, frame, eventID, errorPayload string) (int64, bool, error) {
	if clientID == "" || len(clientID) > SessionInputMaxClientIDBytes {
		return 0, false, ErrSessionInputID
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `INSERT INTO session_inputs(session_id,client_id,frame,event_id,settled_at) VALUES($1,$2,$3,$4,now()) ON CONFLICT DO NOTHING`, sessionID, clientID, frame, eventID)
	if err != nil || tag.RowsAffected() == 0 {
		return 0, false, err
	}
	var seq int64
	if errorPayload != "" {
		if seq, _, err = appendAgentEvent(ctx, tx, sessionID, eventID, "error", errorPayload, time.Now()); err != nil {
			return 0, false, err
		}
		if _, err = tx.Exec(ctx, `UPDATE session_inputs SET event_seq=$3 WHERE session_id=$1 AND client_id=$2`, sessionID, clientID, seq); err != nil {
			return 0, false, err
		}
	}
	return seq, true, tx.Commit(ctx)
}

// RefreshChildTurnInput replaces the summary of an already queued Blerg notice.
// It never creates another input or wakes the engine. A consumed receipt stays
// consumed: the parent already received the instruction to read this child.
func RefreshChildTurnInput(ctx context.Context, pool *pgxpool.Pool, sessionID, clientID, text string) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var seq int64
	var settled *time.Time
	err = tx.QueryRow(ctx, `SELECT event_seq,settled_at FROM session_inputs WHERE session_id=$1 AND client_id=$2 AND frame->>'source'='child_turn' FOR UPDATE`, sessionID, clientID).Scan(&seq, &settled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if settled == nil {
		if _, err = tx.Exec(ctx, `UPDATE session_inputs SET frame=jsonb_set(frame,'{text}',to_jsonb($3::text)) WHERE session_id=$1 AND client_id=$2`, sessionID, clientID, text); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_events SET payload=jsonb_set(payload,'{text}',to_jsonb($3::text)) WHERE session_id=$1 AND seq=$2 AND kind='input_accepted'`, sessionID, seq, text); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}
