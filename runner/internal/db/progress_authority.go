package db

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// AppendChildProgressAt shares the lifecycle row lock with starts/completions. A delayed plan
// cannot revive an ended child, write under a replaced daemon, or cross a newer incarnation.
func AppendChildProgressAt(ctx context.Context, pool *pgxpool.Pool, sid, eventID, kind, payload, child, daemonID string, at time.Time) (int64, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer rollbackBackgroundTaskTx(ctx, tx)
	if err := ValidateChildProgressTx(ctx, tx, sid, child, daemonID, at); err != nil {
		return 0, false, err
	}
	seq, fresh, err := AppendAgentEventTx(ctx, tx, sid, eventID, kind, payload, at)
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, err
	}
	return seq, fresh, nil
}

// ValidateChildProgressTx locks the session and validates one current child incarnation.
func ValidateChildProgressTx(ctx context.Context, tx pgx.Tx, sid, child, daemonID string, at time.Time) error {
	var owner, status string
	if err := tx.QueryRow(ctx, `SELECT daemon_id,status FROM sessions WHERE id=$1 FOR UPDATE`, sid).Scan(&owner, &status); err != nil {
		return err
	}
	if owner != daemonID || status == "ended" || status == "stopped" || status == "error" {
		return ErrBackgroundTaskAuthority
	}
	var startSeq int64
	var startID string
	var startAt time.Time
	err := tx.QueryRow(ctx, `SELECT seq,client_event_id::text,ts FROM agent_events WHERE session_id=$1 AND kind='subagent_started' AND payload->>'child_id'=$2 ORDER BY seq DESC LIMIT 1`, sid, child).Scan(&startSeq, &startID, &startAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBackgroundTaskAuthority
	}
	if err != nil {
		return err
	}
	if at.Before(startAt) {
		return ErrBackgroundTaskAuthority
	}
	var doneRaw string
	err = tx.QueryRow(ctx, `SELECT payload::text FROM agent_events WHERE session_id=$1 AND kind='subagent_done' AND payload->>'child_id'=$2 AND seq>$3 AND (COALESCE(payload->>'start_event_id','')='' OR payload->>'start_event_id'=$4) ORDER BY seq DESC LIMIT 1`, sid, child, startSeq, startID).Scan(&doneRaw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		var done struct {
			Background bool `json:"background"`
		}
		if json.Unmarshal([]byte(doneRaw), &done) != nil || !done.Background {
			return ErrBackgroundTaskAuthority
		}
	}

	return nil
}
