package db

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WithDesktopSnapshotFences holds several desktop ownership locks using one
// connection. Nested single-desktop fences could exhaust the lock pool when
// a person has many desktops. Sorted IDs give every replica the same order.
func WithDesktopSnapshotFences(parent context.Context, main *pgxpool.Pool, ids []string, fn func(context.Context) error) error {
	if len(ids) == 0 {
		return fn(parent)
	}
	if main == nil {
		return ErrDesktopAuthority
	}
	ctx, cancel := context.WithTimeout(parent, runtimeFenceTimeout)
	defer cancel()
	pool, err := runtimeFencePool(ctx, main)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer done()
		_ = tx.Rollback(cleanup)
	}()
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	for i, id := range ordered {
		if i > 0 && ordered[i-1] == id {
			continue
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text,876434))`, id); err != nil {
			return err
		}
	}
	if err := fn(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
