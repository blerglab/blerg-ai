package server

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/workspacepolicy"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Only P0b's authenticated post-creation binding grants file authority. The
// immutable manifest's legacy Root is deliberately never consulted here.
func loadExplorerManifest(ctx context.Context, pool *pgxpool.Pool, sid string) (protocol.WorkspaceManifest, error) {
	w, err := db.GetSessionWorkspace(ctx, pool, sid)
	if err != nil || w == nil || w.Manifest == nil {
		return protocol.WorkspaceManifest{}, errWorkspaceUnavailable
	}
	for _, member := range w.Manifest.Members {
		if !workspacepolicy.Directory(member.Path) || !workspacepolicy.Directory(member.Name) {
			return protocol.WorkspaceManifest{}, errWorkspaceUnavailable
		}
	}
	row, err := db.GetSession(ctx, pool, sid)
	if err != nil || row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID != w.Manifest.OwnerID || row.Runtime == nil || row.EndedAt != nil || row.ArchivedAt != nil || row.DiscardRequestedAt != nil {
		return protocol.WorkspaceManifest{}, errWorkspaceUnavailable
	}
	runtime := *row.Runtime
	switch runtime {
	case "daemon":
		runtime = "host"
	case "docker":
		runtime = "sandbox"
	}
	if runtime != w.Manifest.Runtime {
		return protocol.WorkspaceManifest{}, errWorkspaceUnavailable
	}
	return *w.Manifest, nil
}
func loadExplorerBinding(ctx context.Context, pool *pgxpool.Pool, sid string) (protocol.WorkspaceManifest, int64, error) {
	manifest, err := loadExplorerManifest(ctx, pool, sid)
	if err != nil {
		return protocol.WorkspaceManifest{}, 0, err
	}
	generation, binding, err := db.GetWorkspaceRuntime(ctx, pool, sid)
	if err != nil || binding == nil || generation != binding.Generation || binding.Validate(manifest) != nil {
		return protocol.WorkspaceManifest{}, 0, errWorkspaceUnavailable
	}
	return manifest, generation, nil
}

// AuthorizeWorkspaceRuntime is called by trusted launch/recovery code AFTER
// BeginWorkspaceRuntime and writer fencing, BEFORE dispatch. Reconnect requires
// a fresh authorization from persisted lifecycle state, never a reported frame.
// P1/P2/P3 supply this hook; receiving a root report does not call it.
func (dc *DaemonConn) AuthorizeWorkspaceRuntime(sid string, ref protocol.WorkspaceRuntimeRef) bool {
	if sid == "" || ref.Generation <= 0 || ref.LaunchGeneration == "" || (dc.BoundSessionID != "" && dc.BoundSessionID != sid) {
		return false
	}
	dc.liveMu.Lock()
	defer dc.liveMu.Unlock()
	if dc.workspaceRuntimeRefs == nil {
		dc.workspaceRuntimeRefs = map[string]protocol.WorkspaceRuntimeRef{}
	}
	if old, ok := dc.workspaceRuntimeRefs[sid]; ok && (old.LaunchGeneration != ref.LaunchGeneration || old.Generation > ref.Generation) {
		return false
	}
	dc.workspaceRuntimeRefs[sid] = ref
	return true
}
func (dc *DaemonConn) authorizedWorkspaceRuntime(sid string) (protocol.WorkspaceRuntimeRef, bool) {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	ref, ok := dc.workspaceRuntimeRefs[sid]
	return ref, ok
}

// Caller holds the authenticated connection fence (daemonReadPump). Never
// reacquire it here: advisory locks on separate connections are not reentrant.
func (h *Hub) bindPreparedWorkspaceRuntime(ctx context.Context, pool *pgxpool.Pool, dc *DaemonConn, msg protocol.WorkspaceRuntimePrepared) {
	if pool == nil || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(msg.SessionID) != dc || (dc.BoundSessionID != "" && dc.BoundSessionID != msg.SessionID) {
		return
	}
	authorized, ok := dc.authorizedWorkspaceRuntime(msg.SessionID)
	if !ok || authorized != msg.Binding.WorkspaceRuntimeRef {
		return
	}
	bind := func(ctx context.Context) error {
		if err := validateWorkspaceDesktop(ctx, pool, dc, msg.SessionID); err != nil {
			return err
		}
		if _, err := loadExplorerManifest(ctx, pool, msg.SessionID); err != nil {
			return err
		}
		current, ok := dc.authorizedWorkspaceRuntime(msg.SessionID)
		if !ok || current != authorized || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(msg.SessionID) != dc {
			return errWorkspaceUnavailable
		}
		if dc.RuntimeTokenID != "" {
			if err := db.ValidateRuntimeBinding(ctx, pool, msg.SessionID, dc.RuntimeTokenID, false); err != nil {
				return err
			}
		}
		return db.BindWorkspaceRuntime(ctx, pool, msg.SessionID, authorized, msg.Binding)
	}
	_ = bind(ctx)
}

// validateWorkspaceDesktop is also used after waiting for runtime replies: an
// unchanged hub pointer cannot preserve authority across a persisted reassignment.
func validateWorkspaceDesktop(ctx context.Context, pool *pgxpool.Pool, dc *DaemonConn, sid string) error {
	if dc.DesktopBinding != nil {
		b := dc.DesktopBinding
		if !b.ExpiresAt.After(time.Now()) || !db.DesktopOwnsSession(ctx, pool, dc.ID, b.OwnerID, sid) {
			return errWorkspaceUnavailable
		}
		return db.ValidateDesktopConnection(ctx, pool, *b, dc.DesktopGeneration)
	}
	if dc.LegacyDesktop {
		return db.ValidateLegacyDesktop(ctx, pool, dc.ID, dc.DesktopGeneration, dc.LegacyRevision)
	}
	return nil // Session-scoped pods use the runtime launch fence.
}

func withWorkspaceDesktopFence(ctx context.Context, pool *pgxpool.Pool, dc *DaemonConn, sid string, fn func(context.Context) error) error {
	check := func(fenced context.Context) error {
		if err := validateWorkspaceDesktop(fenced, pool, dc, sid); err != nil {
			return err
		}
		return fn(fenced)
	}
	if dc.DesktopBinding != nil || dc.LegacyDesktop {
		return db.WithDesktopFence(ctx, pool, dc.ID, check)
	}
	return check(ctx)
}
