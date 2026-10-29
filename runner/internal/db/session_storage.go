package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSessionStorageAtMaximum: the session is already at (or above) the install maximum.
var ErrSessionStorageAtMaximum = errors.New("session storage is at the install maximum")

// SessionUploadLimit is the session's own attachment total in bytes, or 0 when it uses the
// install default. A missing session reads as 0 too; callers have already loaded it.
func SessionUploadLimit(ctx context.Context, pool *pgxpool.Pool, sessionID string) (int64, error) {
	var n *int64
	err := pool.QueryRow(ctx, `SELECT upload_limit_bytes FROM sessions WHERE id = $1`, sessionID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) || n == nil {
		return 0, nil
	}
	return *n, err
}

// RaiseSessionUploadLimit adds step bytes to the session's attachment total (starting from
// current, the effective total the caller computed from the install default when the session has
// none of its own), never past maximum, and records who did it. It returns the new total.
func RaiseSessionUploadLimit(ctx context.Context, pool *pgxpool.Pool, sessionID, actor string, defaultBytes, step, maximum int64) (previous, next int64, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var own *int64
	if err = tx.QueryRow(ctx, `SELECT upload_limit_bytes FROM sessions WHERE id = $1 FOR UPDATE`, sessionID).Scan(&own); err != nil {
		return 0, 0, err
	}
	previous = defaultBytes
	if own != nil {
		previous = *own
	}
	if previous >= maximum {
		return previous, previous, ErrSessionStorageAtMaximum
	}
	next = min(previous+step, maximum)
	if _, err = tx.Exec(ctx, `UPDATE sessions SET upload_limit_bytes = $2 WHERE id = $1`, sessionID, next); err != nil {
		return 0, 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO session_storage_audit(session_id, actor, previous_bytes, new_bytes) VALUES($1, $2, $3, $4)`, sessionID, actor, previous, next); err != nil {
		return 0, 0, err
	}
	return previous, next, tx.Commit(ctx)
}

// FileDeletion is who deleted a session file, for the audit kept in session_file_deletions.
type FileDeletion struct {
	Actor      string
	ActorAdmin bool
	Via        string
}

// DeleteArtifactAudited removes the row and records who deleted it in the same transaction, so a
// deletion never happens without its record. It reports whether there was a row.
func DeleteArtifactAudited(ctx context.Context, pool *pgxpool.Pool, a ArtifactRow, by FileDeletion) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tag, err := tx.Exec(ctx, `DELETE FROM session_artifacts WHERE session_id = $1 AND id = $2`, a.SessionID, a.ID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_file_deletions(session_id, artifact_id, name, origin, version, size, actor, actor_admin, via)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9)`, a.SessionID, a.ID, a.Name, a.Origin, a.Version, a.Size, by.Actor, by.ActorAdmin, by.Via); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
