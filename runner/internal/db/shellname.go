package db

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
)

func SaveShellName(ctx context.Context, pool *pgxpool.Pool, session, owner, login, shell, name string) error {
	if name == "" {
		return DeleteShellName(ctx, pool, session, owner, login, shell)
	}
	result, err := pool.Exec(ctx, `INSERT INTO shell_names(shell_id,session_id,owner_id,login_id,name) VALUES($1,$2,$3,$4,$5) ON CONFLICT(shell_id) DO UPDATE SET name=EXCLUDED.name WHERE shell_names.session_id=EXCLUDED.session_id AND shell_names.owner_id=EXCLUDED.owner_id AND shell_names.login_id=EXCLUDED.login_id`, shell, session, owner, login, name)
	if err == nil && result.RowsAffected() != 1 {
		return errors.New("shell name scope mismatch")
	}
	return err
}
func ShellNames(ctx context.Context, pool *pgxpool.Pool, session, owner, login string) (map[string]string, error) {
	rows, err := pool.Query(ctx, `SELECT shell_id,name FROM shell_names WHERE session_id=$1 AND owner_id=$2 AND login_id=$3`, session, owner, login)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// DeleteShellName removes presentation data within the exact original scope.
func DeleteShellName(ctx context.Context, pool *pgxpool.Pool, session, owner, login, shell string) error {
	_, err := pool.Exec(ctx, `DELETE FROM shell_names WHERE shell_id=$1 AND session_id=$2 AND owner_id=$3 AND login_id=$4`, shell, session, owner, login)
	return err
}
