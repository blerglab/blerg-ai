package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrLimitResumeBusy = errors.New("limit resume busy")

// The session UUID supplies stable random staggering across replicas/restarts.
// The same expression gates both SQL selection and the atomic claim.
const limitResumeDueSQL = "reset_at + (60 + (get_byte(sha256(convert_to(session_id::text,'UTF8')),0) % 31)) * interval '1 second'"

func LimitResumeDueAt(id string, reset time.Time) time.Time {
	sum := sha256.Sum256([]byte(id))
	return reset.Add(time.Minute + time.Duration(sum[0]%31)*time.Second)
}

type LimitResume struct {
	WindowID          string     `json:"-"`
	RefusedAt         *time.Time `json:"-"`
	LastAttemptTurnID string     `json:"-"`
	DispatchedAt      *time.Time `json:"-"`
	Provider          string     `json:"-"`
	Enabled           bool       `json:"enabled"`
	State             string     `json:"state"`
	Reason            string     `json:"reason"`
	ResetAt           *time.Time `json:"reset_at"`
	Attempts          int        `json:"attempts"`
	AuthSessionID     string     `json:"-"`
	TurnID            string     `json:"-"`
	CancelledTurnID   string     `json:"-"`
	EventSeq          int64      `json:"-"`
	CancelledAt       *time.Time `json:"-"`
	ExpiresAt         *time.Time `json:"-"`
	AttemptID         string     `json:"-"`
}

func GetLimitResume(ctx context.Context, q Querier, id string) (*LimitResume, error) {
	r := &LimitResume{State: "disabled"}
	if q == nil {
		return r, nil
	}
	err := q.QueryRow(ctx, `SELECT window_id,refused_at,last_attempt_turn_id,dispatched_at,provider,enabled,state,reason,reset_at,attempts,auth_session_id,turn_id,cancelled_turn_id,event_seq,cancelled_at,expires_at,attempt_id FROM session_limit_resume WHERE session_id=$1`, id).Scan(&r.WindowID, &r.RefusedAt, &r.LastAttemptTurnID, &r.DispatchedAt, &r.Provider, &r.Enabled, &r.State, &r.Reason, &r.ResetAt, &r.Attempts, &r.AuthSessionID, &r.TurnID, &r.CancelledTurnID, &r.EventSeq, &r.CancelledAt, &r.ExpiresAt, &r.AttemptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil
	}
	return r, err
}

// limitResumeLockPools holds one bounded lock pool per main pool. Callbacks run
// application queries on the lock connection, so it carries the main pool's
// search_path; reusing warm connections keeps a hot path (every user_message,
// browser send and heartbeat replay) from dialing PostgreSQL on each call.
var limitResumeLockPools = struct {
	sync.Mutex
	pools map[*pgxpool.Pool]*pgxpool.Pool
}{pools: map[*pgxpool.Pool]*pgxpool.Pool{}}

func limitResumeLockPool(ctx context.Context, main *pgxpool.Pool) (*pgxpool.Pool, error) {
	limitResumeLockPools.Lock()
	defer limitResumeLockPools.Unlock()
	if pool := limitResumeLockPools.pools[main]; pool != nil {
		return pool, nil
	}
	var searchPath string
	if err := main.QueryRow(ctx, `SHOW search_path`).Scan(&searchPath); err != nil {
		return nil, err
	}
	cfg := main.Config().Copy()
	cfg.MaxConns = lockPoolSize()
	cfg.MinConns = 0
	cfg.MinIdleConns = 0
	cfg.MaxConnIdleTime = 15 * time.Second
	cfg.MaxConnLifetime = 10 * time.Minute
	cfg.HealthCheckPeriod = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["search_path"] = searchPath
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	limitResumeLockPools.pools[main] = pool
	return pool, nil
}

// WithLimitResumeLock serializes session messages, cancellation and dispatch across replicas.
// It does not wait: a held lock returns ErrLimitResumeBusy at once.
// A failed unlock destroys the connection instead of returning a locked connection to the pool.
func WithLimitResumeLock(ctx context.Context, pool *pgxpool.Pool, id string, fn func(*pgxpool.Conn) error) error {
	return withLimitResumeLock(ctx, pool, id, 0, fn)
}

