package server

import (
	"context"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

const desktopEnrollmentGuidance = "Open Settings → Account → Your desktops to enroll it, or Add a desktop and run its install command."

// Only a verified shared bearer can identify an unassigned desktop by name.
// A forged hello must never overwrite another machine's connection status.
func (h *Hub) recordLegacyRefusal(ctx context.Context, pool *pgxpool.Pool, name string) {
	d, err := db.FindUnownedDesktopByName(ctx, pool, name)
	if err != nil || d == nil {
		return
	}
	_ = db.WithDesktopFence(ctx, pool, d.ID, func(ctx context.Context) error {
		if h.GetDaemon(d.ID) != nil {
			return nil
		}
		_, err := pool.Exec(ctx, `UPDATE daemons SET status='refused',status_reason=$2,last_seen_at=now() WHERE id=$1 AND desktop_owner_id IS NULL AND connection_generation IS NOT DISTINCT FROM NULLIF($3,'')::uuid`, d.ID, desktopEnrollmentGuidance, d.Generation)
		return err
	})
}

// Called under the hello's desktop fence, using only the core-verified ID and
// the persisted generation observed before attach. A claimed hello ID cannot
// select this row, and a live connection or intervening binding wins.
func (h *Hub) recordPersonalRefusal(ctx context.Context, pool *pgxpool.Pool, observed db.DesktopRow) {
	if h.GetDaemon(observed.ID) != nil {
		return
	}
	_, _ = pool.Exec(ctx, `UPDATE daemons SET status='refused',status_reason=$2,last_seen_at=now()
 WHERE id=$1 AND connection_generation IS NOT DISTINCT FROM NULLIF($3,'')::uuid
 AND ownership_revision=$4 AND COALESCE(desktop_owner_id,'')=$5`, observed.ID, desktopEnrollmentGuidance, observed.Generation, observed.Revision, observed.OwnerID)
}

func (h *Hub) recordDaemonClosed(parent context.Context, dc *DaemonConn, pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	defer cancel()
	_ = db.WithDesktopFence(ctx, pool, dc.ID, func(ctx context.Context) error {
		if h.GetDaemon(dc.ID) != nil {
			return nil
		}
		_, err := pool.Exec(ctx, `UPDATE daemons SET status='disconnected',status_reason='Desktop disconnected; check the daemon service and reconnect.',last_seen_at=now() WHERE id=$1 AND status<>'refused' AND connection_generation IS NOT DISTINCT FROM NULLIF($2,'')::uuid`, dc.ID, dc.DesktopGeneration)
		return err
	})
}
