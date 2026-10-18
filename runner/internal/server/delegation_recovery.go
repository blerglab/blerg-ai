package server

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

func queueDelegationRecoveryReady(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, ids []string, required ...[]string) error {
	var stopped []string
	enqueue := func(refused []string) error {
		stopped = refused
		if !killStoppedSessions(dc, refused) {
			return errors.New("recovery stop inventory could not be delivered")
		}
		raw, err := json.Marshal(protocol.DaemonRecoveryReady{Type: "daemon_recovery_ready", SessionID: dc.BoundSessionID})
		if err != nil || !h.trySendToDaemon(dc, raw) {
			return errors.New("recovery readiness could not be delivered")
		}
		return nil
	}
	if pool == nil {
		if len(ids) != 0 {
			return errors.New("recovery policy is unavailable")
		}
		return enqueue(nil)
	}
	if err := db.WithDelegationRecoveryGuard(ctx, pool, ids, enqueue, required...); err != nil {
		return err
	}
	for _, id := range stopped {
		revokeSessionTokens(ctx, pool, id, "recovery refused")
	}
	return nil
}
