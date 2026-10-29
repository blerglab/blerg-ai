package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LaunchSecretCleanup contains only the invocation's expected object metadata.
// It deliberately outlives sessions and never contains Secret data.
type LaunchSecretCleanup struct {
	Attempt    string
	Namespace  string
	Metadata   json.RawMessage
	ReadyAfter time.Time
}

func PutLaunchSecretCleanup(ctx context.Context, pool *pgxpool.Pool, intent LaunchSecretCleanup) error {
	_, err := pool.Exec(ctx, `INSERT INTO launch_secret_cleanup(attempt,namespace,metadata,ready_after) VALUES($1,$2,$3,$4)`, intent.Attempt, intent.Namespace, intent.Metadata, intent.ReadyAfter)
	return err
}

func DeleteLaunchSecretCleanup(ctx context.Context, pool *pgxpool.Pool, attempt string) error {
	_, err := pool.Exec(ctx, `DELETE FROM launch_secret_cleanup WHERE attempt=$1`, attempt)
	return err
}

func ListLaunchSecretCleanup(ctx context.Context, pool *pgxpool.Pool, namespace string) ([]LaunchSecretCleanup, error) {
	rows, err := pool.Query(ctx, `SELECT attempt::text,namespace,metadata,ready_after FROM launch_secret_cleanup WHERE namespace=$1 AND ready_after<=now() ORDER BY ready_after LIMIT 100`, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var intents []LaunchSecretCleanup
	for rows.Next() {
		var intent LaunchSecretCleanup
		if err := rows.Scan(&intent.Attempt, &intent.Namespace, &intent.Metadata, &intent.ReadyAfter); err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	return intents, rows.Err()
}

// Pin before deletion. Later reconciliation must never accept a replacement
// UID, even when a copied annotation still carries the original marker.
func PinLaunchSecretCleanupUID(ctx context.Context, pool *pgxpool.Pool, attempt, uid string) error {
	tag, err := pool.Exec(ctx, `UPDATE launch_secret_cleanup SET metadata=jsonb_set(metadata,'{uid}',to_jsonb($2::text)) WHERE attempt=$1 AND (COALESCE(metadata->>'uid','')='' OR metadata->>'uid'=$2)`, attempt, uid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("launch cleanup UID changed")
	}
	return nil
}

// Rotate unresolved obligations so a batch of foreign or unavailable objects
// cannot indefinitely starve later cleanup work.
func RetryLaunchSecretCleanup(ctx context.Context, pool *pgxpool.Pool, attempt string) error {
	_, err := pool.Exec(ctx, `UPDATE launch_secret_cleanup SET ready_after=now()+interval '1 minute' WHERE attempt=$1`, attempt)
	return err
}
