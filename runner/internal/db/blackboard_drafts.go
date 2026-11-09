package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxBlackboardDraftBytes bounds a draft's stored JSON (see migration 142's CHECK, which is the
// source of truth enforced by the database; this constant lets the server reject an oversized
// draft before it ever reaches that CHECK, with a clearer error than a constraint violation).
const MaxBlackboardDraftBytes = 16 << 10

// ErrBlackboardDraftTooLarge is returned by SaveBlackboardDraft for a draft over MaxBlackboardDraftBytes.
var ErrBlackboardDraftTooLarge = errors.New("blackboard draft too large")

// BlackboardDraft is one workspace's pending proposed setup, keyed by workspace (session) ID.
// Every string field inside Draft may contain text authored by a worker, the board or a commit
// log; callers must keep it delimited as untrusted wherever it is rendered back (see
// blackboardDraftUntrusted in the server package, matching the convention in blackboard_agent.go).
type BlackboardDraft struct {
	ID          string          `json:"id"`
	Revision    int             `json:"revision"`
	WorkspaceID string          `json:"workspace_id"`
	Draft       json.RawMessage `json:"draft"`
	CreatedAt   string          `json:"created_at"`
	CreatedBy   string          `json:"created_by"`
}

// SaveBlackboardDraft replaces any existing draft for the workspace wholesale: a draft is
// regenerated, never patched in place.
func SaveBlackboardDraft(ctx context.Context, pool *pgxpool.Pool, workspaceID, createdBy string, draft json.RawMessage) error {
	_, err := SaveBlackboardDraftRevision(ctx, pool, workspaceID, createdBy, draft)
	return err
}

// SaveBlackboardDraftRevision returns the exact snapshot saved by this call.
func SaveBlackboardDraftRevision(ctx context.Context, pool *pgxpool.Pool, workspaceID, createdBy string, draft json.RawMessage) (*BlackboardDraft, error) {
	if len(draft) > MaxBlackboardDraftBytes {
		return nil, ErrBlackboardDraftTooLarge
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = LockDraftWorkspace(ctx, tx, workspaceID); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO blackboard_drafts(workspace_id,draft,created_by) VALUES($1,$2,$3)
 ON CONFLICT(workspace_id) DO UPDATE SET draft=$2, created_by=$3, created_at=now(), revision=blackboard_drafts.revision+1`, workspaceID, draft, createdBy)
	if err != nil {
		return nil, err
	}
	result, err := GetBlackboardDraft(ctx, tx, workspaceID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// GetBlackboardDraft returns nil, nil when the workspace has no pending draft.
func GetBlackboardDraft(ctx context.Context, pool DBTX, workspaceID string) (*BlackboardDraft, error) {
	var d BlackboardDraft
	var createdAt time.Time
	err := pool.QueryRow(ctx, `SELECT id::text,revision,workspace_id,draft,created_at,created_by FROM blackboard_drafts WHERE workspace_id=$1`, workspaceID).
		Scan(&d.ID, &d.Revision, &d.WorkspaceID, &d.Draft, &createdAt, &d.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return &d, nil
}

// DeleteBlackboardDraft is idempotent: discarding an already-discarded (or never-created) draft
// is not an error.
func DeleteBlackboardDraft(ctx context.Context, pool *pgxpool.Pool, workspaceID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = LockDraftWorkspace(ctx, tx, workspaceID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM blackboard_drafts WHERE workspace_id=$1`, workspaceID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LockDraftWorkspace serializes save, ask creation, and answer across server processes.
func LockDraftWorkspace(ctx context.Context, tx pgx.Tx, workspace string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('bound-draft:' || $1,0))`, workspace)
	return err
}
