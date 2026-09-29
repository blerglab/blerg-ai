package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PgStore struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*PgStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &PgStore{pool: pool}, nil
}

func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool}
}

func (s *PgStore) Pool() *pgxpool.Pool            { return s.pool }
func (s *PgStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *PgStore) Close()                         { s.pool.Close() }
