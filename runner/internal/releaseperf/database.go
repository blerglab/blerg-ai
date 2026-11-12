package releaseperf

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenPool opens a connection pool to the given DSN, for seeding a database
// a real server binary has already migrated.
func OpenPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, dsn)
}

// CreateDisposableDatabase drops any existing database of the given name and
// creates a fresh, empty one on the server named by adminDSN, returning a
// DSN for the new database. It is always this tool's own, never the shared
// TEST_DATABASE_URL database other tests use.
func CreateDisposableDatabase(ctx context.Context, adminDSN, name string) (string, error) {
	cfg, err := pgxpool.ParseConfig(adminDSN)
	if err != nil {
		return "", fmt.Errorf("parse admin DSN: %w", err)
	}
	cfg.MaxConns = 2
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("connect to administer %s: %w", name, err)
	}
	defer pool.Close()
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := pool.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
		return "", fmt.Errorf("drop stale %s: %w", name, err)
	}
	if _, err := pool.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		return "", fmt.Errorf("create %s: %w", name, err)
	}
	return withDatabaseName(adminDSN, name)
}

// DropDatabase removes the named database if present. Called on every exit
// path, success or failure.
func DropDatabase(ctx context.Context, adminDSN, name string) error {
	cfg, err := pgxpool.ParseConfig(adminDSN)
	if err != nil {
		return fmt.Errorf("parse admin DSN: %w", err)
	}
	cfg.MaxConns = 2
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect to administer %s: %w", name, err)
	}
	defer pool.Close()
	ident := pgx.Identifier{name}.Sanitize()
	_, err = pool.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
	return err
}

// SettleDatabase runs VACUUM ANALYZE and CHECKPOINT on the database named by
// dsn, settling Postgres' own background autovacuum/checkpoint I/O from a
// bulk seed before the host-quiet check runs — otherwise that check would
// see the tool's own seeding work and could falsely refuse its own run.
func SettleDatabase(ctx context.Context, dsn string) error {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse DSN: %w", err)
	}
	// VACUUM/CHECKPOINT cannot run inside pgx's pooled transaction wrapping;
	// use a dedicated single connection.
	cfg.MaxConns = 1
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect to settle database: %w", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "VACUUM ANALYZE"); err != nil {
		return fmt.Errorf("VACUUM ANALYZE: %w", err)
	}
	if _, err := pool.Exec(ctx, "CHECKPOINT"); err != nil {
		return fmt.Errorf("CHECKPOINT: %w", err)
	}
	return nil
}

// ReadLocalAccountID looks up the real account id core assigned when the
// release-perf account was created through the real /auth/setup page (H1:
// not a hand-rolled row). The browser's own access token lives only in an
// in-page JS module variable (authClient.ts), out of reach of a plain
// fetch, so the orchestrator reads the id back directly from core's own
// database instead of asking the page for it.
func ReadLocalAccountID(ctx context.Context, pool *pgxpool.Pool, username string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE provider = 'local' AND provider_subject = $1`, username).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("read account id for %q: %w", username, err)
	}
	return id, nil
}

func withDatabaseName(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse DSN: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}
