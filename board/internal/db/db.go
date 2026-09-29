// Package db is blerg-board's Postgres layer: schema migrations, boards, cards,
// tokens, admission reviews, and events. All card mutations bump `version`
// (uniform versioning — see the design spec) and ordering operations serialise
// on the board row lock.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Sentinel errors mapped to HTTP statuses by the API layer.
var (
	ErrNotFound          = errors.New("not found")
	ErrStaleVersion      = errors.New("stale version")
	ErrInvalidRepos      = errors.New("repos must be a non-empty subset of the board's repos")
	ErrColumnHasCards    = errors.New("column has live cards")
	ErrDependencyCycle   = errors.New("dependency would create a cycle")
	ErrDuplicateDedupKey = errors.New("duplicate dedup key")
	ErrDuplicateExternal = errors.New("duplicate external id")
	// ErrCardMoved: a guarded claim (ClaimCard) found the card somewhere
	// other than where the caller last saw it — someone else got there first.
	ErrCardMoved = errors.New("card is no longer in the column it was claimed from")
)

// Connect opens a pool and runs migrations.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := RunMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// RunMigrations applies embedded SQL migrations under a transaction-scoped
// advisory lock so concurrent starters cannot race.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	acquired, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for migrations: %w", err)
	}
	defer acquired.Release()
	conn := acquired.Conn()

	_, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   text        PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migrations transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after a successful commit is a no-op; the commit error is what is returned

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('blerg_board_migrations'))`); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

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
		// Each migration in its own savepoint so a crash between executing and
		// recording cannot leave the DB split.
		migTx, err := tx.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration transaction %s: %w", name, err)
		}
		if _, err := migTx.Exec(ctx, string(data)); err != nil {
			migTx.Rollback(ctx) //nolint:errcheck // best-effort rollback; the migration error is what is returned
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := migTx.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, name,
		); err != nil {
			migTx.Rollback(ctx) //nolint:errcheck // best-effort rollback; the migration error is what is returned
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := migTx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}

	return tx.Commit(ctx)
}

// lockBoard takes the board row lock that serialises number allocation, rank
// allocation, and column reordering for one board. Returns ErrNotFound when
// the board does not exist.
func lockBoard(ctx context.Context, tx pgx.Tx, boardID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM boards WHERE id = $1 FOR UPDATE`, boardID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
