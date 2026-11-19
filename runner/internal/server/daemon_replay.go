package server

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"

	"github.com/jackc/pgx/v5/pgxpool"
)

const daemonReplayBatch = 8
const daemonReplayInterval = 100 * time.Millisecond

// Bound restart fanout across desktops as well as work per connection. A full
// scheduler leaves durable work for the next heartbeat, rather than queuing
// unbounded goroutines. Each socket has at most one replay in flight.
var daemonReplaySlots = make(chan struct{}, 4)

// Called only after the accepted frame/hello has released its authority fence.
// The writer still validates every outgoing frame under current authority.
func scheduleDaemonReplay(parent context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, ids []string) {
	if pool == nil {
		return
	}
	if !dc.replayRunning.CompareAndSwap(false, true) {
		logDaemonReplay("already_running", slog.LevelWarn)
		return
	}
	select {
	case daemonReplaySlots <- struct{}{}:
	default:
		dc.replayRunning.Store(false)
		logDaemonReplay("slots_full", slog.LevelWarn)
		return
	}
	ids = append([]string(nil), ids...)
	go func() {
		defer dc.replayRunning.Store(false)
		defer func() { <-daemonReplaySlots }()
		defer func() {
			if recover() != nil {
				logDaemonReplay("panic", slog.LevelError)
			}
		}()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
		defer cancel()
		batch, err := prioritizeDaemonReplay(ctx, pool, dc, ids)
		if err != nil {
			failurelog.Default.Event("daemon_replay:priority", slog.LevelWarn, "component", "server", "operation", "daemon_replay", "cause", failurelog.Cause(err))
			return
		}
		for _, id := range batch {
			if !waitDaemonReplay(ctx, dc.desktopDone) {
				return
			}
			if h.GetDaemon(dc.ID) != dc {
				return
			}
			dc.replayCursor.Add(1)
			healSessionTokens(ctx, h, pool, dc, []string{id})
			replayPendingSessionInputs(ctx, h, pool, dc, []string{id})
		}
		if ctx.Err() == nil {
			replaySessionPlugins(ctx, h, pool, dc)
		}
	}()
}

// Advance the cursor only for attempted sessions, so a slow first session
// cannot starve the rest when a batch exhausts its deadline.
func daemonReplaySessionIDs(ids []string, cursor uint64) []string {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	batch := make([]string, 0, len(ids))
	if len(ids) == 0 {
		return batch
	}
	start := cursor % uint64(len(ids))
	for i := 0; i < len(ids); i++ {
		batch = append(batch, ids[(start+uint64(i))%uint64(len(ids))])
	}
	return batch
}

func waitDaemonReplay(ctx context.Context, disconnected <-chan struct{}) bool {
	timer := time.NewTimer(daemonReplayInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-disconnected:
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

// Pending person input outranks token repair, then rotate the remaining
// inventory. Discovery is set-based so forty idle sessions do not delay the
// session whose live send failed. All processing remains under the work budget.
func prioritizeDaemonReplay(ctx context.Context, pool *pgxpool.Pool, dc *DaemonConn, ids []string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT s.id::text,
 EXISTS(SELECT 1 FROM session_inputs i WHERE i.session_id=s.id AND i.settled_at IS NULL),
 s.status IN ('starting','running','waiting','idle') AND NOT EXISTS(
 SELECT 1 FROM board_tokens t WHERE t.session_id=s.id AND t.revoked_at IS NULL
 AND t.expires_at>now() AND 'message'=ANY(t.capabilities))
 FROM sessions s WHERE s.daemon_id=$1 AND s.id=ANY($2::uuid[])`, dc.ID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	priorities := map[string]int{}
	for rows.Next() {
		var id string
		var pending, dead bool
		if err := rows.Scan(&id, &pending, &dead); err != nil {
			return nil, err
		}
		switch {
		case pending:
			priorities[id] = 0
		case dead:
			priorities[id] = 1
		default:
			priorities[id] = 2
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ordered := daemonReplaySessionIDs(ids, dc.replayCursor.Load())
	out := make([]string, 0, len(ordered))
	seen := map[string]bool{}
	for priority := 0; priority < 3; priority++ {
		for _, id := range ordered {
			if p, ok := priorities[id]; ok && p == priority && !seen[id] {
				out = append(out, id)
				seen[id] = true
			}
		}
	}
	return out, nil
}

func logDaemonReplay(cause string, level slog.Level) {
	failurelog.Default.Event("daemon_replay:"+cause, level, "component", "server", "operation", "daemon_replay", "cause", cause)
}
