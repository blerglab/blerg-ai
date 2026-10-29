package server

import (
	"context"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

const msgClusterBareRepo = "repo must be given as org/name for cluster sessions (or set BLERG_RUNNER_GITHUB_ORG)"

// resolveClusterRepoName turns a bare folder name into the repository a cluster clones.
//
// A configured default organization or git base (allowBare) is the install's own answer and wins
// over any desktop's report. Without one, only the desktop the start names (the launch sheet's
// chosen machine, or a desktop parent's own machine for its child) may answer: another person's
// desktop must never decide which
// repository a start clones, and two machines' same-named folders can be different repositories.
func resolveClusterRepoName(repo, provider string, desktop *DaemonConn, allowBare bool) (string, string, string) {
	if strings.Contains(repo, "/") {
		return repo, provider, repoProviderProblem(provider, repo, true)
	}
	if allowBare {
		return repo, provider, ""
	}
	if desktop != nil {
		if origin, ok := desktop.RepoOrigin(repo); ok && (provider == "" || origin.Provider == provider) {
			if ref, ok := gitprovider.Default.ParseFullName(origin.Provider, origin.FullName); ok {
				return ref.FullName(), origin.Provider, ""
			}
		}
	}
	return "", "", msgClusterBareRepo
}

// resolveClusterRepo resolves a cluster start's repo, consulting only the explicitly named desktop.
func (a *API) resolveClusterRepo(ctx context.Context, repo, provider, daemonID string, owners ...string) (string, string, string) {
	jm := a.hub.JobManager()
	if jm == nil {
		return "", "", "cluster runtime is not configured"
	}
	allowBare := jm.GitHubOrg != "" || jm.ExplicitGitURLBase != ""
	var desktop *DaemonConn
	if daemonID != "" && !strings.Contains(repo, "/") && !allowBare {
		owner := ""
		if len(owners) > 0 {
			owner = owners[0]
		}
		if desktop = a.hub.ownedHostDaemon(ctx, owner, daemonID); desktop == nil {
			return "", "", "the selected desktop is disconnected; use owner/name for the cluster"
		}
	}
	return resolveClusterRepoName(repo, provider, desktop, allowBare)
}
