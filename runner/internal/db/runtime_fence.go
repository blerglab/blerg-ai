package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Advisory locks use a separate, bounded pool: callbacks need the main pool,
// including installations configured with MaxConns=1. Canonical configurations
// share warm lock connections across sessions and test schemas; no new TCP
// connection is opened per native frame. Keys remain private process memory.
var runtimeFencePools = struct {
	sync.Mutex
	pools map[string]*pgxpool.Pool
}{pools: map[string]*pgxpool.Pool{}}

func runtimeFencePool(ctx context.Context, main *pgxpool.Pool) (*pgxpool.Pool, error) {
	cfg := main.Config().Copy()
	delete(cfg.ConnConfig.RuntimeParams, "search_path") // advisory locks access no application tables
	cfg.ConnConfig.Tracer = nil
	key := cfg.ConnConfig.ConnString()
	// ConnString preserves original URL parameters even after RuntimeParams is
	// edited. Schemas do not partition PostgreSQL advisory locks.
	if parsed, e := url.Parse(key); e == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		query := parsed.Query()
		query.Del("search_path")
		parsed.RawQuery = query.Encode()
		key = parsed.String()
	}
	runtimeFencePools.Lock()
	defer runtimeFencePools.Unlock()
	if pool := runtimeFencePools.pools[key]; pool != nil {
		return pool, nil
	}
	cfg.MaxConns = lockPoolSize()
	cfg.MinConns = 0
	cfg.MinIdleConns = 0
	cfg.MaxConnIdleTime = 30 * time.Second
	cfg.MaxConnLifetime = 10 * time.Minute
	cfg.HealthCheckPeriod = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	runtimeFencePools.pools[key] = pool
	return pool, nil
}

// lockPoolSessions reports the configured concurrent session cap; the bounded
// advisory-lock pools are sized from it when first used. Unset means the
// default cap of 4.
var lockPoolSessions atomic.Pointer[func() int]

// SetLockPoolSessions sizes the advisory-lock pools from the session cap. Call
// it at startup, before the first runtime frame; a pool keeps its size once made.
func SetLockPoolSessions(fn func() int) { lockPoolSessions.Store(&fn) }

const (
	lockPoolFloor   = 6
	lockPoolCeiling = 32
)

// lockPoolSize is two connections per session (a pod's read and write pumps
// each hold one while they wait on the session fence) plus two, within a floor
// and a ceiling that keeps PostgreSQL's connection budget safe.
func lockPoolSize() int32 {
	sessions := 4
	if fn := lockPoolSessions.Load(); fn != nil && *fn != nil {
		sessions = (*fn)()
	}
	return int32(min(max(2*sessions+2, lockPoolFloor), lockPoolCeiling))
}

// ErrRuntimeFenceBusy means the fence could not be taken (pool or lock wait
// timed out, or the database refused); the frame's handler never ran, so the
// caller may retry it.
var ErrRuntimeFenceBusy = errors.New("runtime fence busy")

// WithRuntimeLaunchFenceRetry is WithRuntimeLaunchFence retried once when the
// fence itself could not be taken, so one database latency spike does not drop
// a pod's socket.
func WithRuntimeLaunchFenceRetry(parent context.Context, main *pgxpool.Pool, session string, fn func(context.Context) error) error {
	err := WithRuntimeLaunchFence(parent, main, session, fn)
	if errors.Is(err, ErrRuntimeFenceBusy) && parent.Err() == nil {
		err = WithRuntimeLaunchFence(parent, main, session, fn)
	}
	return err
}

// WithRuntimeLaunchFence linearizes an entire accepted runtime frame (or
// handshake/disconnect) against launch rotation across Runner replicas. The
// transaction owns only its advisory lock; application handlers keep using
// the independent main pool. Cancellation bounds DB waits and handler I/O.
func WithRuntimeLaunchFence(parent context.Context, main *pgxpool.Pool, session string, fn func(context.Context) error) error {
	if main == nil {
		return ErrTokenInvalid
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	pool, err := runtimeFencePool(ctx, main)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRuntimeFenceBusy, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRuntimeFenceBusy, err)
	}
	defer func() {
		cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancelCleanup()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text,876433))`, session); err != nil {
		return fmt.Errorf("%w: %w", ErrRuntimeFenceBusy, err)
	}
	if err = fn(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WithRuntimeAuthority marks writes made by an authenticated scoped runtime.
// Only server-side authentication code supplies this token ID; host writes
// retain their existing recovery behavior.
func WithRuntimeAuthority(ctx context.Context, tokenID string) context.Context {
	return context.WithValue(ctx, runtimeAuthorityKey{}, tokenID)
}

type runtimeAuthorityKey struct{}

func runtimeAuthorityToken(ctx context.Context) string {
	token, _ := ctx.Value(runtimeAuthorityKey{}).(string)
	return token
}

// The predicate is evaluated in the state-changing statement, so a stop that
// wins after frame authentication cannot be undone by that admitted frame.
func runtimeWriteGuard(parameter string) string {
	return strings.ReplaceAll(`(RUNTIME::text = '' OR (
 sessions.runtime='cluster' AND sessions.ended_at IS NULL
 AND sessions.status NOT IN ('stopped','ended')
 AND EXISTS (SELECT 1 FROM session_runtime r JOIN board_tokens t ON t.id=r.runtime_token_id
 WHERE r.session_id=sessions.id AND r.runtime_token_id=NULLIF(RUNTIME,'')::uuid
 AND t.revoked_at IS NULL AND t.expires_at>now())))`, "RUNTIME", parameter)
}

// HasRuntimeAuthority reports whether a lifecycle write must fail closed.
func HasRuntimeAuthority(ctx context.Context) bool { return runtimeAuthorityToken(ctx) != "" }