// WithLimitResumeLockWait is WithLimitResumeLock that blocks up to wait for a
// held lock before returning ErrLimitResumeBusy.
func WithLimitResumeLockWait(ctx context.Context, pool *pgxpool.Pool, id string, wait time.Duration, fn func(*pgxpool.Conn) error) error {
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return withLimitResumeLock(ctx, pool, id, wait, fn)
}

func withLimitResumeLock(ctx context.Context, pool *pgxpool.Pool, id string, wait time.Duration, fn func(*pgxpool.Conn) error) error {
	// Locks must not reserve a main-pool connection: callbacks and nested locks
	// may need that pool, which can be configured with only one connection.
	lockCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	lockPool, err := limitResumeLockPool(lockCtx, pool)
	if err != nil {
		return err
	}
	c, err := lockPool.Acquire(lockCtx)
	if err != nil {
		return err
	}
	defer c.Release()
	key := "limit_resume:" + id
	discard := func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = c.Conn().Close(closeCtx)
	}
	if wait > 0 {
		// lock_timeout bounds pg_advisory_lock; it is reset before the
		// connection goes back to the pool (or the connection is discarded).
		if _, err = c.Exec(lockCtx, `SELECT set_config('lock_timeout',$1,false)`, fmt.Sprintf("%dms", wait.Milliseconds())); err != nil {
			discard()
			return err
		}
		_, err = c.Exec(lockCtx, `SELECT pg_advisory_lock(hashtextextended($1,104729))`, key)
		if _, e := c.Exec(lockCtx, `SELECT set_config('lock_timeout','0',false)`); e != nil {
			discard()
			if err == nil {
				return e
			}
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return ErrLimitResumeBusy
		}
		if err != nil {
			return err
		}
	} else {
		var locked bool
		if err = c.QueryRow(lockCtx, `SELECT pg_try_advisory_lock(hashtextextended($1,104729))`, key).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return ErrLimitResumeBusy
		}
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var unlocked bool
		if e := c.QueryRow(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,104729))`, key).Scan(&unlocked); e != nil || !unlocked {
			_ = c.Conn().Close(unlockCtx)
		}
	}()
	return fn(c)
}

// HasLimitResume reports whether the session has a limit-resume row at all.
// Sessions without one skip per-turn tracking; BackfillLimitResumeTurn
// recovers the turn from the transcript when a refusal or an opt-in arrives.
func HasLimitResume(ctx context.Context, q Querier, id string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_limit_resume WHERE session_id=$1)`, id).Scan(&ok)
	return ok, err
}

