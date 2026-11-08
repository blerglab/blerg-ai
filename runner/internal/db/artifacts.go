package db

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// ErrArtifactAlreadyLive is returned by InsertLiveArtifactCapped when another transaction's live
// row for this (session, origin, name) already exists by the time this one acquires the
// per-session advisory lock: two concurrent first-time `--live` publishes of the same new name
// race for who becomes its live row, and only the first to commit wins (BLG-470 round 2 #4). The
// unique index backs this up for any path that does not hold the lock, but normal callers see
// this error, not a constraint violation, because the check runs inside the same lock as the
// insert — there is no gap for a second insert to land in.
var ErrArtifactAlreadyLive = errors.New("a live row for this name already exists")

// MaxArtifactVersions is how many versions of one file name (per origin) a session may hold.
const MaxArtifactVersions = 500

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
	// Live: this row is updated in place by a republish of the same name (`publish --live`),
	// instead of a new version being added. At most one live row per (session, origin, name).
	Live bool
	// UpdatedAt is when the row's bytes last changed: created_at for a file published once, the
	// time of its last in-place overwrite for a live one.
	UpdatedAt time.Time
}

const artifactCols = `id, session_id::text, name, size, content_type, sha256, created_at, origin, coalesce(uploaded_by, ''), version, live, updated_at`

func scanArtifact(row pgx.Row) (ArtifactRow, error) {
	var a ArtifactRow
	err := row.Scan(&a.ID, &a.SessionID, &a.Name, &a.Size, &a.ContentType, &a.SHA256, &a.CreatedAt, &a.Origin, &a.UploadedBy, &a.Version, &a.Live, &a.UpdatedAt)
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
	return insertArtifactCapped(ctx, pool, a, limit, maxTotal, MaxArtifactVersions)
}

