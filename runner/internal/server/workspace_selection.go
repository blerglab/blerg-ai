package server

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

// The browser supplies references, never a manifest. Resolve and bind freshly
// against the same authenticated desktop under its ownership fence.
func (a *API) prepareWorkspaceSelection(ctx context.Context, owner string, dc *DaemonConn, req spawnSessionRequest) (*protocol.WorkspaceManifest, error) {
	if req.Selection == nil || req.NoRepo || req.NewRepo || req.Clone || req.GitURL != "" || req.Repo != "" || req.Provider != "" || len(req.Selection.Hosted) > 0 {
		return nil, errDiscoveryUnavailable
	}
	runtime := "host"
	if req.Runtime == "docker" {
		runtime = "sandbox"
	} else if req.Runtime != "" && req.Runtime != "daemon" {
		return nil, errDiscoveryUnavailable
	}
	if dc == nil || !dc.WorkspaceCapabilities.Supports(runtime, false) {
		return nil, errors.New("peer does not support this workspace runtime")
	}
	// Every explicit selection uses the negotiated runtime, including a single member.
	s := req.Selection
	res, err := a.hub.requestWorkspaceDiscovery(ctx, owner, dc, protocol.DiscoverWorkspace{Operation: "resolve", Layout: s.Layout, Runtime: runtime, RootIdentity: s.RootIdentity, Members: s.Members, Folder: s.Folder, MembershipSnapshot: s.MembershipSnapshot})
	if err != nil || !res.OK || res.Resolved == nil || res.Resolved.Layout != s.Layout {
		return nil, errDiscoveryUnavailable
	}
	v := res.Resolved
	if req.InitializeGit {
		if runtime != "host" {
			return nil, errors.New("initializing Git is only available for host folders")
		}
		if len(v.Members) == 0 && v.Folder != nil {
			res, err = a.hub.requestWorkspaceDiscovery(ctx, owner, dc, protocol.DiscoverWorkspace{Operation: "initialize", Runtime: "host", RootIdentity: v.RootIdentity, Folder: v.Folder})
			if err != nil || !res.OK || res.Resolved == nil || len(res.Resolved.Members) != 1 {
				return nil, errors.New("could not initialize Git; choose a folder outside an existing worktree")
			}
			v = res.Resolved
		}
	}

	m := protocol.WorkspaceManifest{Version: 2, Layout: v.Layout, Kind: "repo", OwnerID: owner, Runtime: runtime, LaunchGeneration: newUUID(), Root: v.Root, Members: []protocol.WorkspaceMember{}}
	if v.Folder != nil || len(v.Members) != 1 {
		m.Kind = "folder"
	}
	authorized := map[string]bool{}
	if a.repos != nil {
		list, _ := a.repos.List(ctx)
		for _, repo := range list {
			if !repo.IsLocal {
				authorized[repoKey(repo.Provider, repo.FullName)] = true
			}
		}
	}
	if a.userRepos != nil && coreProofFrom(ctx).valid() {
		list, _ := a.userRepos.List(ctx, owner)
		for _, repo := range list {
			if !repo.IsLocal {
				authorized[repoKey(repo.Provider, repo.FullName)] = true
			}
		}
	}
	paths, _ := workspaceselection.MemberPaths(*v)
	for i, source := range v.Members {
		member := protocol.WorkspaceMember{ID: source.ID, Name: source.Name, Path: paths[i], SourceIdentity: source.SourceIdentity, CommonDirectoryIdentity: source.CommonDirectoryIdentity, Destination: source.SourceIdentity}
		if source.Origin != nil && authorized[repoKey(source.Origin.Provider, source.Origin.FullName)] {
			member.Provider = source.Origin.Provider
			member.Project = source.Origin.FullName
		}
		m.Members = append(m.Members, member)
	}
	// Local presence and matching names are not project authority. Only an
	// exact provider/origin in a separately authorized hosted source is mapped.
	m.Digest = workspaceselection.Digest(m)
	if m.Validate() != nil {
		return nil, errDiscoveryUnavailable
	}
	bound, err := a.hub.requestWorkspaceDiscovery(ctx, owner, dc, protocol.DiscoverWorkspace{Operation: "bind", SelectionIdentity: v.SelectionIdentity, Manifest: &m})
	if err != nil || !bound.OK {
		return nil, errDiscoveryUnavailable
	}
	return &m, nil
}

func (h *Hub) releaseWorkspaceSelection(dc *DaemonConn, m *protocol.WorkspaceManifest) {
	if dc == nil || m == nil {
		return
	}
	raw, _ := json.Marshal(protocol.DiscoverWorkspace{Type: "discover_workspace", RequestID: newUUID(), Operation: "release", Manifest: m})
	h.trySendToDaemon(dc, raw)
}
