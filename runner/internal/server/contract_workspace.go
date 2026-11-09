package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

// A contract start (POST /api/runner/start: a board, a cron, an API caller) names its repository the
// way it knows it: a folder name or owner/name. A desktop with workspace discovery (0.5) launches
// only from a fresh, bound selection of source references, which the launch sheet gets from
// discovery. The runner resolves the named repository into that selection itself, against the
// same desktop and runtime the start already chose, exactly as the launch sheet would: list the
// desktop's repositories, pick the one the start names, resolve and bind it.

// maxContractDiscoveryPages bounds the listing a contract start reads to find its repository.
const maxContractDiscoveryPages = 20

// contractReposWorkspace resolves every target on dc into one bound workspace for runtime
// ("docker" or "daemon"), from fresh discovery only (never the desktop's cached inventory): a folder
// name matches the folder; owner/name matches a repository whose fresh origin is exactly that
// (or, as before, a folder of that name with no origin at all), and a folder whose origin is
// another repository never matches. It needs the complete listing: a truncated one, or one longer
// than the page budget, is refused rather than taken as proof that a match is unique. Its refusals
// say what to do.
func (a *API) contractReposWorkspace(ctx context.Context, owner string, dc *DaemonConn, repos []ContractRepository, runtime string) (*protocol.WorkspaceManifest, *APIError) {
	var rootIdentity string
	matches := make([][]protocol.DiscoveryEntry, len(repos))
	cursor := ""
	complete := false
	for page := 0; page < maxContractDiscoveryPages; page++ {
		res, err := a.hub.requestWorkspaceDiscovery(ctx, owner, dc, protocol.DiscoverWorkspace{Operation: "list", Path: ".", Cursor: cursor})
		if err != nil || !res.OK || res.Page == nil {
			return nil, apiErrorf(http.StatusServiceUnavailable, "could not list the repositories on %s; retry when it is connected", dc.Name)
		}
		if rootIdentity != "" && res.Page.RootIdentity != rootIdentity {
			return nil, apiErrorf(http.StatusConflict, "the repositories on %s changed while the session was starting; retry", dc.Name)
		}
		rootIdentity = res.Page.RootIdentity
		for _, e := range res.Page.Entries {
			if e.Kind != "repository" {
				continue
			}
			for i, named := range repos {
				_, shortName, qualified := strings.Cut(named.Repo, "/")
				var match bool
				switch {
				case !qualified:
					match = e.Path == named.Repo || e.Name == named.Repo
				case e.Origin != nil:
					match = strings.EqualFold(e.Origin.FullName, named.Repo) && (named.Provider == "" || e.Origin.Provider == named.Provider)
				default:
					match = e.Path == shortName || e.Name == shortName
				}
				if match {
					matches[i] = append(matches[i], e)
				}
			}
		}
		if res.Page.NextCursor == "" {
			// A last page that is still truncated hides entries that no cursor reaches.
			complete = !res.Page.Truncated
			break
		}
		cursor = res.Page.NextCursor
	}
	if !complete {
		return nil, apiErrorf(http.StatusUnprocessableEntity, "%s lists too many folders to be sure which %s this is; start it from the launch sheet and choose it there", dc.Name, repos[0].Repo)
	}
	selection := protocol.WorkspaceSelectionRequest{DaemonID: dc.ID, RootIdentity: rootIdentity}
	if len(repos) > 1 {
		selection.Layout = protocol.WorkspaceLayoutNames
	}
	seen := map[string]bool{}
	for i, named := range repos {
		switch len(matches[i]) {
		case 0:
			return nil, apiErrorf(422, "%s has no repository named %s; open or clone it there, or choose another desktop", dc.Name, named.Repo)
		case 1:
		default:
			return nil, apiErrorf(422, "%s has %d repositories matching %s; start it from the launch sheet to choose one", dc.Name, len(matches[i]), named.Repo)
		}
		entry := matches[i][0]
		if seen[entry.SourceIdentity] {
			return nil, apiErrorf(422, "Two repository names select the same folder; choose each repository once.")
		}
		seen[entry.SourceIdentity] = true
		selection.Members = append(selection.Members, protocol.SelectionRef{Path: entry.Path, SourceIdentity: entry.SourceIdentity})
	}
	m, err := a.prepareWorkspaceSelection(ctx, owner, dc, spawnSessionRequest{Selection: &selection, Runtime: runtime})
	if err != nil {
		return nil, apiErrorf(http.StatusConflict, "the repository %s on %s changed while the session was starting, or this runtime is unavailable there; retry", repos[0].Repo, dc.Name)
	}
	return m, nil
}

