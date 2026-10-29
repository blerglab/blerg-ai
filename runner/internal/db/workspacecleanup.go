package db

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// RequestLocalWorkspaceStop revokes ordinary session admission immediately while
// preserving only exact-incarnation cleanup. It never claims physical removal.
func RequestLocalWorkspaceStop(ctx context.Context, pool *pgxpool.Pool, id string, end SessionEnd) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is harmless
	var completed bool
	err = tx.QueryRow(ctx, `SELECT final_digest IS NOT NULL FROM local_workspace_authority WHERE session_id=$1 AND NOT aborted FOR UPDATE`, id).Scan(&completed)
	if err != nil {
		return false, err
	}
	if !completed {
		if _, err = tx.Exec(ctx, `UPDATE local_workspace_authority SET stop_requested=true WHERE session_id=$1 AND NOT aborted`, id); err != nil {
			return false, err
		}
		now := time.Now()
		if err = endSessionStatus(ctx, tx, id, "stopped", &now, end); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `UPDATE board_tokens SET revoked_at=now() WHERE session_id=$1 AND revoked_at IS NULL`, id); err != nil {
			return false, err
		}
	}
	return completed, tx.Commit(ctx)
}

// Exact cleanup outcomes may advance after ordinary admission was revoked.
func SetWorkspaceCleanupResults(ctx context.Context, pool *pgxpool.Pool, id string, ref protocol.WorkspaceRuntimeRef, results []protocol.WorkspaceMemberResult) error {
	w, err := GetSessionWorkspace(ctx, pool, id)
	if err != nil {
		return err
	}
	if w == nil || w.Manifest == nil || w.Manifest.LaunchGeneration != ref.LaunchGeneration || ref.Generation <= 0 {
		return ErrWorkspaceRuntimeStale
	}
	if err = w.Manifest.ValidateResults(results); err != nil {
		return err
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `UPDATE session_workspaces w SET results=$4 FROM sessions s,local_workspace_authority a WHERE w.session_id=s.id AND a.session_id=s.id AND NOT a.aborted AND a.final_digest IS NULL AND w.session_id=$1 AND w.launch_generation=$2 AND w.runtime_generation=$3 AND a.runtime_generation=$3 AND w.runtime_binding IS NOT NULL`, id, ref.LaunchGeneration, ref.Generation, raw)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrWorkspaceRuntimeStale
	}
	return err
}

func BindWorkspaceCleanupRuntime(ctx context.Context, pool *pgxpool.Pool, id string, ref protocol.WorkspaceRuntimeRef, b protocol.WorkspaceRuntimeBinding) error {
	w, err := GetSessionWorkspace(ctx, pool, id)
	if err != nil {
		return err
	}
	if w == nil || w.Manifest == nil || b.WorkspaceRuntimeRef != ref || b.Validate(*w.Manifest) != nil {
		return ErrWorkspaceRuntimeStale
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `UPDATE session_workspaces w SET runtime_binding=$4 FROM local_workspace_authority a WHERE a.session_id=w.session_id AND NOT a.aborted AND a.final_digest IS NULL AND a.stop_requested AND w.session_id=$1 AND w.launch_generation=$2 AND w.runtime_generation=$3 AND a.runtime_generation=$3 AND (w.runtime_binding IS NULL OR w.runtime_binding=$4::jsonb)`, id, ref.LaunchGeneration, ref.Generation, raw)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrWorkspaceRuntimeStale
	}
	return err
}
