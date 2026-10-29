package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Canonicalize in parent order before hashing or approval. Friendly names are
// accepted only when unambiguous; projected paths and IDs also select members.
func selectClusterChild(cs *childStart, parent protocol.WorkspaceManifest) *APIError {
	fail := func() *APIError {
		return apiErrorf(http.StatusUnprocessableEntity, "choose parent workspace member IDs or unambiguous names/paths with repo(s) or all_repos")
	}
	if parent.Validate() != nil || parent.Runtime != "cluster" || cs.NoRepo || (cs.Runtime != "" && cs.Runtime != "cluster") {
		return fail()
	}
	ids := append([]string(nil), cs.Repos...)
	if cs.Repo != "" {
		if len(ids) > 0 {
			return fail()
		}
		ids = []string{cs.Repo}
	}
	if cs.AllRepos {
		if len(ids) > 0 {
			return fail()
		}
		for _, m := range parent.Members {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 || len(ids) > len(parent.Members) {
		return fail()
	}
	wanted := map[string]bool{}
	for _, selector := range ids {
		matches := workspaceSelectorMatches(parent.Members, selector)
		if len(matches) != 1 {
			return fail()
		}
		id := matches[0].ID
		if wanted[id] {
			return fail()
		}
		wanted[id] = true
	}
	selected := []string{}
	for _, m := range parent.Members {
		if wanted[m.ID] {
			selected = append(selected, m.ID)
			delete(wanted, m.ID)
		}
	}
	if len(wanted) > 0 {
		return fail()
	}
	cs.Repos, cs.Repo, cs.Provider, cs.AllRepos, cs.WorkspaceLaunch = selected, "", "", false, parent.LaunchGeneration
	return nil
}

func childSelectionLabel(cs childStart) string {
	if len(cs.Repos) > 0 {
		return strings.Join(cs.Repos, ", ")
	}
	return cs.Repo
}

// Re-read the immutable parent and private contexts at dispatch, including a
// delayed approval. No caller-supplied credential or shell grant is inherited.
func (a *API) clusterChildWorkspace(ctx context.Context, parent *db.SessionRow, req runnerStartRequest, childID string) (*protocol.WorkspaceManifest, []clusterworkspace.Member, *APIError) {
	fail := func() (*protocol.WorkspaceManifest, []clusterworkspace.Member, *APIError) {
		return nil, nil, apiErrorf(http.StatusConflict, "parent workspace selection is unavailable or changed")
	}
	w, err := db.GetSessionWorkspace(ctx, a.dbPool, parent.ID)
	if err != nil {
		return fail()
	}
	if w == nil || w.Manifest == nil {
		if len(req.WorkspaceRepos) > 0 || req.WorkspaceLaunch != "" {
			return fail()
		}
		return nil, nil, nil
	}
	if a.workspaceCluster == nil || !a.workspaceCluster.Capabilities().Supports("cluster", false) {
		return fail()
	}
	if req.WorkspaceLaunch != w.Manifest.LaunchGeneration || w.Manifest.OwnerID != derefOrEmpty(parent.SpawningAccountID) {
		return fail()
	}
	cs := childStart{Repos: req.WorkspaceRepos, Runtime: req.Runtime}
	if selectClusterChild(&cs, *w.Manifest) != nil {
		return fail()
	}
	child := protocol.WorkspaceManifest{Version: protocol.WorkspaceVersion, Kind: "folder", Layout: w.Manifest.Layout, OwnerID: w.Manifest.OwnerID, Runtime: "cluster", LaunchGeneration: newUUID()}
	for _, member := range w.Manifest.Members {
		if containsString(cs.Repos, member.ID) {
			child.Members = append(child.Members, member)
		}
	}
	if len(child.Members) == 1 {
		child.Kind = "repo"
	}
	raw, _ := json.Marshal(child)
	sum := sha256.Sum256(raw)
	child.Digest = hex.EncodeToString(sum[:])
	var contextsRaw []byte
	if err := a.dbPool.QueryRow(ctx, `SELECT contexts FROM cluster_workspace_storage WHERE session_id=$1 AND launch_generation=$2 AND owner_id=$3`, parent.ID, w.Manifest.LaunchGeneration, child.OwnerID).Scan(&contextsRaw); err != nil {
		return fail()
	}
	var contexts []clusterworkspace.Member
	if json.Unmarshal(contextsRaw, &contexts) != nil {
		return fail()
	}
	members, err := ClusterWorkspaceChild(*w.Manifest, child, contexts)
	if err != nil || childID == parent.ID {
		return fail()
	}
	return &child, members, nil
}
