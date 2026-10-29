package server

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type clusterStopWaiter struct {
	id              string
	ref             protocol.WorkspaceRuntimeRef
	done            chan struct{}
	minimumRevision int64
}

// Only a persisted, exact-incarnation final report acknowledges this request.
func (d *DaemonConn) acknowledgeClusterStop(ctx context.Context, pool *pgxpool.Pool, session, id string, ref protocol.WorkspaceRuntimeRef, revision int64, results []protocol.WorkspaceMemberResult) bool {
	d.clusterStopMu.Lock()
	defer d.clusterStopMu.Unlock()
	if w := d.clusterStop; w != nil && w.id == id && w.ref == ref && revision > w.minimumRevision {
		if persistClusterWorkspaceReport(ctx, pool, session, ref, revision, results, true) != nil {
			return false
		}
		select {
		case <-w.done:
		default:
			close(w.done)
		}
		return true
	}
	return false
}

func (a *API) settleClusterStop(ctx context.Context, id string) error {
	state, err := a.readClusterStopState(ctx, id)
	if err != nil {
		return err
	}
	ref := state.ref
	generation := ref.Generation
	if state.settled && !state.observed && state.allocated {
		return nil
	}
	if state.terminal || state.observed || !state.allocated {
		// An observation or end timestamp cannot authorize runtime frames. The
		// trusted coordinator fences actual writers instead of asking for a
		// report that those guards correctly reject.
		jm := a.hub.JobManager()
		if jm == nil {
			return errClusterWorkspace
		}
		if !state.allocated {
			claim, err := jm.historyClaim(ctx, "blerg-workspace-"+id)
			if err != nil || claim != nil {
				return errClusterWorkspace
			}
		}
		if err := jm.fenceNativeHistoryWriter(ctx, SessionJobSpec{SessionID: id, Resume: state.allocated}); err != nil {
			return err
		}
		// Existing terminal evidence is preserved exactly. An absent ledger is
		// safe only with generation zero, no binding, PVC, Job or pod. Neither
		// case invents a successful final settlement.
		if state.terminal || !state.allocated || state.settled {
			return nil
		}
	}
	// Revoke agent-side work authority first, retaining runtime final-report authority.
	if err := db.WithRuntimeLaunchFence(ctx, a.dbPool, id, func(fenced context.Context) error {
		_, err := a.dbPool.Exec(fenced, `UPDATE board_tokens SET revoked_at=now() WHERE session_id=$1 AND id NOT IN (SELECT runtime_token_id FROM session_runtime WHERE session_id=$1)`, id)
		return err
	}); err != nil {
		return err
	}
	dc := a.hub.FindDaemonForSession(id)
	if !state.observed && dc != nil && dc.RuntimeTokenID != "" && dc.WorkspaceRuntime == ref {
		var previous int64
		if err := a.dbPool.QueryRow(ctx, `SELECT CASE WHEN reported_generation=$2 THEN report_revision ELSE 0 END FROM cluster_workspace_storage WHERE session_id=$1`, id, generation).Scan(&previous); err != nil {
			return err
		}
		wait := &clusterStopWaiter{id: newUUID(), ref: ref, done: make(chan struct{}), minimumRevision: previous}
		dc.clusterStopMu.Lock()
		dc.clusterStop = wait
		dc.clusterStopMu.Unlock()
		defer func() {
			dc.clusterStopMu.Lock()
			if dc.clusterStop == wait {
				dc.clusterStop = nil
			}
			dc.clusterStopMu.Unlock()
		}()
		raw, _ := json.Marshal(struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
			RequestID string `json:"request_id"`
			protocol.WorkspaceRuntimeRef
		}{"cluster_workspace_stop", id, wait.id, ref})
		timeout := a.clusterStopTimeout
		if timeout <= 0 {
			timeout = 85 * time.Second
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case dc.send <- raw:
			select {
			case <-wait.done:
				return nil
			case <-timer.C:
			case <-ctx.Done():
			}
		default:
		}
	}
	// No acknowledgement is not an invitation to trust arbitrary late frames.
	// Seal conservative evidence while current authority is fenced, before end.
	return db.WithRuntimeLaunchFence(ctx, a.dbPool, id, func(fenced context.Context) error {
		current, err := db.GetSessionWorkspace(fenced, a.dbPool, id)
		if err != nil || current == nil || current.Manifest == nil || current.Manifest.LaunchGeneration != ref.LaunchGeneration {
			return errClusterWorkspace
		}
		row, err := db.GetSession(fenced, a.dbPool, id)
		if err != nil || row == nil || db.TerminallyEnded(row) || derefOrEmpty(row.SpawningAccountID) != current.Manifest.OwnerID {
			return errClusterWorkspace
		}
		results := make([]protocol.WorkspaceMemberResult, 0, len(current.Manifest.Members))
		for _, member := range current.Manifest.Members {
			v := protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: member.ID, LaunchGeneration: ref.LaunchGeneration}, Branch: "wip/" + id, RecoveryLocation: path.Join("projects", member.Path)}
			for _, old := range current.Results {
				if old.MemberID == member.ID {
					v = old
					break
				}
			}
			v.Pending = true
			v.SyncError = "final settlement unacknowledged; retained workspace requires recovery"
			if v.RecoveryLocation == "" {
				v.RecoveryLocation = path.Join("projects", member.Path)
			}
			results = append(results, v)
		}
		tx, err := a.dbPool.Begin(fenced)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(fenced) }()
		var sealed int64
		if err := tx.QueryRow(fenced, `SELECT settled_generation FROM cluster_workspace_storage WHERE session_id=$1 AND launch_generation=$2 FOR UPDATE`, id, ref.LaunchGeneration).Scan(&sealed); err != nil {
			return err
		}
		if sealed > 0 && sealed == ref.Generation {
			return nil
		}
		raw, _ := json.Marshal(results)
		// Trusted coordinator path also covers never-prepared roots. Runtime frames
		// retain all their live-token, prepared-root and terminal-write guards.
		tag, err := tx.Exec(fenced, `UPDATE session_workspaces w SET results=$4 FROM sessions s WHERE w.session_id=s.id AND w.session_id=$1 AND w.launch_generation=$2 AND w.runtime_generation=$3 AND s.ended_at IS NOT DISTINCT FROM $6::timestamptz AND s.status=$7 AND s.spawning_account_id=$5`, id, ref.LaunchGeneration, ref.Generation, raw, current.Manifest.OwnerID, row.EndedAt, row.Status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errClusterWorkspace
		}
		_, err = tx.Exec(fenced, `UPDATE board_tokens SET revoked_at=now() WHERE id IN (SELECT runtime_token_id FROM session_runtime WHERE session_id=$1)`, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(fenced, `UPDATE cluster_workspace_storage SET settled_generation=$2,reported_generation=$2,report_revision=CASE WHEN reported_generation=$2 THEN report_revision+1 ELSE 1 END WHERE session_id=$1 AND launch_generation=$3`, id, ref.Generation, ref.LaunchGeneration); err != nil {
			return err
		}
		return tx.Commit(fenced)
	})
}

