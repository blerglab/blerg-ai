package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the §6.8 persistence seam. Handlers depend on this, never on pgx directly.
type Store interface {
	Pool() *pgxpool.Pool
	Ping(ctx context.Context) error
}
