package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNativeHistoryBinding = errors.New("native history does not match this session's owner or engine")

type SessionNativeHistory struct {
	SessionID, AccountID, Engine, ClaimName, ClaimUID string
	CreatedAt, LastUsedAt                             time.Time
	DetachedAt, ReclaimedAt                           *time.Time
}

func GetSessionNativeHistory(ctx context.Context, pool *pgxpool.Pool, id string) (*SessionNativeHistory, error) {
	h := &SessionNativeHistory{}
	err := pool.QueryRow(ctx, `SELECT session_id::text,account_id,engine,claim_name,claim_uid,created_at,last_used_at,detached_at,reclaimed_at FROM session_native_history WHERE session_id=$1`, id).Scan(&h.SessionID, &h.AccountID, &h.Engine, &h.ClaimName, &h.ClaimUID, &h.CreatedAt, &h.LastUsedAt, &h.DetachedAt, &h.ReclaimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

// BindSessionNativeHistory is immutable: neither a retry nor a replacement can
// change the claim identity, owner or engine of a session's retained history.
func BindSessionNativeHistory(ctx context.Context, pool *pgxpool.Pool, h SessionNativeHistory) error {
	if h.AccountID == "" || h.Engine == "" || h.ClaimName == "" || h.ClaimUID == "" {
		return ErrNativeHistoryBinding
	}
	_, err := pool.Exec(ctx, `INSERT INTO session_native_history(session_id,session_ref,account_id,engine,claim_name,claim_uid)
 SELECT id,id,$2,$3,$4,$5 FROM sessions WHERE id=$1 AND spawning_account_id=$2 AND runtime='cluster' AND ended_at IS NULL AND status NOT IN ('ended','stopped','error') AND COALESCE(NULLIF(engine,''),'claude')=$3
 ON CONFLICT(session_id) DO NOTHING`, h.SessionID, h.AccountID, h.Engine, h.ClaimName, h.ClaimUID)
	if err != nil {
		return err
	}
	got, err := GetSessionNativeHistory(ctx, pool, h.SessionID)
	if err != nil {
		return err
	}
	if got == nil || got.DetachedAt != nil || got.ReclaimedAt != nil || got.AccountID != h.AccountID || got.Engine != h.Engine || got.ClaimName != h.ClaimName || got.ClaimUID != h.ClaimUID {
		return ErrNativeHistoryBinding
	}
	return nil
}

func TouchSessionNativeHistory(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `UPDATE session_native_history SET last_used_at=now() WHERE session_id=$1`, id)
	return err
}

// WithSessionNativeHistoryLock fences every Job writer across server replicas.
// The existing advisory-lock implementation uses a separate connection, so the
// callback can use a single-connection main pool without deadlocking it.
func WithSessionNativeHistoryLock(ctx context.Context, pool *pgxpool.Pool, id string, fn func() error) error {
	return WithLimitResumeLock(ctx, pool, "native-history:"+id, func(*pgxpool.Conn) error { return fn() })
}

// ListExpiredNativeHistory bounds cleanup work to genuinely ended sessions.
// Disconnected and paused sessions have no expiry, regardless of age.
func ListExpiredNativeHistory(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT h.session_id::text FROM session_native_history h LEFT JOIN sessions s ON s.id=h.session_ref WHERE h.reclaimed_at IS NULL AND ((s.ended_at<$1 AND s.status IN ('ended','stopped','error')) OR (h.session_ref IS NULL AND h.detached_at<$1)) ORDER BY COALESCE(h.detached_at,s.ended_at) LIMIT 20`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ReclaimExpiredNativeHistory holds the session row through external deletion.
// A concurrent resume waits, then observes the immutable missing-claim binding
// rather than starting a writer against data that is being reclaimed.
func ReclaimExpiredNativeHistory(ctx context.Context, pool *pgxpool.Pool, id string, cutoff time.Time, remove func(SessionNativeHistory) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Match normal session deletion's lock order: session first, then history.
	// If deletion wins first, the subsequent locked history read sees detached_at.
	var ref string
	err = tx.QueryRow(ctx, `SELECT COALESCE(session_ref::text,'') FROM session_native_history WHERE session_id=$1`, id).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if ref != "" {
		var locked string
		err = tx.QueryRow(ctx, `SELECT id::text FROM sessions WHERE id=$1 FOR UPDATE`, ref).Scan(&locked)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	var h SessionNativeHistory
	err = tx.QueryRow(ctx, `SELECT session_id::text,COALESCE(session_ref::text,''),account_id,engine,claim_name,claim_uid,detached_at,reclaimed_at FROM session_native_history WHERE session_id=$1 FOR UPDATE`, id).Scan(&h.SessionID, &ref, &h.AccountID, &h.Engine, &h.ClaimName, &h.ClaimUID, &h.DetachedAt, &h.ReclaimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if h.ReclaimedAt != nil {
		return nil
	}
	if ref == "" {
		if h.DetachedAt == nil || !h.DetachedAt.Before(cutoff) {
			return nil
		}
	} else {
		var eligible bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$1 AND ended_at<$2 AND status IN ('ended','stopped','error'))`, ref, cutoff).Scan(&eligible)
		if err != nil {
			return err
		}
		if !eligible {
			return nil
		}
	}
	if err = remove(h); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE session_native_history SET reclaimed_at=now() WHERE session_id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
