package mcpconn

import (
	"context"
	"errors"
	"time"
)

// RenewSessionBinding extends only an already active, live lease. It never attaches.
// Core rechecks the human chain on every renewal, including idle Runner sessions.
func (s *Service) RenewSessionBinding(ctx context.Context, in SessionDispatchRequest) (err error) {
	defer func() { logSessionFailure("binding_renew", err) }()
	if !validSessionDispatch(in) {
		return ErrNotFound
	}
	tag, err := s.st.Pool().Exec(ctx, `WITH db_time AS MATERIALIZED (SELECT clock_timestamp() AS at) UPDATE mcp_connections c SET binding_lease_until=least(db_time.at::timestamptz+interval '1 minute',human_deadline),updated_at=db_time.at FROM db_time
 WHERE `+sessionWhere+` AND credential_generation=$5 AND bound_sid=$6 AND lifecycle='active' AND status='ok'
 AND human_deadline>db_time.at AND binding_lease_until>db_time.at AND (session_refresh_lease_until IS NULL OR session_refresh_lease_until>db_time.at)
 AND EXISTS(SELECT 1 FROM human_sessions h JOIN accounts a ON a.id=h.account_id
 WHERE h.chain_id=c.bound_sid AND h.account_id=c.account_id AND h.revoked_at IS NULL AND h.expires_at>db_time.at
 AND a.disabled_at IS NULL AND NOT a.must_change_password)`, in.ConnectionID, in.AccountID, in.RunnerInstallationID, in.RunnerSessionID, in.CredentialGeneration, in.HumanSessionID)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return err
}

// SweepSessionCredentials suspends lost leases/identity proofs without erasing.
// Only bounded retention or an explicit closing record invokes terminal cleanup.
// Work is bounded; several Core replicas may safely reconcile the same rows.
func (s *Service) SweepSessionCredentials(ctx context.Context) (err error) {
	defer func() { logSessionFailure("sweep", err) }()
	if err = s.suspendExpiredSessionBindings(ctx); err != nil {
		return err
	}
	return s.closeExpiredSessionCredentials(ctx)
}

// Suspension has no provider I/O and runs independently of slow cleanup.
func (s *Service) suspendExpiredSessionBindings(ctx context.Context) (err error) {
	defer func() { logSessionFailure("sweep_suspend", err) }()
	now := s.oauthTime()
	_, err = s.st.Pool().Exec(ctx, `UPDATE mcp_connections c SET lifecycle='suspended',credential_generation=credential_generation+1,
 suspended_at=$1,cleanup_after=$1::timestamptz+interval '168 hours',binding_lease_until=NULL,
 status=CASE WHEN session_refresh_lease_until IS NOT NULL THEN 'needs_auth' ELSE status END,
 session_refresh_lease_until=NULL,updated_at=$1
 WHERE runner_session_id IS NOT NULL AND lifecycle='active' AND
 (human_deadline<=$1 OR binding_lease_until<=$1 OR session_refresh_lease_until<=$1 OR NOT EXISTS(
 SELECT 1 FROM human_sessions h JOIN accounts a ON a.id=h.account_id WHERE h.chain_id=c.bound_sid
 AND h.account_id=c.account_id AND h.revoked_at IS NULL AND h.expires_at>$1
 AND a.disabled_at IS NULL AND NOT a.must_change_password))`, now)
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) closeExpiredSessionCredentials(ctx context.Context) (err error) {
	defer func() { logSessionFailure("sweep_close", err) }()
	now := s.oauthTime()
	rows, err := s.st.Pool().Query(ctx, `SELECT id::text,account_id::text,runner_installation_id,runner_session_id::text,credential_generation
 FROM mcp_connections WHERE runner_session_id IS NOT NULL AND lifecycle IN ('suspended','closing') AND cleanup_after<=$1
 ORDER BY cleanup_after,id LIMIT 2`, now)
	if err != nil {
		return err
	}
	type pending struct {
		scope      SessionScope
		id         string
		generation int64
	}
	var work []pending
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.id, &item.scope.AccountID, &item.scope.RunnerInstallationID, &item.scope.RunnerSessionID, &item.generation); err != nil {
			break
		}
		work = append(work, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range work {
		// A concurrent human reattachment changes generation and wins before closing.
		if closeErr := s.CloseSessionCredential(ctx, item.scope, item.id, item.generation); closeErr != nil && !errors.Is(closeErr, ErrNotFound) {
			err = errors.Join(err, closeErr)
		}
	}
	return err
}

// StartSessionSweepLoop isolates prompt suspension from provider revocation.
// Each cleanup pass handles at most two credentials; it cannot delay suspension.
func StartSessionSweepLoop(ctx context.Context, s *Service) {
	startSessionSweepWorker(ctx, s.suspendExpiredSessionBindings, 10*time.Second)
	startSessionSweepWorker(ctx, s.closeExpiredSessionCredentials, 30*time.Second)
}

func startSessionSweepWorker(ctx context.Context, sweep func(context.Context) error, timeout time.Duration) {
	go func() {
		run := func() {
			bounded, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			if err := sweep(bounded); err != nil {
				logSessionFailure("sweep_loop", err)
			}
		}
		run()
		ticker := time.NewTicker(15 * time.Second)
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
