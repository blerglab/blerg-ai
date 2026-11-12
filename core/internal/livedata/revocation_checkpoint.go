package livedata

import (
	"context"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RevocationCheckpoint retries local revocation tickets outside Postgres. Human
// security changes never wait for this optional authority store. Pending tickets
// deny polling globally; the revoked grant itself is denied in the committing
// transaction. Startup invalidates existing approvals, including uncheckpointed restores.
type RevocationCheckpoint struct {
	Pool      *pgxpool.Pool
	Authority RevocationAuthority
	Epoch     int64
}

func (r RevocationCheckpoint) Start(ctx context.Context) error {
	external, e := r.Authority.RevocationMark(ctx)
	if e != nil || r.Epoch < 1 {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	// Never hold the singleton row while contacting the independent authority.
	var observed int64
	if e = r.Pool.QueryRow(ctx, `SELECT high_water FROM live_data_revocation_checkpoint WHERE singleton`).Scan(&observed); e != nil {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	if e = r.Authority.AdvanceRevocations(ctx, observed); e != nil {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	tx, e := r.Pool.Begin(ctx)
	if e != nil {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var epoch, mark int64
	if e = tx.QueryRow(ctx, `SELECT authority_epoch,high_water FROM live_data_revocation_checkpoint WHERE singleton FOR UPDATE`).Scan(&epoch, &mark); e != nil {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	if external > mark {
		var currentGrants bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM live_data_grants WHERE (scope->>'authority_epoch')::bigint >= $1)`, r.Epoch).Scan(&currentGrants); e != nil {
			return r.refuse("revocation_checkpoint_unavailable")
		}
		if epoch >= r.Epoch || currentGrants {
			return r.refuse("restore_detected")
		}
		// Only a newer fenced epoch can rebase a rolled-back snapshot; all of its
		// approvals belong to older epochs and are revoked before routes are served.
		mark = external
	}
	if epoch > r.Epoch {
		return r.refuse("authority_epoch_regressed")
	}
	if _, e = tx.Exec(ctx, `UPDATE live_data_revocation_checkpoint SET authority_epoch=$1,high_water=$2,guarded=true WHERE singleton`, r.Epoch, mark); e != nil {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	// Do not lower a sequence: concurrent processes and abandoned reservations
	// must never reuse a previously acknowledged ticket.
	if _, e = tx.Exec(ctx, `SELECT setval('live_data_revocation_requested',greatest($1,(SELECT last_value FROM live_data_revocation_requested),(SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM live_data_revocation_acknowledged)),$1>0 OR (SELECT is_called FROM live_data_revocation_requested) OR (SELECT is_called FROM live_data_revocation_acknowledged))`, mark); e != nil {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	if e = tx.Commit(ctx); e != nil {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	// Lost, uncheckpointed revocations cannot be distinguished from an ordinary
	// restart. Fail closed for both: approvals must be renewed after startup.
	// Do this without the singleton lock: grant triggers acquire it themselves.
	if _, e = r.Pool.Exec(ctx, `UPDATE live_data_grants SET revoked_at=clock_timestamp() WHERE revoked_at IS NULL`); e != nil {
		return r.refuse("startup_grant_invalidation_failed")
	}
	return nil
}
func (r RevocationCheckpoint) Sync(ctx context.Context) error {
	var requested, acknowledged int64
	if e := r.Pool.QueryRow(ctx, `SELECT high_water,(SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM live_data_revocation_acknowledged) FROM live_data_revocation_checkpoint WHERE singleton`).Scan(&requested, &acknowledged); e != nil {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	if requested <= acknowledged {
		return nil
	}
	if e := r.Authority.AdvanceRevocations(ctx, requested); e != nil {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	if requested > 0 {
		if _, e := r.Pool.Exec(ctx, `SELECT setval('live_data_revocation_acknowledged',greatest($1,(SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM live_data_revocation_acknowledged)),true)`, requested); e != nil {
			return r.refuse("revocation_checkpoint_unavailable")
		}
	}
	return nil
}
func (r RevocationCheckpoint) Check(ctx context.Context) error {
	external, e := r.Authority.RevocationMark(ctx)
	if e != nil {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	var mark, epoch, acknowledged int64
	if e = r.Pool.QueryRow(ctx, `SELECT high_water,authority_epoch,
 (SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM live_data_revocation_acknowledged)
 FROM live_data_revocation_checkpoint WHERE singleton`).Scan(&mark, &epoch, &acknowledged); e != nil || mark < external || epoch != r.Epoch || mark > acknowledged {
		return r.refuse("revocation_checkpoint_unavailable")
	}
	return nil
}
func (r RevocationCheckpoint) Run(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			work, cancel := context.WithTimeout(ctx, 2*time.Second)
			_ = r.Sync(work)
			cancel()
		}
	}
}
func (r RevocationCheckpoint) refuse(cause string) error {
	failurelog.Default.Event("live_data_authority", slog.LevelWarn, "cause", cause)
	return ErrUnavailable
}
