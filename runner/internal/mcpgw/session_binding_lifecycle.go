package mcpgw

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type SessionLifecycleClient interface {
	SessionLifecycle(context.Context, SessionDispatchRequest, string) (int64, error)
}

// ReconcileSessionBindings delivers retained control records, never credentials.
// A generation conflict ends this delivery; it cannot target a newer attachment.
func ReconcileSessionBindings(ctx context.Context, pool *pgxpool.Pool, core SessionLifecycleClient, enabled bool) error {
	if pool == nil || core == nil {
		return sessionCoreFailure("lifecycle_configuration")
	}
	work, err := db.ListMCPBindingWork(ctx, pool)
	if err != nil {
		return sessionCoreFailure("lifecycle_database")
	}
	var failures error
	for _, item := range work {
		action := item.Action
		if action == "active" {
			live, liveErr := db.MCPScopedGrantLive(ctx, pool, item.Scope, item.ConnectionID)
			if liveErr != nil {
				failures = errors.Join(failures, sessionCoreFailure("lifecycle_live_check"))
				continue
			}
			if !enabled || !live {
				if err = db.SuspendMCPBindingWork(ctx, pool, item); err != nil {
					failures = errors.Join(failures, sessionCoreFailure("lifecycle_suspend_record"))
				}
				continue
			}
			action = "renew"
		}
		in := SessionDispatchRequest{MCPSessionScope: item.Scope, ConnectionID: item.ConnectionID}
		in.CredentialGeneration = item.ControlGeneration
		generation, deliveryErr := core.SessionLifecycle(ctx, in, action)
		// Terminal intent survives a lost suspension acknowledgment or an older
		// superseded delivery. Recover only Core's exact idempotent suspension
		// acknowledgment; a newer active attachment still refuses this old fence.
		if action == "close" && errors.Is(deliveryErr, ErrConnectionGone) {
			ack, ackErr := core.SessionLifecycle(ctx, in, "suspend")
			if ackErr == nil {
				if ack != in.CredentialGeneration && ack != in.CredentialGeneration+1 {
					ackErr = sessionCoreFailure("lifecycle_fence_response")
				} else {
					in.CredentialGeneration = ack
					generation, ackErr = core.SessionLifecycle(ctx, in, "close")
				}
			}
			deliveryErr = ackErr
		}
		err = deliveryErr
		superseded := errors.Is(err, ErrConnectionGone)
		if superseded {
			generation = item.ControlGeneration
			_ = sessionCoreFailure("lifecycle_generation_superseded")
		} else if err != nil {
			failures = errors.Join(failures, sessionCoreFailure("lifecycle_delivery"))
			continue
		}
		if err = db.CompleteMCPBindingWork(ctx, pool, item, superseded, generation); err != nil {
			failures = errors.Join(failures, sessionCoreFailure("lifecycle_ack"))
		}
	}
	return failures
}

func StartSessionBindingLoop(ctx context.Context, pool *pgxpool.Pool, core SessionLifecycleClient, enabled bool) {
	go func() {
		run := func() {
			bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := ReconcileSessionBindings(bounded, pool, core, enabled); err != nil {
				_ = sessionCoreFailure("lifecycle_reconcile")
			}
		}
		run()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}
