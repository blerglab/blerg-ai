package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// RunMigrations creates the schema_migrations table if it does not exist and
// then applies any SQL migration files that have not yet been recorded.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	// Acquire a dedicated connection so all migration operations (including the
	// advisory lock and nested transactions) share the same underlying pg conn.
	acquired, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for migrations: %w", err)
	}
	defer acquired.Release()
	conn := acquired.Conn()

	// Ensure the tracking table exists.
	_, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   text        PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// Begin a transaction and acquire a transaction-scoped advisory lock to
	// serialise concurrent RunMigrations callers (e.g. two pods starting
	// simultaneously) so they cannot race on the schema_migrations INSERT.
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migrations transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit; on an error path the original error is what gets returned

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('blerg_runner_migrations'))`); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	// Collect already-applied filenames within the locked transaction.
	rows, err := tx.Query(ctx, `SELECT filename FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("query schema_migrations: %w", err)
	}
	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan schema_migrations row: %w", err)
		}
		applied[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema_migrations: %w", err)
	}

	// Collect .sql files from the embedded FS.
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		if applied[name] {
			continue
		}

		data, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		// Each migration runs in its own nested transaction (savepoint) so
		// that a crash between executing the SQL and recording it in
		// schema_migrations cannot leave the DB in a split state.
		migTx, err := tx.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration transaction %s: %w", name, err)
		}

		if _, err := migTx.Exec(ctx, string(data)); err != nil {
			migTx.Rollback(ctx) //nolint:errcheck // the migration error is already being returned; a failed rollback adds nothing
			return fmt.Errorf("apply migration %s: %w", name, err)
		}

		if _, err := migTx.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, name,
		); err != nil {
			migTx.Rollback(ctx) //nolint:errcheck // the migration error is already being returned; a failed rollback adds nothing
			return fmt.Errorf("record migration %s: %w", name, err)
		}

		if err := migTx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}

	return tx.Commit(ctx)
}
