package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrArtifactLimit is returned by InsertArtifact when the session already holds the maximum.
var ErrArtifactLimit = errors.New("session artifact limit reached")

// ErrArtifactBytesLimit is returned by InsertArtifactCapped when the new file would push the
// session's files of that origin past the total byte cap.
var ErrArtifactBytesLimit = errors.New("session artifact size limit reached")

// ErrArtifactVersionLimit is returned by InsertArtifactCapped when the session already holds
// MaxArtifactVersions files of that name and origin.
var ErrArtifactVersionLimit = errors.New("artifact version limit reached")

// MaxArtifactVersions is how many versions of one file name (per origin) a session may hold.
const MaxArtifactVersions = 20

// Who put a file there.
const (
	OriginAgent = "agent"
	OriginUser  = "user"
)

// ArtifactRow is one session_artifacts row: the index of a file an agent published.
type ArtifactRow struct {
	ID          string
	SessionID   string
	Name        string
	Size        int64
	ContentType string
	SHA256      string
	CreatedAt   time.Time
	Origin      string // OriginAgent (default when empty) or OriginUser
	UploadedBy  string // the account id of the person, for OriginUser
	// Version is the file's number among the files of the same name and origin in the session
	// (1 for the first). It is stored, so deleting an earlier version never renumbers the rest.
	Version int
	// Previous is set only on the row InsertArtifactCapped returns: the version this one follows
	// (0 when it is the first of its name).
	Previous int
}

const artifactCols = `id, session_id::text, name, size, content_type, sha256, created_at, origin, coalesce(uploaded_by, ''), version`

func scanArtifact(row pgx.Row) (ArtifactRow, error) {
	var a ArtifactRow
	err := row.Scan(&a.ID, &a.SessionID, &a.Name, &a.Size, &a.ContentType, &a.SHA256, &a.CreatedAt, &a.Origin, &a.UploadedBy, &a.Version)
	return a, err
}

// CountArtifacts is how many artifacts a session holds.
func CountArtifacts(ctx context.Context, pool *pgxpool.Pool, sessionID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM session_artifacts WHERE session_id = $1`, sessionID).Scan(&n)
	return n, err
}

// CountArtifactsOrigin is how many files of one origin a session holds, and their total size.
func CountArtifactsOrigin(ctx context.Context, pool *pgxpool.Pool, sessionID, origin string) (n int, bytes int64, err error) {
	err = pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(size), 0)::bigint FROM session_artifacts
		WHERE session_id = $1 AND origin = $2`, sessionID, origin).Scan(&n, &bytes)
	return n, bytes, err
}

// CountArtifactVersions is how many files of that name and origin a session holds.
func CountArtifactVersions(ctx context.Context, pool *pgxpool.Pool, sessionID, origin, name string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM session_artifacts
		WHERE session_id = $1 AND origin = $2 AND name = $3`, sessionID, origin, name).Scan(&n)
	return n, err
}

// InsertArtifact adds a row unless the session already holds limit files of that origin
// (ErrArtifactLimit). The count and the insert run under one per-session
// advisory lock, so concurrent uploads cannot push a session past limit.
func InsertArtifact(ctx context.Context, pool *pgxpool.Pool, a ArtifactRow, limit int) (ArtifactRow, error) {
	return InsertArtifactCapped(ctx, pool, a, limit, 0)
}

// InsertArtifactCapped is InsertArtifact plus a cap on the total bytes of that origin's files
// (maxTotal > 0; ErrArtifactBytesLimit). The new row's version is one more than the highest of the
// same name and origin, taken under the same lock; a name that already has MaxArtifactVersions
// files is refused (ErrArtifactVersionLimit).
func InsertArtifactCapped(ctx context.Context, pool *pgxpool.Pool, a ArtifactRow, limit int, maxTotal int64) (ArtifactRow, error) {
	if a.Origin == "" {
		a.Origin = OriginAgent
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ArtifactRow{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "artifacts:"+a.SessionID); err != nil {
		return ArtifactRow{}, err
	}
	var n, sameName, maxVersion int
	var total int64
	if err := tx.QueryRow(ctx, `SELECT count(*), coalesce(sum(size), 0)::bigint,
			count(*) FILTER (WHERE name = $3), coalesce(max(version) FILTER (WHERE name = $3), 0)
		FROM session_artifacts WHERE session_id = $1 AND origin = $2`, a.SessionID, a.Origin, a.Name).Scan(&n, &total, &sameName, &maxVersion); err != nil {
		return ArtifactRow{}, err
	}
	if sameName >= MaxArtifactVersions {
		return ArtifactRow{}, ErrArtifactVersionLimit
	}
	if n >= limit {
		return ArtifactRow{}, ErrArtifactLimit
	}
	if maxTotal > 0 && total+a.Size > maxTotal {
		return ArtifactRow{}, ErrArtifactBytesLimit
	}
	out, err := scanArtifact(tx.QueryRow(ctx, `
		INSERT INTO session_artifacts (id, session_id, name, size, content_type, sha256, origin, uploaded_by, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9)
		RETURNING `+artifactCols, a.ID, a.SessionID, a.Name, a.Size, a.ContentType, a.SHA256, a.Origin, a.UploadedBy, maxVersion+1))
	if err != nil {
		return ArtifactRow{}, fmt.Errorf("insert artifact: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ArtifactRow{}, err
	}
	out.Previous = maxVersion
	return out, nil
}

// ListArtifacts returns a session's artifacts, newest first.
func ListArtifacts(ctx context.Context, pool *pgxpool.Pool, sessionID string) ([]ArtifactRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+artifactCols+` FROM session_artifacts
		 WHERE session_id = $1 ORDER BY created_at DESC, id DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtifactRow{}
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetArtifact returns one artifact of a session, or nil when there is none
// (an unknown id, or one that belongs to another session).
func GetArtifact(ctx context.Context, pool *pgxpool.Pool, sessionID, id string) (*ArtifactRow, error) {
	a, err := scanArtifact(pool.QueryRow(ctx, `
		SELECT `+artifactCols+` FROM session_artifacts WHERE session_id = $1 AND id = $2`, sessionID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// LatestArtifactVersion is the highest version among the files of that name and origin (0 when
// there is none).
func LatestArtifactVersion(ctx context.Context, pool *pgxpool.Pool, sessionID, origin, name string) (int, error) {
	var v int
	err := pool.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM session_artifacts
		WHERE session_id = $1 AND origin = $2 AND name = $3`, sessionID, origin, name).Scan(&v)
	return v, err
}

// DeleteArtifact removes the row and reports whether there was one.
func DeleteArtifact(ctx context.Context, pool *pgxpool.Pool, sessionID, id string) (bool, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM session_artifacts WHERE session_id = $1 AND id = $2`, sessionID, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ExistingSessionIDs returns which of ids name a session row (used to find
// orphaned artifact directories).
func ExistingSessionIDs(ctx context.Context, pool *pgxpool.Pool, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `SELECT id::text FROM sessions WHERE id::text = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
