package server

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"

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
	// Even a deep single selection needs the new consumer: legacy Repo is a
	// top-level folder, and must never silently address another checkout.
	s := req.Selection
	res, err := a.hub.requestWorkspaceDiscovery(ctx, owner, dc, protocol.DiscoverWorkspace{Operation: "resolve", Runtime: runtime, RootIdentity: s.RootIdentity, Members: s.Members, Folder: s.Folder, MembershipSnapshot: s.MembershipSnapshot})
	if err != nil || !res.OK || res.Resolved == nil {
		return nil, errDiscoveryUnavailable
	}
	v := res.Resolved
	if dc.WorkspaceCapabilities.ManifestVersion != 2 && (v.Folder != nil || len(v.Members) != 1 || v.Members[0].Path == "." || strings.Contains(v.Members[0].Path, "/")) {
		return nil, errors.New("peer does not support workspace v2")
	}

	m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: owner, Runtime: runtime, LaunchGeneration: newUUID(), Root: v.Root, Members: []protocol.WorkspaceMember{}}
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
	for _, source := range v.Members {
		memberPath := "."
		if v.Folder != nil {
			memberPath = strings.TrimPrefix(source.Path, v.Folder.Path+"/")
			if v.Folder.Path == "." {
				memberPath = source.Path
			}
			if source.Path == v.Folder.Path {
				memberPath = "."
			}
		} else if len(v.Members) > 1 {
			memberPath = path.Join("members", source.ID)
		}
		member := protocol.WorkspaceMember{ID: source.ID, Name: source.Name, Path: memberPath, SourceIdentity: source.SourceIdentity, CommonDirectoryIdentity: source.CommonDirectoryIdentity, Destination: source.SourceIdentity}
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
