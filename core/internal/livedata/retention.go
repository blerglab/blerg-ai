package livedata

import (
	"context"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

func (p Postgres) Maintain(ctx context.Context) error {
	if p.Pool == nil {
		return ErrUnavailable
	}
	for _, table := range []string{"live_data_approval_audit", "live_data_credential_audit"} {
		// Names are fixed above; no request string enters SQL.
		_, err := p.Pool.Exec(ctx, "DELETE FROM "+table+" WHERE id IN (SELECT id FROM "+table+" WHERE created_at<clock_timestamp()-interval '180 days' LIMIT 1000)")
		if err != nil {
			failurelog.Default.Event("live_data_retention", slog.LevelWarn, "component", "core", "cause", "retention_unavailable")
			return ErrUnavailable
		}
	}
	_, err := p.Pool.Exec(ctx, `UPDATE live_data_grants g SET revoked_at=coalesce(revoked_at,clock_timestamp()) FROM live_data_proposals p WHERE p.binding_id=g.binding_id AND p.revision=g.revision AND NOT g.runner_committed AND p.decision_deadline<=clock_timestamp() AND g.revoked_at IS NULL`)
	if err != nil {
		failurelog.Default.Event("live_data_retention", slog.LevelWarn, "component", "core", "cause", "retention_unavailable")
		return ErrUnavailable
	}
	return nil
}
func (p Postgres) RunMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = p.Maintain(work)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
