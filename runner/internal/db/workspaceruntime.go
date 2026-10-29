package db

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrWorkspaceRuntimeStale = errors.New("stale workspace runtime")

// BeginWorkspaceRuntime is a trusted server-only compare-and-set transition.
// Callers must authorize owner/runtime, serialize against lifecycle operations,
// and fence/terminate the previous physical writer before dispatching a new one.
// Never call this from an untrusted runtime-prepared frame. The returned
// generation must be bound to the authenticated runtime connection. Results and
// membership survive replacement; pending roots are unavailable to explorers.
func BeginWorkspaceRuntime(ctx context.Context, pool *pgxpool.Pool, id, launch string, expected int64) (int64, error) {
	if expected < 0 {
		return 0, ErrWorkspaceRuntimeStale
	}
	var generation int64
	err := pool.QueryRow(ctx, `UPDATE session_workspaces w SET runtime_generation=runtime_generation+1,runtime_binding=NULL FROM sessions s WHERE w.session_id=s.id AND w.session_id=$1 AND w.launch_generation=$2 AND w.runtime_generation=$3 AND w.runtime_generation<9223372036854775807 AND s.ended_at IS NULL AND s.status NOT IN ('stopped','ended') RETURNING w.runtime_generation`, id, launch, expected).Scan(&generation)
	if err != nil {
		return 0, ErrWorkspaceRuntimeStale
	}
	return generation, nil
}

// GetWorkspaceRuntime returns the current generation and nil while preparation
// is pending (or for a legacy session). Callers must still apply viewer authz.
func GetWorkspaceRuntime(ctx context.Context, pool *pgxpool.Pool, id string) (int64, *protocol.WorkspaceRuntimeBinding, error) {
	var generation int64
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT runtime_generation,runtime_binding FROM session_workspaces WHERE session_id=$1`, id).Scan(&generation, &raw)
	if err != nil {
		return 0, nil, err
	}
	if raw == nil {
		return generation, nil, nil
	}
	var b protocol.WorkspaceRuntimeBinding
	if err := json.Unmarshal(raw, &b); err != nil {
		return 0, nil, err
	}
	return generation, &b, nil
}

// BindWorkspaceRuntime accepts only the generation authorized by the server for
// the authenticated runtime, never a generation taken on trust from the frame.
// The physical binding is write-once per generation; exact retries are allowed.
func BindWorkspaceRuntime(ctx context.Context, pool *pgxpool.Pool, id string, authorized protocol.WorkspaceRuntimeRef, b protocol.WorkspaceRuntimeBinding) error {
	if authorized != b.WorkspaceRuntimeRef {
		return ErrWorkspaceRuntimeStale
	}
	w, err := GetSessionReaderWorkspace(ctx, pool, id)
	if err != nil {
		return err
	}
	if w == nil || w.Manifest == nil {
		return ErrWorkspaceRuntimeStale
	}
	if err := b.Validate(*w.Manifest); err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `UPDATE session_workspaces w SET runtime_binding=$4 FROM sessions s WHERE w.session_id=s.id AND w.session_id=$1 AND w.launch_generation=$2 AND w.runtime_generation=$3 AND (w.runtime_binding IS NULL OR w.runtime_binding=$4::jsonb) AND s.ended_at IS NULL AND s.status NOT IN ('stopped','ended')`, id, authorized.LaunchGeneration, authorized.Generation, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrWorkspaceRuntimeStale
	}
	return nil
}

// SetWorkspaceRuntimeResults fences reports against both selection and runtime
// generations. The caller merges member outcomes with previous results, keeping
// successful outcomes when other members need retry. Authentication is required
// before this API just as for BindWorkspaceRuntime.
func SetWorkspaceRuntimeResults(ctx context.Context, pool *pgxpool.Pool, id string, ref protocol.WorkspaceRuntimeRef, results []protocol.WorkspaceMemberResult) error {
	w, err := GetSessionWorkspace(ctx, pool, id)
	if err != nil {
		return err
	}
	if w == nil || w.Manifest == nil || w.Manifest.LaunchGeneration != ref.LaunchGeneration || ref.Generation <= 0 {
		return ErrWorkspaceRuntimeStale
	}
	if err := w.Manifest.ValidateResults(results); err != nil {
		return err
	}
	if results == nil {
		results = []protocol.WorkspaceMemberResult{}
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `UPDATE session_workspaces w SET results=$4 FROM sessions s WHERE w.session_id=s.id AND w.session_id=$1 AND w.launch_generation=$2 AND w.runtime_generation=$3 AND w.runtime_binding IS NOT NULL AND s.ended_at IS NULL AND s.status NOT IN ('stopped','ended')`, id, ref.LaunchGeneration, ref.Generation, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrWorkspaceRuntimeStale
	}
	return nil
}
