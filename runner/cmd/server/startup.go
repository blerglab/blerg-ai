package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/blerglab/blerg-ai/contracts/health"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openDatabase validates configuration once, then waits through database restarts.
// gate, if set, is told why each attempt failed, so /readyz can say it after a sustained failure.
func openDatabase(ctx context.Context, dsn string, gate *health.Gate) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	if config.ConnConfig.ConnectTimeout == 0 {
		config.ConnConfig.ConnectTimeout = 5 * time.Second
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	retry := health.Retry{Logf: log.Printf, Gate: gate, Secrets: []string{dsn, config.ConnConfig.Password}}
	if err := health.RetryDatabase(ctx, func(ctx context.Context) error { return db.RunMigrations(ctx, pool) }, retry); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// readinessCheck is what /readyz asks once the server is up: the database and its migrations,
// or nothing when the runner runs with no database configured (dev mode), where it is ready as
// soon as it serves.
func readinessCheck(pool *pgxpool.Pool) func(context.Context) error {
	if pool == nil {
		return func(context.Context) error { return nil }
	}
	return func(ctx context.Context) error { return db.Ready(ctx, pool) }
}
