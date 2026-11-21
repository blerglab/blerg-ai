package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The read pump calls this after releasing the authority fence. It retries
// row contention and CAS races in order; the next heartbeat can rederive live
// status if bounded retries are exhausted, but not a one-shot error message.
func applyPinnedStateFrame(parent context.Context, h *Hub, pool *pgxpool.Pool, pin db.DaemonAuthority, msg protocol.SessionStateChanged) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, pinnedStateContextKey{}, &pin)
	var err error
retryLoop:
	for attempt := 0; attempt < 3; attempt++ {
		err = HandleSessionStateChanged(ctx, h, pool, msg)
		if err == nil {
			return
		}
		if !retryPinnedState(err) || attempt == 2 || ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 50 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			break retryLoop
		}
	}
	cause := failurelog.Cause(err)
	var pgerr *pgconn.PgError
	switch {
	case errors.As(err, &pgerr) && pgerr.Code == "55P03":
		cause = "lock_timeout"
	case errors.Is(err, errPinnedStateRetry):
		cause = "state_changed"
	case errors.Is(err, db.ErrDesktopAuthority):
		cause = "authority_changed"
	}
	attrs := []any{"component", "server", "operation", "daemon_status_frame", "stage", "deferred", "cause", cause}
	failurelog.Default.Event("daemon_status_frame:deferred", slog.LevelWarn, attrs...)
}

func retryPinnedState(err error) bool {
	if errors.Is(err, db.ErrDesktopAuthority) {
		return false
	}
	var pgerr *pgconn.PgError
	return errors.Is(err, errPinnedStateRetry) || (errors.As(err, &pgerr) && pgerr.Code == "55P03")
}
