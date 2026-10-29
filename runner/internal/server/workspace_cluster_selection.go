package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

// WorkspaceClusterConsumer is P1's handoff to P3. Preparing a runtime root,
// credentials, jobs, rollback and recovery remain P3's responsibility. P1
// never substitutes a desktop root or a guessed inode for an allocated root.
// Implementations must persist the supplied immutable manifest before dispatch.
type WorkspaceClusterConsumer interface {
	Capabilities() protocol.WorkspaceCapabilities
	Prepare(context.Context, protocol.WorkspaceManifest) (protocol.WorkspaceRoot, error)
	Start(context.Context, protocol.WorkspaceManifest, WorkspaceLaunchRequest) (string, error)
}
type WorkspaceLaunchRequest struct {
	Title, Prompt, Engine, Model, Effort, Interaction string
	selectionStartedAt                                time.Time
	principal                                         identity.Principal // verified requester; never decoded from the launch body
}

func (a *API) SetWorkspaceClusterConsumer(c WorkspaceClusterConsumer) { a.workspaceCluster = c }

func (a *API) authorizedHostedSelection(ctx context.Context, p identity.Principal, selection protocol.WorkspaceSelectionRequest) (protocol.WorkspaceManifest, error) {
	m := protocol.WorkspaceManifest{Version: 2, Kind: "repo", Layout: protocol.WorkspaceLayoutNames, OwnerID: p.Sub, Runtime: "cluster", LaunchGeneration: newUUID(), Members: []protocol.WorkspaceMember{}}
	if !selection.Layout.Valid() || selection.Layout == protocol.WorkspaceLayoutStructure || selection.DaemonID != "" || selection.Folder != nil || len(selection.Members) != 0 || len(selection.Hosted) < 1 || len(selection.Hosted) > 20 {
		return m, errDiscoveryUnavailable
	}
	var sources [][]RepoInfo
	if a.repos != nil {
		list, _ := a.repos.List(ctx)
		sources = append(sources, list)
	}
	if a.userRepos != nil {
		list, _ := a.userRepos.List(withCoreProof(ctx, proofFromPrincipal(p)), p.Sub)
		sources = append(sources, list)
	}
	allowed := map[string]RepoInfo{}
	for _, list := range sources {
		for _, repo := range list {
			if !repo.IsLocal && repo.Provider != "" && strings.Contains(repo.FullName, "/") {
				allowed[repoKey(repo.Provider, repo.FullName)] = repo
			}
		}
	}
	seen := map[string]bool{}
	for _, ref := range selection.Hosted {
		key := repoKey(ref.Provider, ref.FullName)
		repo, ok := allowed[key]
		if !ok || seen[key] {
			return m, errDiscoveryUnavailable
		}
		seen[key] = true
		sum := sha256.Sum256([]byte("hosted-v1:" + key))
		source := hex.EncodeToString(sum[:])
		id := source[:24]
		m.Members = append(m.Members, protocol.WorkspaceMember{ID: id, Name: repo.Name, Path: id, SourceIdentity: source, CommonDirectoryIdentity: source, Provider: repo.Provider, Project: repo.FullName, Destination: repo.FullName})
	}
	layoutSources := make([]protocol.DiscoverySource, len(m.Members))
	for i, member := range m.Members {
		layoutSources[i] = protocol.DiscoverySource{ID: member.ID, Name: member.Name}
	}
	paths, _ := workspaceselection.MemberPaths(protocol.DiscoveryResolved{Layout: protocol.WorkspaceLayoutNames, Members: layoutSources}, workspaceselection.MemberPathOptions{Cluster: true})
	for i := range m.Members {
		m.Members[i].Path = paths[i]
	}
	if len(m.Members) > 1 {
		m.Kind = "folder"
	}
	return m, nil
}
func (a *API) startSelectedCluster(w http.ResponseWriter, r *http.Request, p identity.Principal, req spawnSessionRequest) {
	selectionStartedAt := time.Now().UTC()
	if req.Selection == nil || req.NoRepo || req.NewRepo || req.Clone || req.Repo != "" || req.GitURL != "" || req.Provider != "" || len(req.MCP) > 0 || req.BoardID != "" || req.Kind != "agent" {
		writeError(w, 422, "invalid cluster workspace request")
		return
	}
	m, err := a.authorizedHostedSelection(r.Context(), p, *req.Selection)
	if err != nil {
		discoveryProblem(w)
		return
	}
	consumer := a.workspaceCluster
	if consumer == nil || !consumer.Capabilities().Supports("cluster", false) {
		writeError(w, 409, "The cluster runtime does not support workspace v2 yet.")
		return
	}
	if problem := a.modelEffortProblem(r.Context(), req.Engine, req.Model, req.Effort, ""); problem != "" {
		writeError(w, 422, problem)
		return
	}
	interaction, apiErr := resolveInteraction(req.Interaction, true, false)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	m.Root, err = consumer.Prepare(r.Context(), m)
	if err == nil {
		m.Digest = workspaceselection.Digest(m)
		err = m.Validate()
	}
	if err != nil {
		writeError(w, 409, "The cluster workspace could not be prepared.")
		return
	}
	id, err := consumer.Start(r.Context(), m, WorkspaceLaunchRequest{principal: p, selectionStartedAt: selectionStartedAt, Title: cleanSessionTitle(req.Title), Prompt: workspacePrompt(req.InitialPrompt, &m), Engine: req.Engine, Model: req.Model, Effort: req.Effort, Interaction: interaction})
	if err != nil || id == "" {
		writeError(w, 409, "The cluster workspace could not be started.")
		return
	}
	writeJSON(w, 202, map[string]string{"session_id": id})
}

// WorkspaceClusterChildConsumer prepares a child from an already authorized
// immutable subset. P3 must validate credentials, allocate/persist P0b runtime
// binding and clean up a failed preparation before returning. Spec has the
// normal child proof, messaging token, recovery and no-plugin policy.
type WorkspaceClusterChildConsumer interface {
	StartChild(context.Context, protocol.WorkspaceManifest, protocol.WorkspaceManifest, SessionJobSpec) error
}