// This is trusted lifecycle state, not a runtime-frame admission check. A
// missing reusable live seal is distinct from a failed read or a missing ledger
// for an already allocated generation. The caller holds the replacement lock.
type clusterStopState struct {
	ref                                    protocol.WorkspaceRuntimeRef
	allocated, settled, terminal, observed bool
}

func (a *API) readClusterStopState(ctx context.Context, id string) (state clusterStopState, err error) {
	err = db.WithRuntimeLaunchFence(ctx, a.dbPool, id, func(fenced context.Context) error {
		generation, binding, err := db.GetWorkspaceRuntime(fenced, a.dbPool, id)
		if err != nil {
			return err
		}
		w, err := db.GetSessionWorkspace(fenced, a.dbPool, id)
		if err != nil || w == nil || w.Manifest == nil {
			return errClusterWorkspace
		}
		row, err := db.GetSession(fenced, a.dbPool, id)
		if err != nil || row == nil || derefOrEmpty(row.SpawningAccountID) != w.Manifest.OwnerID {
			return errClusterWorkspace
		}
		state.ref = protocol.WorkspaceRuntimeRef{LaunchGeneration: w.Manifest.LaunchGeneration, Generation: generation}
		state.terminal = db.TerminallyEnded(row)
		state.observed = row.EndedAt != nil || terminalSessionStatus(row.Status)
		var launch, owner string
		var reported, sealed int64
		err = a.dbPool.QueryRow(fenced, `SELECT launch_generation,owner_id,reported_generation,settled_generation FROM cluster_workspace_storage WHERE session_id=$1`, id).Scan(&launch, &owner, &reported, &sealed)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if generation != 0 || binding != nil {
				return errClusterWorkspace
			}
		case err != nil:
			return err
		default:
			if launch != state.ref.LaunchGeneration || owner != w.Manifest.OwnerID || reported > generation || sealed > generation {
				return errClusterWorkspace
			}
			state.allocated = true
			state.settled = generation > 0 && sealed == generation && reported == generation
		}
		if state.observed || !state.allocated {
			// Revoke before physical teardown; terminal/recoverable runtime writes and
			// reconnects stay refused even if teardown fails and cleanup must retry.
			_, err = a.dbPool.Exec(fenced, `UPDATE board_tokens SET revoked_at=now() WHERE session_id=$1`, id)
			return err
		}
		return nil
	})
	return state, err
}

// A stopped workspace may use its existing runtime socket only for settlement.
// Revoked work authority cannot be healed or reopened by a new runtime hello.
func clusterWorkspaceWorkRevoked(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	var revoked bool
	err := pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM session_runtime r JOIN board_tokens t ON t.id=r.messaging_token_id WHERE r.session_id=$1 AND t.revoked_at IS NULL AND t.expires_at>now())`, id).Scan(&revoked)
	return revoked, err
}
