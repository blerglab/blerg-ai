package server

import (
	"context"
	"net/http"
	"strconv"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Resolve from persisted launch state before admission/dispatch. An unqualified
// folder read must never silently select whichever repository happens to be cwd.
func (h *Hub) qualifyWorkspaceGit(ctx context.Context, sid string, ref protocol.WorkspaceGitRef) (protocol.WorkspaceGitRef, error) {
	if h.desktopPool == nil {
		if ref.IsZero() {
			return ref, nil
		}
		return ref, commitLookupFailed("git_database_unavailable", errCommitUnavailable)
	}
	ws, err := db.GetSessionReaderWorkspace(ctx, h.desktopPool, sid)
	if err != nil {
		return ref, commitLookupFailed("git_workspace_read_failed", err)
	}
	if ws == nil || ws.Manifest == nil {
		if ws != nil && ws.Kind == "repo" && ref.IsZero() {
			return ref, nil
		}
		return ref, commitLookupFailed("git_workspace_missing", errCommitUnavailable)
	}
	manifest, generation, err := loadExplorerBinding(ctx, h.desktopPool, sid)
	if err != nil {
		return ref, commitLookupFailed("git_binding_unavailable", err)
	}
	if ref.IsZero() && len(manifest.Members) == 1 {
		ref = protocol.WorkspaceGitRef{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}, MemberID: manifest.Members[0].ID}
	}
	if err := ref.Validate(manifest, generation); err != nil {
		return ref, commitLookupFailed("git_reference_stale", err)
	}
	dc := h.FindDaemonForSession(sid)
	if dc == nil {
		return ref, commitLookupFailed("git_daemon_missing", errCommitUnavailable)
	}
	authorized, ok := dc.authorizedWorkspaceRuntime(sid)
	if !ok {
		return ref, commitLookupFailed("git_runtime_not_authorized", errCommitUnavailable)
	}
	if authorized != ref.WorkspaceRuntimeRef {
		return ref, commitLookupFailed("git_runtime_changed", errCommitUnavailable)
	}
	if err := validateWorkspaceDesktop(ctx, h.desktopPool, dc, sid); err != nil {
		return ref, commitLookupFailed("git_desktop_authority_failed", err)
	}
	return ref, nil
}

func (h *Hub) currentWorkspaceGit(ctx context.Context, sid string, ref protocol.WorkspaceGitRef) bool {
	current, err := h.qualifyWorkspaceGit(ctx, sid, ref)
	return err == nil && current == ref
}

func workspaceGitQuery(r *http.Request) protocol.WorkspaceGitRef {
	generation, _ := strconv.ParseInt(r.URL.Query().Get("generation"), 10, 64)
	// Malformed supplied generations cannot collapse to legacy compatibility.
	if r.URL.Query().Get("generation") != "" && generation <= 0 {
		generation = -1
	}
	return protocol.WorkspaceGitRef{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: r.URL.Query().Get("launch_generation"), Generation: generation}, MemberID: r.URL.Query().Get("member_id")}
}
