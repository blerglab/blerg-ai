package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// InsertArtifactWithVersionLimit exercises the production transaction with a small cap.
func InsertArtifactWithVersionLimit(ctx context.Context, pool *pgxpool.Pool, a ArtifactRow, maxVersions int) (ArtifactRow, error) {
	return insertArtifactCapped(ctx, pool, a, 50, 0, maxVersions)
}
