package server

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"github.com/jackc/pgx/v5/pgxpool"
)

type legacyWorkspaceRefusal string

func (e legacyWorkspaceRefusal) Error() string { return string(e) }

// errLegacyWorkspaceBusy is retryable: the desktop had no free attachment slot until the
// caller's deadline. It says nothing about the workspace and must never fence anything.
var errLegacyWorkspaceBusy = legacyWorkspaceRefusal("The desktop is busy attaching other session workspaces. Try again in a moment.")

// Attachment is idempotent. A desktop that answers "busy" is asked again after a short,
// growing pause, within the caller's own deadline.
const legacyAttachBusyBudget = 2 * time.Second

var legacyAttachBackoff = []time.Duration{20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}

// Legacy attachment adds readers and separately consented shells to an existing
// writer. It never replaces that writer or enters P2's completion transaction.
// Called under both the persisted desktop fence and the session launch fence.
func (h *Hub) attachLegacyWorkspace(ctx context.Context, pool *pgxpool.Pool, dc *DaemonConn, req protocol.LocalWorkspaceControl, reply *protocol.LocalWorkspaceControlResult) error {
	row, err := db.GetSession(ctx, pool, req.SessionID)
	if err != nil || row == nil || row.DaemonID != dc.ID || h.FindDaemonForSession(row.ID) != dc || row.SpawningAccountID == nil || !h.ownsDesktopID(ctx, *row.SpawningAccountID, dc.ID) || row.EndedAt != nil || row.ArchivedAt != nil || row.DiscardRequestedAt != nil || !isLiveStatus(row.Status) || row.Runtime == nil || req.Binding.Root.Inode == 0 {
		return errWorkspaceUnavailable
	}
	runtime := "host"
	if *row.Runtime == "docker" {
		runtime = "sandbox"
	} else if *row.Runtime != "daemon" {
		return errWorkspaceUnavailable
	}
	if !dc.WorkspaceCapabilities.Supports(runtime, false) {
		return errWorkspaceUnavailable
	}
	m := protocol.WorkspaceManifest{Version: protocol.WorkspaceVersion, Kind: "repo", OwnerID: *row.SpawningAccountID, Runtime: runtime, LaunchGeneration: "legacy-" + row.ID, Members: []protocol.WorkspaceMember{{ID: "repo", Name: "repository", Path: ".", SourceIdentity: "legacy-source", CommonDirectoryIdentity: "legacy-common", Destination: "legacy-source"}}}
	m.Digest = workspaceselection.Digest(m)
	ws, err := db.GetSessionReaderWorkspace(ctx, pool, row.ID)
	if err != nil || ws == nil {
		return errWorkspaceUnavailable
	}
	if ws.Manifest == nil {
		if err = db.InsertSessionWorkspace(ctx, pool, row.ID, m); err != nil {
			return err
		}
	} else if !reflect.DeepEqual(*ws.Manifest, m) {
		return errWorkspaceUnavailable
	}
	generation, binding, err := db.GetWorkspaceRuntime(ctx, pool, row.ID)
	if err != nil {
		return err
	}
	if generation == 0 {
		generation, err = db.BeginWorkspaceRuntime(ctx, pool, row.ID, m.LaunchGeneration, 0)
		if err != nil {
			return err
		}
	}
	expected := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: generation}, ManifestDigest: m.Digest, Root: req.Binding.Root}
	if binding != nil && *binding != expected {
		return errWorkspaceUnavailable
	}
	if err = db.BindWorkspaceRuntime(ctx, pool, row.ID, expected.WorkspaceRuntimeRef, expected); err != nil {
		return err
	}
	if !dc.AuthorizeWorkspaceRuntime(row.ID, expected.WorkspaceRuntimeRef) {
		return errWorkspaceUnavailable
	}
	reply.Manifest = &m
	reply.Binding = expected
	reply.ShellEligible = !row.RestrictTools && row.ParentSessionID == nil && row.CronID == nil && row.StartedByKind != "cron"
	reply.Launch = protocol.SupervisorLaunch{SessionID: row.ID, LaunchGeneration: m.LaunchGeneration, RuntimeGeneration: generation, Runtime: runtime, ConnectionID: dc.DesktopGeneration}
	return nil
}

// The browser can retry registration, but supplies no directory or authority.
// Both the daemon and the control RPC independently validate the live session.
func (a *API) ensureLegacyWorkspace(ctx context.Context, id string) error {
	ws, err := db.GetSessionReaderWorkspace(ctx, a.dbPool, id)
	if err != nil || ws == nil {
		return errWorkspaceUnavailable
	}
	if ws.Manifest != nil && !strings.HasPrefix(ws.Manifest.LaunchGeneration, "legacy-") {
		return nil
	}
	row, err := db.GetSession(ctx, a.dbPool, id)
	if err != nil || row == nil {
		return errWorkspaceUnavailable
	}
	if row.Runtime != nil && *row.Runtime == "cluster" {
		return legacyWorkspaceRefusal("This session started without a workspace. Start a new cluster session to browse its files.")
	}
	dc := a.hub.FindDaemonForSession(id)
	if dc == nil {
		return legacyWorkspaceRefusal("The session's daemon is disconnected. Reconnect it, then Refresh.")
	}
	if dc.DesktopBinding == nil && !dc.Compatibility {
		return legacyWorkspaceRefusal("The desktop's ownership could not be verified. Reconnect the desktop, then Refresh.")
	}
	if dc.WorkspaceCapabilities.ManifestVersion != protocol.WorkspaceVersion {
		return legacyWorkspaceRefusal("This daemon needs an update before it can browse session workspaces.")
	}
	// Busy retries never outlive the caller, and never exceed a short bound of their own.
	ctx, cancel := context.WithTimeout(ctx, legacyAttachBusyBudget)
	defer cancel()
	for attempt := 0; ; attempt++ {
		reply, err := a.hub.shellCall(ctx, dc, shellruntime.RPC{SessionID: id, Op: "attach_workspace"})
		if err == nil {
			return nil
		}
		if reply.ErrorCode != "busy" {
			return legacyAttachmentRefusal(reply.ErrorCode)
		}
		pause := legacyAttachBackoff[min(attempt, len(legacyAttachBackoff)-1)]
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < pause {
			return errLegacyWorkspaceBusy
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errLegacyWorkspaceBusy
		case <-timer.C:
		}
	}
}

func legacyAttachmentRefusal(code string) error {
	switch code {
	case "project_missing":
		return legacyWorkspaceRefusal("The session is still resuming or has no project directory. Wait for recovery, then Refresh.")
	case "project_changed":
		return legacyWorkspaceRefusal("The session's project directory was removed or replaced. Restore its original directory, then Refresh.")
	case "unsupported":
		return legacyWorkspaceRefusal("This daemon does not support browsing this session's workspace.")
	case "ownership_changed":
		return legacyWorkspaceRefusal("The session's workspace ownership or directory binding changed. Reconnect its desktop, then Refresh.")
	default:
		return legacyWorkspaceRefusal("The daemon could not attach the workspace reader and shell. Check that recovery has finished, then Refresh.")
	}
}