func contractRepositoriesProblem(req runnerStartRequest, p RunnerPrincipal) *APIError {
	if len(req.Repositories) == 0 {
		return nil
	}
	if len(req.Repositories) < 2 || len(req.Repositories) > 20 || req.NoRepo || p.Kind == sessionDelegateKind || req.Grant != nil || req.CronID != "" || len(req.WorkspaceRepos) > 0 || req.WorkspaceLaunch != "" {
		return apiErrorf(422, "repositories requires 2 to 20 repository targets on a new session")
	}
	first := req.Repositories[0]
	if (req.Repo != "" && req.Repo != first.Repo) || (req.GitURL != "" && req.GitURL != first.GitURL) || (req.Provider != "" && req.Provider != first.Provider) {
		return apiErrorf(422, "repo, git_url and provider must agree with the first repository target")
	}
	seen := map[string]bool{}
	for _, repo := range req.Repositories {
		key := strings.ToLower(repo.Provider + ":" + repo.Repo)
		if validateRepoName(repo.Repo) != nil || seen[key] || repoProviderProblem(repo.Provider, repo.Repo, false) != "" || !gitURLMatchesProvider(repo.GitURL, repo.Provider) {
			return apiErrorf(422, "repositories must name distinct valid repository targets")
		}
		seen[key] = true
	}
	return nil
}

func (a *API) contractClusterWorkspace(ctx context.Context, p RunnerPrincipal, req runnerStartRequest) (*protocol.WorkspaceManifest, *APIError) {
	if a.workspaceCluster == nil || !a.workspaceCluster.Capabilities().Supports("cluster", false) || p.spawningAccountID() == "" {
		return nil, apiErrorf(409, "The cluster runtime does not support this workspace start.")
	}
	selection := protocol.WorkspaceSelectionRequest{Layout: protocol.WorkspaceLayoutNames}
	for _, repo := range req.Repositories {
		if repo.GitURL != "" {
			ref, ok := gitprovider.Default.ParseRemoteURL(repo.GitURL)
			if !ok || (repo.Provider != "" && repo.Provider != ref.Provider) || (strings.Contains(repo.Repo, "/") && !strings.EqualFold(repo.Repo, ref.FullName())) {
				return nil, apiErrorf(422, "Each repository clone URL must match its target on a supported git provider.")
			}
			repo.Repo, repo.Provider = ref.FullName(), ref.Provider
		}
		name, provider, problem := a.resolveClusterRepo(ctx, repo.Repo, repo.Provider, req.DaemonID, p.spawningAccountID())
		if problem != "" {
			return nil, apiErrorf(422, "%s", problem)
		}
		if !strings.Contains(name, "/") {
			ref, ok := gitprovider.Default.ParseRemoteURL(a.hub.JobManager().CloneURLFor(provider, name))
			if !ok {
				return nil, apiErrorf(422, "Use owner/name for every cluster repository.")
			}
			name, provider = ref.FullName(), ref.Provider
		}
		selection.Hosted = append(selection.Hosted, protocol.HostedSelectionRef{FullName: name, Provider: firstNonEmpty(provider, "github")})
	}
	// Reuse the launch sheet's owner-authorized hosted inventory; naming a card's repository
	// never grants access to another account's sources.
	m, err := a.authorizedHostedSelectionWithProof(ctx, p.spawningAccountID(), p.proof(), selection)
	if err == nil {
		m.Root, err = a.workspaceCluster.Prepare(ctx, m)
	}
	if err != nil {
		return nil, apiErrorf(422, "The card's repositories could not all be authorized and prepared; check repository access.")
	}
	m.Digest = workspaceselection.Digest(m)
	if m.Validate() != nil {
		return nil, apiErrorf(409, "The cluster workspace could not be prepared.")
	}
	return &m, nil
}
