package db

// Indexes built outside the migration transaction (0.4.5). RunMigrations applies every migration in
// one transaction, so an index a migration builds on a large table holds that table's writes for the
// whole build, and during a rolling update the old runner, still serving, waits on it. An index that
// only speeds up a read is built here instead, with CREATE INDEX CONCURRENTLY after the migrations:
// writes go on while it builds, and the read it serves works (more slowly) until it exists.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// concurrentIndex is one index EnsureConcurrentIndexes builds: its name and its definition after
// "CREATE INDEX CONCURRENTLY IF NOT EXISTS <name>".
type concurrentIndex struct {
	name, def string
}

// concurrentIndexes are built in this order.
var concurrentIndexes = []concurrentIndex{
	// The History tab's rename rows (ListSessionRenamedEvents), as 067's index serves its card chips.
	{"agent_events_session_renamed_idx", "ON agent_events (session_id, seq) WHERE kind = 'session_renamed'"},
	// The History feed's strokes (ListStrokeEvents): marks, asks, approvals, proposals and reviews.
	{"agent_events_strokes_idx", "ON agent_events (session_id, seq) WHERE kind IN " + StrokeKindsSQL},
}

var literalRe = regexp.MustCompile(`'([^']*)'`)

// sameLiterals reports whether two index definitions name the same quoted values (the kinds of a
// partial index's predicate): Postgres writes `kind IN (...)` back as `kind = ANY (ARRAY[...])`, so
// the values, not the text, are compared.
func sameLiterals(a, b string) bool {
	set := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, m := range literalRe.FindAllStringSubmatch(s, -1) {
			out[m[1]] = true
		}
		return out
	}
	x, y := set(a), set(b)
	if len(x) != len(y) {
		return false
	}
	for k := range x {
		if !y[k] {
			return false
		}
	}
	return true
}

// ErrConcurrentIndexesBusy: another runner holds the build lock and is building or repairing the
// indexes now; try again later.
var ErrConcurrentIndexesBusy = errors.New("another runner is building the indexes")

// concurrentIndexLock is the session-level advisory lock one builder holds for the whole pass.
const concurrentIndexLock = "blerg_runner_concurrent_indexes"

// EnsureConcurrentIndexes builds each index of concurrentIndexes that is missing, without blocking
// writes. A build that failed earlier leaves an invalid index behind; it is dropped (concurrently) and
// built again. It runs on a connection of its own, under a session advisory lock, so only one runner
// builds or repairs at a time: an index another runner is still building is invalid while it builds,
// and must never be taken for a failed one and dropped. A runner that does not get the lock returns
// ErrConcurrentIndexesBusy at once, touching nothing. A crashed builder's lock ends with its
// connection, and its half-built index is then repaired by the next pass.
func EnsureConcurrentIndexes(ctx context.Context, pool *pgxpool.Pool) error {
	acquired, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire a connection for the indexes: %w", err)
	}
	defer acquired.Release()
	conn := acquired.Conn()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, concurrentIndexLock).Scan(&locked); err != nil {
		return fmt.Errorf("take the index build lock: %w", err)
	}
	if !locked {
		return ErrConcurrentIndexesBusy
	}
	defer func() {
		// The connection goes back to the pool: give the lock back on it, even if ctx ended.
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock(hashtext($1))`, concurrentIndexLock); err != nil {
			// A connection that could not give the lock back must not be reused with it held.
			_ = conn.Close(uctx)
		}
	}()
	for _, ix := range concurrentIndexes {
		var valid *bool
		err := conn.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			WHERE c.relname = $1 AND c.relnamespace = current_schema()::regnamespace`, ix.name).Scan(&valid)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check index %s: %w", ix.name, err)
		}
		if valid != nil && *valid {
			// A valid index built from an older definition (its kind list changed) no longer serves the
			// read whose predicate names the new list: drop it and build the current one.
			var def string
			if err := conn.QueryRow(ctx, `SELECT pg_get_indexdef(c.oid) FROM pg_class c
				WHERE c.relname = $1 AND c.relnamespace = current_schema()::regnamespace`, ix.name).Scan(&def); err != nil {
				return fmt.Errorf("read index %s: %w", ix.name, err)
			}
			if sameLiterals(def, ix.def) {
				continue
			}
			log.Printf("db: index %s has an older definition; building it again", ix.name)
		} else if valid != nil {
			log.Printf("db: index %s is invalid (an earlier build failed); building it again", ix.name)
		}
		if valid != nil {
			if _, err := conn.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+ix.name); err != nil {
				return fmt.Errorf("drop invalid index %s: %w", ix.name, err)
			}
		}
		if _, err := conn.Exec(ctx, `CREATE INDEX CONCURRENTLY IF NOT EXISTS `+ix.name+` `+ix.def); err != nil {
			return fmt.Errorf("build index %s: %w", ix.name, err)
		}
		log.Printf("db: built index %s", ix.name)
	}
	return nil
}
