package db

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// GetAccountPreference returns the stored value of one preference, or nil when the account has not set it.
func GetAccountPreference(ctx context.Context, q Querier, accountID, key string) (json.RawMessage, error) {
	var v []byte
	err := q.QueryRow(ctx, `SELECT value FROM account_preferences WHERE account_id=$1 AND key=$2`, accountID, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(v), nil
}

// PutAccountPreference stores (replaces) one preference of one account.
func PutAccountPreference(ctx context.Context, q Querier, accountID, key string, value json.RawMessage) error {
	_, err := q.Exec(ctx, `INSERT INTO account_preferences (account_id, key, value) VALUES ($1,$2,$3)
		ON CONFLICT (account_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, accountID, key, []byte(value))
	return err
}