func insertArtifactCapped(ctx context.Context, pool *pgxpool.Pool, a ArtifactRow, limit int, maxTotal int64, maxVersions int) (ArtifactRow, error) {
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
	if a.Live {
		// Still holding the lock: re-check for an existing live row here, not just rely on the
		// unique index catching a race after the fact. Whichever of two concurrent first-time
		// `--live` publishes of this name acquires the lock second sees the first's committed row
		// and fails cleanly and immediately, rather than racing into a constraint violation.
		var existingID string
		err := tx.QueryRow(ctx, `SELECT id FROM session_artifacts
			WHERE session_id = $1 AND origin = $2 AND name = $3 AND live`, a.SessionID, a.Origin, a.Name).Scan(&existingID)
		if err == nil {
			return ArtifactRow{}, ErrArtifactAlreadyLive
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ArtifactRow{}, err
		}
	}
	var n, sameName, maxVersion int
	var total int64
	if err := tx.QueryRow(ctx, `SELECT count(*), coalesce(sum(size), 0)::bigint,
			count(*) FILTER (WHERE name = $3), coalesce(max(version) FILTER (WHERE name = $3), 0)
		FROM session_artifacts WHERE session_id = $1 AND origin = $2`, a.SessionID, a.Origin, a.Name).Scan(&n, &total, &sameName, &maxVersion); err != nil {
		return ArtifactRow{}, err
	}
	if sameName >= maxVersions {
		return ArtifactRow{}, ErrArtifactVersionLimit
	}
	if limit > 0 && n >= limit {
		return ArtifactRow{}, ErrArtifactLimit
	}
	if maxTotal > 0 && total+a.Size > maxTotal {
		return ArtifactRow{}, ErrArtifactBytesLimit
	}
	out, err := scanArtifact(tx.QueryRow(ctx, `
		INSERT INTO session_artifacts (id, session_id, name, size, content_type, sha256, origin, uploaded_by, version, live)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9, $10)
		RETURNING `+artifactCols, a.ID, a.SessionID, a.Name, a.Size, a.ContentType, a.SHA256, a.Origin, a.UploadedBy, maxVersion+1, a.Live))
	if err != nil {
		return ArtifactRow{}, fmt.Errorf("insert artifact: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ArtifactRow{}, err
	}
	out.Previous = maxVersion
	return out, nil
}

// GetLiveArtifact returns the one row of (sessionID, origin, name) that is live, or nil when the
// name has no live row (it does not exist yet, or every row of it is a normal version).
func GetLiveArtifact(ctx context.Context, pool *pgxpool.Pool, sessionID, origin, name string) (*ArtifactRow, error) {
	a, err := scanArtifact(pool.QueryRow(ctx, `
		SELECT `+artifactCols+` FROM session_artifacts
		 WHERE session_id = $1 AND origin = $2 AND name = $3 AND live`, sessionID, origin, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListLiveArtifacts returns every live row across every session, for the startup sweep
// (BLG-470 round 3 #3) that checks each one's bytes still match what the row says: a crash
// between UpdateLiveArtifactContent's commit and its rename leaves a live row describing content
// the file on disk does not have, with nothing to notice on its own.
func ListLiveArtifacts(ctx context.Context, pool *pgxpool.Pool) ([]ArtifactRow, error) {
	rows, err := pool.Query(ctx, `SELECT `+artifactCols+` FROM session_artifacts WHERE live`)
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

// UpdateLiveArtifactContent overwrites a live row's bytes in place (same id, same version, no new
// row): size, content type and checksum move to the republished content, and updated_at to now.
// maxTotal (> 0) is checked against the session's total minus this row's own previous size, so
// republishing the same live file never counts twice toward its cap.
//
// Order matters here, across two review rounds:
//   - Renaming the staging file into place before the row's own transaction commits (round 1 #2)
//     closed the race between two concurrent republishes landing their row commit and their
//     rename in different orders — but if that rename succeeded and the commit then failed, the
//     row rolled back to the old content while the new bytes were already on disk (round 2 #5).
//   - Committing first and renaming after reopens that specific gap (bytes new, row still old)
//     only if a *second* update can run in the meantime; holding a session-level advisory lock
//     (acquired before the transaction, released only after the rename) across the whole thing
//     keeps both properties at once: the transaction commits before anything touches the
//     filesystem, and no concurrent update for this session can even begin until this one's
//     rename (or its restore, see below) is fully resolved and the lock is released.
//
// commitFile must put the new bytes at the file's real path (typically an os.Rename of a staging
// file written ahead of this call); it is retried commitFileRetries times if it fails (os.Rename
// is one syscall — a failed attempt never moves anything, so retrying is always safe). If it is
// still failing after that, the row is restored to the content it had before this call (matching
// the bytes that are, in fact, still on disk) and the error is returned wrapped, for the caller to
// log; a restore that cannot itself complete (logged, not returned, since the caller's own error
// already covers the commitFile failure) leaves the mismatch for a person to find and repair.
const commitFileRetries = 3

func UpdateLiveArtifactContent(ctx context.Context, pool *pgxpool.Pool, sessionID, id string, size int64, contentType, sha256 string, maxTotal int64, commitFile func() error) (ArtifactRow, error) {
	lockKey := "artifacts:" + sessionID
	acquired, err := pool.Acquire(ctx)
	if err != nil {
		return ArtifactRow{}, err
	}
	defer acquired.Release()
	conn := acquired.Conn()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, lockKey); err != nil {
		return ArtifactRow{}, err
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock(hashtext($1))`, lockKey); err != nil {
			// A connection that could not give the lock back must not go back to the pool with it
			// still held: that would wedge every later request for this session behind it.
			_ = conn.Close(uctx)
		}
	}()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return ArtifactRow{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if maxTotal > 0 {
		var totalOthers int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(size), 0)::bigint FROM session_artifacts
			WHERE session_id = $1 AND origin = (SELECT origin FROM session_artifacts WHERE session_id = $1 AND id = $2) AND id <> $2`,
			sessionID, id).Scan(&totalOthers); err != nil {
			return ArtifactRow{}, err
		}
		if totalOthers+size > maxTotal {
			return ArtifactRow{}, ErrArtifactBytesLimit
		}
	}
	// The row's content before this update: what the bytes on disk still describe until
	// commitFile actually lands them. Kept so a commitFile that fails after this transaction
	// commits can be undone — the row restored to match the untouched file — rather than leaving
	// a row that describes bytes nothing ever wrote.
	prev, err := scanArtifact(tx.QueryRow(ctx, `SELECT `+artifactCols+` FROM session_artifacts WHERE session_id = $1 AND id = $2 AND live`, sessionID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactRow{}, errors.New("live artifact not found")
	}
	if err != nil {
		return ArtifactRow{}, fmt.Errorf("read live artifact before update: %w", err)
	}
	out, err := scanArtifact(tx.QueryRow(ctx, `
		UPDATE session_artifacts SET size = $3, content_type = $4, sha256 = $5, updated_at = now()
		 WHERE session_id = $1 AND id = $2 AND live
		RETURNING `+artifactCols, sessionID, id, size, contentType, sha256))
	if err != nil {
		return ArtifactRow{}, fmt.Errorf("update live artifact: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ArtifactRow{}, err
	}
	// Committed, still holding the session-level lock: no other update for this session can
	// start until this call releases it below, so two updates' renames can never interleave
	// independently of their commits even though this one's rename happens after its own commit.
	// From here on the row is already committed to the new content, so the rename, its retries
	// and a restore on failure must not be cut short by ctx: a client that disconnects right after
	// the commit must not leave the row describing bytes that were never written just because the
	// request that triggered them is gone. uctx carries a bound of its own so a wedged connection
	// still gives up eventually instead of leaking the lock forever.
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var commitErr error
	for i := 0; i < commitFileRetries; i++ {
		if commitErr = commitFile(); commitErr == nil {
			return out, nil
		}
	}
	if restoreErr := restoreArtifactContent(uctx, conn, sessionID, id, prev); restoreErr != nil {
		log.Printf("live artifact %s/%s: commit failed after %d retries (%v) and the row could not be restored (%v): it may still describe bytes that were never written; needs manual repair",
			sessionID, id, commitFileRetries, commitErr, restoreErr)
	} else {
		log.Printf("live artifact %s/%s: commit failed after %d retries (%v); restored the row to its previous content",
			sessionID, id, commitFileRetries, commitErr)
	}
	return ArtifactRow{}, fmt.Errorf("commit live artifact file (restored after %d retries): %w", commitFileRetries, commitErr)
}

// restoreArtifactContent undoes an UpdateLiveArtifactContent whose commitFile never landed: sets
// the row back to prev's size, content type, sha256 and updated_at. Runs on the same connection,
// still holding the session-level lock UpdateLiveArtifactContent acquired (no re-lock needed, and
// none of this is racing a concurrent update for the same session by construction), so no extra
// guard is needed against clobbering a newer write the way a fresh lock acquisition would.
func restoreArtifactContent(ctx context.Context, conn interface {
	Begin(context.Context) (pgx.Tx, error)
}, sessionID, id string, prev ArtifactRow) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE session_artifacts SET size = $3, content_type = $4, sha256 = $5, updated_at = $6
		 WHERE session_id = $1 AND id = $2 AND live`,
		sessionID, id, prev.Size, prev.ContentType, prev.SHA256, prev.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("the live row is gone; nothing to restore")
	}
	return tx.Commit(ctx)
}

// InsertLiveArtifactCapped is InsertArtifactCapped with live = true: the explicit switch to a live
// file (the name had no live row yet, so this publish becomes it), or the first publish of a brand
// new name with `--live`. Future republishes of the same name instead call UpdateLiveArtifactContent.
func InsertLiveArtifactCapped(ctx context.Context, pool *pgxpool.Pool, a ArtifactRow, limit int, maxTotal int64) (ArtifactRow, error) {
	a.Live = true
	return insertArtifactCapped(ctx, pool, a, limit, maxTotal, MaxArtifactVersions)
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
// The live row's version counts as latest, if the name has one, whatever the highest version
// number among its rows is (BLG-470 #5): during a rolling update, a server that does not know the
// live column can still insert an ordinary version over a live name, and that version's number
// can outrank the live row's. Without this, the download route would name the live row's bytes
// as an "older" version and the orphaned one as the plain, current name.
func LatestArtifactVersion(ctx context.Context, pool *pgxpool.Pool, sessionID, origin, name string) (int, error) {
	var v int
	err := pool.QueryRow(ctx, `SELECT coalesce(max(version) FILTER (WHERE live), max(version), 0) FROM session_artifacts
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
