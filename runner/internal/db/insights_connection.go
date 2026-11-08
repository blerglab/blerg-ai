package db

import (
	"context"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"sync"
	"time"
)

// These bounded interactive reads are too small to recover JIT compilation
// costs. Restore the connection's setting before returning it to the pool.
func insightsConnection(ctx context.Context, pool *pgxpool.Pool) (*pgxpool.Conn, func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	var jit string
	if err = conn.QueryRow(ctx, "SHOW jit").Scan(&jit); err != nil {
		conn.Release()
		return nil, nil, err
	}
	if _, err = conn.Exec(ctx, "SET jit=off"); err != nil {
		conn.Release()
		return nil, nil, err
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			if _, err := conn.Exec(cleanup, "SELECT set_config('jit',$1,false)", jit); err != nil {
				failurelog.Default.Event("insights_connection_reset", slog.LevelWarn, "operation", "insights_connection_reset", "cause", failurelog.Cause(err))
				_ = conn.Hijack().Close(cleanup)
				return
			}
			conn.Release()
		})
	}
	return conn, release, nil
}
