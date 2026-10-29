package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A contract start (POST /api/runner/start: a board, a cron, an API caller) names its repository the
// way it knows it: a folder name or owner/name. A desktop with workspace discovery (0.5) launches
// only from a fresh, bound selection of source references, which the launch sheet gets from
// discovery. The runner resolves the named repository into that selection itself, against the
// same desktop and runtime the start already chose, exactly as the launch sheet would: list the
// desktop's repositories, pick the one the start names, resolve and bind it.

// maxContractDiscoveryPages bounds the listing a contract start reads to find its repository.
const maxContractDiscoveryPages = 20

// contractRepoWorkspace resolves repo on dc into a bound one-repository workspace for runtime
// ("docker" or "daemon"), from fresh discovery only (never the desktop's cached inventory): a folder
// name matches the folder; owner/name matches a repository whose fresh origin is exactly that
// (or, as before, a folder of that name with no origin at all), and a folder whose origin is
// another repository never matches. It needs the complete listing: a truncated one, or one longer
// than the page budget, is refused rather than taken as proof that a match is unique. Its refusals
// say what to do.
func (a *API) contractRepoWorkspace(ctx context.Context, owner string, dc *DaemonConn, repo, provider, runtime string) (*protocol.WorkspaceManifest, *APIError) {
	_, shortName, qualified := strings.Cut(repo, "/")
	var rootIdentity string
	var matches []protocol.DiscoveryEntry
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
			var match bool
			switch {
			case !qualified:
				match = e.Path == repo || e.Name == repo
			case e.Origin != nil:
				match = strings.EqualFold(e.Origin.FullName, repo) && (provider == "" || e.Origin.Provider == provider)
			default:
				match = e.Path == shortName || e.Name == shortName
			}
			if match {
				matches = append(matches, e)
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
		return nil, apiErrorf(http.StatusUnprocessableEntity, "%s lists too many folders to be sure which %s this is; start it from the launch sheet and choose it there", dc.Name, repo)
	}
	switch len(matches) {
	case 0:
		return nil, apiErrorf(http.StatusUnprocessableEntity, "%s has no repository named %s; open or clone it there, or choose another desktop", dc.Name, repo)
	case 1:
	default:
		return nil, apiErrorf(http.StatusUnprocessableEntity, "%s has %d repositories matching %s; start it from the launch sheet to choose one", dc.Name, len(matches), repo)
	}
	selection := protocol.WorkspaceSelectionRequest{DaemonID: dc.ID, RootIdentity: rootIdentity,
		Members: []protocol.SelectionRef{{Path: matches[0].Path, SourceIdentity: matches[0].SourceIdentity}}}
	m, err := a.prepareWorkspaceSelection(ctx, owner, dc, spawnSessionRequest{Selection: &selection, Runtime: runtime})
	if err != nil {
		return nil, apiErrorf(http.StatusConflict, "%s", fmt.Sprintf("the repository %s on %s changed while the session was starting, or this runtime is unavailable there; retry", repo, dc.Name))
	}
	return m, nil
}
