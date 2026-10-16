package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ClusterModelCatalog contains only a sanitized model catalog. Callers must strip
// credentials, credential fingerprints and authorization proofs before storing it.
type ClusterModelCatalog struct {
	AccountID string
	Engine    string
	Image     string
	Payload   json.RawMessage
	FetchedAt time.Time
}

func LoadClusterModelCatalog(ctx context.Context, pool *pgxpool.Pool, accountID, engine, image string) (json.RawMessage, time.Time, bool, error) {
	var raw json.RawMessage
	var at time.Time
	err := pool.QueryRow(ctx, `SELECT payload,fetched_at FROM cluster_engine_model_catalogs WHERE account_id=$1 AND engine=$2 AND image=$3`, accountID, engine, image).Scan(&raw, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	return raw, at, true, nil
}

// StoreClusterModelCatalog ignores older refreshes finishing after a newer one.
func StoreClusterModelCatalog(ctx context.Context, pool *pgxpool.Pool, accountID, engine, image string, raw json.RawMessage, fetchedAt time.Time) error {
	_, err := pool.Exec(ctx, `INSERT INTO cluster_engine_model_catalogs(account_id,engine,image,payload,fetched_at) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(account_id,engine,image) DO UPDATE SET payload=EXCLUDED.payload,fetched_at=EXCLUDED.fetched_at
 WHERE EXCLUDED.fetched_at>=cluster_engine_model_catalogs.fetched_at`, accountID, engine, image, raw, fetchedAt)
	return err
}

// ListClusterModelCatalogs loads the newest entries for bounded startup preload.
func ListClusterModelCatalogs(ctx context.Context, pool *pgxpool.Pool, limit int) ([]ClusterModelCatalog, error) {
	if limit <= 0 {
		limit = 128
	}
	limit = min(limit, 1024)
	rows, err := pool.Query(ctx, `SELECT account_id,engine,image,payload,fetched_at FROM cluster_engine_model_catalogs ORDER BY fetched_at DESC,account_id,engine,image LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ClusterModelCatalog, 0)
	for rows.Next() {
		var c ClusterModelCatalog
		if err = rows.Scan(&c.AccountID, &c.Engine, &c.Image, &c.Payload, &c.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
