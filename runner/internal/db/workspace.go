package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionWorkspace keeps the existing scalar session schema compatible. A nil
// manifest is a legacy repo session, not an invitation to infer extra members.
type SessionWorkspace struct {
	Kind     string
	Manifest *protocol.WorkspaceManifest
	Results  []protocol.WorkspaceMemberResult
}

// GetSessionWorkspace returns launch selection and settlement state. A legacy
// reader attachment must not turn a scalar-repo session into a P2 selection.
func GetSessionWorkspace(ctx context.Context, pool *pgxpool.Pool, id string) (*SessionWorkspace, error) {
	w, err := GetSessionReaderWorkspace(ctx, pool, id)
	if w != nil && w.Manifest != nil && strings.HasPrefix(w.Manifest.LaunchGeneration, "legacy-") {
		w.Manifest = nil
		w.Results = nil
	}
	return w, err
}

// GetSessionReaderWorkspace also includes post-upgrade reader/shell attachments.
// Only reader binding and owner-shell authorization should consume attachments.
func GetSessionReaderWorkspace(ctx context.Context, pool *pgxpool.Pool, id string) (*SessionWorkspace, error) {
	var w SessionWorkspace
	var manifest, results []byte
	err := pool.QueryRow(ctx, `SELECT s.workspace_kind,w.manifest,w.results FROM sessions s LEFT JOIN session_workspaces w ON w.session_id=s.id WHERE s.id=$1`, id).Scan(&w.Kind, &manifest, &results)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if manifest != nil {
		if err := json.Unmarshal(manifest, &w.Manifest); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(results, &w.Results); err != nil {
			return nil, err
		}
	}
	return &w, nil
}

// InsertSessionWorkspace is insert-only: recovery cannot replace a launch manifest.
func InsertSessionWorkspace(ctx context.Context, pool *pgxpool.Pool, id string, m protocol.WorkspaceManifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is harmless
	var owner, runtime string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(spawning_account_id::text,''),runtime FROM sessions WHERE id=$1 FOR UPDATE`, id).Scan(&owner, &runtime); err != nil {
		return err
	}
	if runtime == "daemon" {
		runtime = "host"
	}
	if runtime == "docker" {
		runtime = "sandbox"
	}
	if owner != m.OwnerID || runtime != m.Runtime {
		return errors.New("workspace session binding mismatch")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_workspaces(session_id,launch_generation,manifest) VALUES($1,$2,$3)`, id, m.LaunchGeneration, raw); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET workspace_kind=$2, repo=CASE WHEN $2='folder' THEN '' ELSE repo END, git_url=CASE WHEN $2='folder' THEN NULL ELSE git_url END WHERE id=$1`, id, m.Kind); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func SetWorkspaceResults(ctx context.Context, pool *pgxpool.Pool, id, generation string, results []protocol.WorkspaceMemberResult) error {
	w, err := GetSessionWorkspace(ctx, pool, id)
	if err != nil {
		return err
	}
	if w == nil || w.Manifest == nil || w.Manifest.LaunchGeneration != generation {
		return errors.New("stale workspace launch")
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
	tag, err := pool.Exec(ctx, `UPDATE session_workspaces SET results=$3 WHERE session_id=$1 AND launch_generation=$2 AND runtime_generation=0`, id, generation, raw)
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("stale workspace launch")
	}
	return err
}