// BackfillLimitResumeTurn tracks the session's latest turn (its last
// user_message with a turn ID, before beforeSeq when that is positive) when
// the row is missing or tracks no turn yet. TrackLimitResumeTurn's sequence and
// cancellation fences apply as if the turn had been tracked when it arrived.
func BackfillLimitResumeTurn(ctx context.Context, q Querier, id string, beforeSeq int64) error {
	var tracked bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_limit_resume WHERE session_id=$1 AND turn_id<>'')`, id).Scan(&tracked); err != nil || tracked {
		return err
	}
	var seq int64
	var turn, source string
	var at time.Time
	err := q.QueryRow(ctx, `SELECT seq,payload->>'turn_id',coalesce(payload->>'source',''),ts FROM agent_events
 WHERE session_id=$1 AND kind='user_message' AND coalesce(payload->>'turn_id','')<>'' AND ($2::bigint<=0 OR seq<$2)
 ORDER BY seq DESC LIMIT 1`, id, beforeSeq).Scan(&seq, &turn, &source, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return TrackLimitResumeTurn(ctx, q, id, turn, source, at, seq)
}

func SetLimitResume(ctx context.Context, q Querier, id string, enabled bool, sid string) error {
	if err := setLimitResume(ctx, q, id, enabled, sid); err != nil || !enabled {
		return err
	}
	return BackfillLimitResumeTurn(ctx, q, id, 0)
}

func setLimitResume(ctx context.Context, q Querier, id string, enabled bool, sid string) error {
	_, err := q.Exec(ctx, `INSERT INTO session_limit_resume(session_id,enabled,auth_session_id,state) VALUES($1,$2,$3,CASE WHEN $2 THEN 'armed' ELSE 'disabled' END)
 ON CONFLICT(session_id) DO UPDATE SET enabled=$2,auth_session_id=$3,
 state=CASE WHEN NOT $2 THEN 'disabled' WHEN session_limit_resume.reset_at IS NOT NULL AND session_limit_resume.expires_at>now() AND session_limit_resume.turn_id<>session_limit_resume.cancelled_turn_id AND session_limit_resume.attempts<3 AND session_limit_resume.state IN ('pending','disabled','unknown','armed') THEN 'pending' WHEN session_limit_resume.refused_at IS NOT NULL AND session_limit_resume.reset_at IS NULL AND session_limit_resume.expires_at>now() AND session_limit_resume.turn_id<>session_limit_resume.cancelled_turn_id AND session_limit_resume.attempts<3 AND session_limit_resume.state IN ('disabled','unknown','armed') THEN 'unknown' WHEN session_limit_resume.state IN ('unknown','dispatched','dispatching','blocked','expired','exhausted') THEN session_limit_resume.state ELSE 'armed' END,
 cancelled_turn_id=CASE WHEN $2 THEN session_limit_resume.cancelled_turn_id ELSE session_limit_resume.turn_id END,
 cancelled_at=CASE WHEN $2 THEN session_limit_resume.cancelled_at ELSE now() END`, id, enabled, sid)
	return err
}
func TrackLimitResumeTurn(ctx context.Context, q Querier, id, turnID, source string, at time.Time, seq int64) error {
	_, err := q.Exec(ctx, `INSERT INTO session_limit_resume(session_id,turn_id,event_seq) VALUES($1,$2,$3)
 ON CONFLICT(session_id) DO UPDATE SET turn_id=$2,event_seq=$3,
 state=CASE WHEN session_limit_resume.enabled THEN 'armed' ELSE 'disabled' END,
 reset_at=NULL,window_id='',refused_at=NULL,next_meter_poll_at=NULL,expires_at=CASE WHEN $4 IN ('limit_resume','system') THEN session_limit_resume.expires_at ELSE NULL END,reason='',attempts=CASE WHEN $4 IN ('limit_resume','system') THEN session_limit_resume.attempts ELSE 0 END,
 attempt_id=CASE WHEN $4 IN ('limit_resume','system') THEN session_limit_resume.attempt_id ELSE '' END
 WHERE session_limit_resume.event_seq<$3 AND session_limit_resume.turn_id<>$2 AND (session_limit_resume.cancelled_at IS NULL OR session_limit_resume.cancelled_at<$5)`, id, turnID, seq, source, at)
	return err
}
func RecordLimitRefusal(ctx context.Context, q Querier, id, provider, turnID string, reset *time.Time, seq int64, at time.Time, windowID ...string) error {
	window := ""
	if len(windowID) > 0 {
		window = windowID[0]
	}
	_, err := q.Exec(ctx, `UPDATE session_limit_resume SET window_id=$7,refused_at=$5,next_meter_poll_at=NULL,provider=$6,event_seq=$3,reset_at=$4,expires_at=coalesce(expires_at,$5::timestamptz+interval '8 days'),
 state=CASE WHEN NOT enabled THEN 'disabled' WHEN attempts>=3 THEN 'exhausted' WHEN $4::timestamptz IS NULL THEN 'unknown' WHEN coalesce(expires_at,$5::timestamptz+interval '8 days')<=now() THEN 'expired' ELSE 'pending' END,
 reason=CASE WHEN $4::timestamptz IS NULL THEN 'Reset time unavailable' ELSE '' END
 WHERE session_id=$1 AND turn_id=$2 AND turn_id<>last_attempt_turn_id AND turn_id<>cancelled_turn_id AND event_seq<$3 AND (cancelled_at IS NULL OR cancelled_at<$5)`, id, turnID, seq, reset, at, provider, window)
	return err
}
func CancelLimitResume(ctx context.Context, q Querier, id string) error {
	_, err := q.Exec(ctx, `INSERT INTO session_limit_resume(session_id,cancelled_at) VALUES($1,now()) ON CONFLICT(session_id) DO UPDATE SET enabled=false,state='disabled',cancelled_turn_id=session_limit_resume.turn_id,cancelled_at=now(),auth_session_id=''`, id)
	return err
}
func ClaimLimitResumeAttempt(ctx context.Context, q Querier, id, attemptID string) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE session_limit_resume SET state='dispatched',reason='Continuation sent; awaiting runtime acknowledgement. If interrupted, continue manually.',dispatched_at=now(),last_attempt_turn_id=turn_id,attempts=attempts+1,attempt_id=$2 WHERE session_id=$1 AND enabled AND state='pending' AND `+limitResumeDueSQL+`<=now() AND expires_at>now() AND attempts<3 AND turn_id<>cancelled_turn_id AND EXISTS (SELECT 1 FROM sessions s WHERE s.id=session_limit_resume.session_id AND s.kind='agent' AND s.status IN ('idle','disconnected') AND coalesce(s.paused_reason,'')='' AND s.background_tasks=0 AND s.ended_at IS NULL)`, id, attemptID)
	return tag.RowsAffected() == 1, err
}
func SetLimitResumeState(ctx context.Context, q Querier, id, state, reason string) error {
	_, err := q.Exec(ctx, `UPDATE session_limit_resume SET state=$2,reason=$3 WHERE session_id=$1`, id, state, reason)
	return err
}
func ListDueLimitResumes(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT session_id FROM session_limit_resume WHERE enabled AND (state='unknown' OR (state='pending' AND (`+limitResumeDueSQL+`<=now() OR expires_at<=now()))) ORDER BY reset_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// HasPendingLimitResume bounds retention to the original refusal's expiry.
func HasPendingLimitResume(ctx context.Context, q Querier, id string) (bool, error) {
	var pending bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_limit_resume WHERE session_id=$1 AND enabled AND (state IN ('pending','unknown') OR (state='dispatched' AND dispatched_at>now()-interval '2 minutes')) AND expires_at>now() AND attempts<=3 AND turn_id<>cancelled_turn_id)`, id).Scan(&pending)
	return pending, err
}

// CancelPendingLimitResume fences delayed events while retaining the session preference.
func CancelPendingLimitResume(ctx context.Context, q Querier, id string) error {
	_, err := q.Exec(ctx, `INSERT INTO session_limit_resume(session_id,cancelled_at) VALUES($1,now()) ON CONFLICT(session_id) DO UPDATE SET state=CASE WHEN session_limit_resume.enabled THEN 'armed' ELSE 'disabled' END,cancelled_turn_id=session_limit_resume.turn_id,cancelled_at=now(),attempts=0,reset_at=NULL,window_id='',refused_at=NULL,next_meter_poll_at=NULL,expires_at=NULL,attempt_id='',reason=''`, id)
	return err
}

// ResolveUnknownLimitReset keeps the original event/turn/expiry fences intact.
func ResolveUnknownLimitReset(ctx context.Context, q Querier, id string, reset time.Time) error {
	_, err := q.Exec(ctx, `UPDATE session_limit_resume SET reset_at=$2,state='pending',reason=''
 WHERE session_id=$1 AND enabled AND state='unknown' AND attempts<3
 AND expires_at>now() AND turn_id<>cancelled_turn_id`, id, reset)
	return err
}

// ClaimLimitResumeMeterPoll spaces on-demand requests across replicas and restarts.
// The scheduler holds the session lock and still checks newly arrived readings on
// every tick; this deadline only throttles the requests sent to the daemon.
func ClaimLimitResumeMeterPoll(ctx context.Context, q Querier, id string, delay time.Duration) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE session_limit_resume SET next_meter_poll_at=now()+$2*interval '1 second'
 WHERE session_id=$1 AND enabled AND state='unknown' AND (next_meter_poll_at IS NULL OR next_meter_poll_at<=now())`, id, delay.Seconds())
	return tag.RowsAffected() == 1, err
}
