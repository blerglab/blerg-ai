package db

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Ready checks database access and all migrations required by this binary.
// Extra migrations from a newer rolling-update replica are allowed.
func Ready(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("database unavailable")
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	var complete bool
	err = pool.QueryRow(ctx, `SELECT count(*) = cardinality($1::text[]) FROM schema_migrations WHERE name = ANY($1::text[])`, names).Scan(&complete)
	if err != nil {
		return err
	}
	if !complete {
		return errors.New("database migrations pending")
	}
	return nil
}
