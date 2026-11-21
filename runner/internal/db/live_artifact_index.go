package db

// The live-file unique index (BLG-470), built the same way as concurrent_indexes.go's: outside
// the migration transaction, with CREATE INDEX CONCURRENTLY, so a session_artifacts table with a
// lot of published files does not hold writes for the whole build. Kept separate from
// EnsureConcurrentIndexes because that one's repair logic is shaped for its own two indexes
// (agent_events, a predicate naming a kind list); this index needs none of that: it is new in
// this release, not a redefinition of an older one.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const liveArtifactIndexName = "session_artifacts_live_name_idx"
const liveArtifactIndexDef = "ON session_artifacts (session_id, origin, name) WHERE live"
const liveArtifactIndexLock = "blerg_runner_live_artifact_index"

// ErrLiveArtifactIndexBusy: another runner holds the build lock and is building or repairing the
// index now; try again later.
var ErrLiveArtifactIndexBusy = errors.New("another runner is building the live artifact index")

// ErrLiveArtifactIndexCannotBuild: the build itself failed on a duplicate-key violation even
// after the repair pass ran in this same call — a bug in the repair, or a race too fast for it to
// close (BLG-470 round 2 #4). Retrying on a timer cannot fix this by itself (nothing about the
// data changes between retries): the caller should stop looping and page a human instead of
// logging the same cause forever.
var ErrLiveArtifactIndexCannotBuild = errors.New("the live artifact index cannot be built: duplicate live rows survived the repair pass")

// repairDuplicateLiveRows keeps, for every (session, origin, name) with more than one live=true
// row, only the one with the latest updated_at (ties broken by id, for a deterministic result);
// the rest are marked live=false, becoming ordinary history. InsertLiveArtifactCapped's own
// lock-protected check (artifacts.go) stops new duplicates from appearing once it ships, but this
// repairs rows a server from before that fix (or before this index existed at all) may have left
// behind — otherwise CREATE UNIQUE INDEX CONCURRENTLY below fails on them forever. Idempotent and
// cheap when there is nothing to fix, so it runs on every call, not just when (re)building.
func repairDuplicateLiveRows(ctx context.Context, conn interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (int, error) {
	rows, err := conn.Query(ctx, `
		WITH ranked AS (
			SELECT id, row_number() OVER (PARTITION BY session_id, origin, name ORDER BY updated_at DESC, id DESC) AS rn
			FROM session_artifacts WHERE live
		)
		UPDATE session_artifacts SET live = false
		 WHERE id IN (SELECT id FROM ranked WHERE rn > 1)
		RETURNING id`)
	if err != nil {
		return 0, fmt.Errorf("repair duplicate live rows: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

// EnsureLiveArtifactIndex builds the at-most-one-live-row-per-name index if it is missing,
// without blocking writes to session_artifacts. An index left invalid by an earlier failed build
// is dropped (concurrently) and built again. Only one runner builds or repairs it at a time, under
// a session-level advisory lock; a runner that does not get the lock returns
// ErrLiveArtifactIndexBusy at once, touching nothing.
//
// InsertLiveArtifactCapped's own lock-protected check (artifacts.go) already stops two
// concurrent first-time `--live` publishes from both inserting a live row for the same new name,
// without needing this index at all; this index is the second line of defense, for any path that
// does not hold that lock. It also repairs any duplicate live rows a server from before that
// check (or before this index existed) may have left behind (repairDuplicateLiveRows), since those
// would otherwise make the build below fail forever.
func EnsureLiveArtifactIndex(ctx context.Context, pool *pgxpool.Pool) error {
	acquired, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire a connection for the live artifact index: %w", err)
	}
	defer acquired.Release()
	conn := acquired.Conn()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, liveArtifactIndexLock).Scan(&locked); err != nil {
		return fmt.Errorf("take the live artifact index build lock: %w", err)
	}
	if !locked {
		return ErrLiveArtifactIndexBusy
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock(hashtext($1))`, liveArtifactIndexLock); err != nil {
			_ = conn.Close(uctx)
		}
	}()

	var valid *bool
	err = conn.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = $1 AND c.relnamespace = current_schema()::regnamespace`, liveArtifactIndexName).Scan(&valid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check the live artifact index: %w", err)
	}
	if valid != nil && *valid {
		return nil
	}
	// Not built yet, or an earlier build failed: repair any rows that would make the build fail
	// before attempting it. Once the index exists and is valid, duplicates cannot occur, so this
	// never needs to run again after this call succeeds.
	if repaired, err := repairDuplicateLiveRows(ctx, conn); err != nil {
		return fmt.Errorf("before building the live artifact index: %w", err)
	} else if repaired > 0 {
		log.Printf("db: demoted %d duplicate live row(s) to history before building the live artifact index", repaired)
	}
	if valid != nil {
		if _, err := conn.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+liveArtifactIndexName); err != nil {
			return fmt.Errorf("drop the invalid live artifact index: %w", err)
		}
	}
	if _, err := conn.Exec(ctx, `CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS `+liveArtifactIndexName+` `+liveArtifactIndexDef); err != nil {
		if isUniqueViolationPgErr(err) {
			log.Printf("db: live artifact index build failed on a duplicate-key violation even after the repair pass: %v", err)
			return ErrLiveArtifactIndexCannotBuild
		}
		return fmt.Errorf("build the live artifact index: %w", err)
	}
	return nil
}

// isUniqueViolationPgErr reports whether err is a Postgres unique-constraint failure (23505).
func isUniqueViolationPgErr(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
