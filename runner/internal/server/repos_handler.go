package server

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// ─── GET /api/repos ───────────────────────────────────────────────────────────

type reposResponse struct {
	Repos   []RepoInfo `json:"repos"`
	StaleAt *string    `json:"stale_at"`
}

// HandleGetRepos returns the repositories the caller can launch on, merged
// from three sources:
//
//  1. the operator's shared GitHub organisation list (BLERG_RUNNER_GITHUB_ORG,
//     optional — RepoLister);
//  2. the caller's OWN repositories on every git provider they have stored a
//     personal token for in blerg-core (UserRepoLister) — never anyone
//     else's;
//  3. folders connected daemons have on disk that neither list covers
//     (local-only), resolved to their hosted repository when the daemons
//     report one.
//
// Hosted entries are deduplicated by provider + owner/name (case-insensitive):
// two providers can each have a repository with the same bare name. Each
// entry's checked_out lists the connected daemons holding a folder of that
// name, unless the daemons say that folder's origin is a different
// repository. stale_at is the earliest time any source served leftovers from
// a failed refresh.
func (a *API) HandleGetRepos(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	var sources [][]RepoInfo
	var staleAt *time.Time
	noteStale := func(t *time.Time) {
		if t != nil && (staleAt == nil || t.Before(*staleAt)) {
			staleAt = t
		}
	}
	// a.repos is nil when no GitHub org is configured — the normal case for a
	// plain desktop install — and a.userRepos when core isn't wired. Either
	// is just an empty source: the local-folder merge below must still run.
	if a.repos != nil {
		list, st := a.repos.List(r.Context())
		sources = append(sources, list)
		noteStale(st)
	}
	if a.userRepos != nil {
		list, st := a.userRepos.List(withCoreProof(r.Context(), proofFromPrincipal(principal)), principal.Sub)
		sources = append(sources, list)
		noteStale(st)
	}
	repos := mergeHostedRepos(sources...)

	daemons := a.hub.GetAllDaemons()
	checkedOut := make(map[string][]string)
	for _, d := range daemons {
		for _, folder := range d.CheckedOutRepos() {
			checkedOut[folder] = append(checkedOut[folder], d.Name)
		}
	}
	origins := make(map[string]*protocol.RepoOrigin, len(checkedOut))
	originOf := func(folder string) *protocol.RepoOrigin {
		o, seen := origins[folder]
		if !seen {
			if got, ok := agreedOrigin(daemons, folder); ok {
				o = &got
			}
			origins[folder] = o
		}
		return o
	}

	// A folder is the hosted repository of the same name unless the daemons
	// agree its origin is some other repository. (An unknown origin keeps the
	// historical same-name match.) Only same-named folders count: a daemon
	// launch addresses the folder by the repository's bare name.
	claimed := make(map[string]bool)
	for i := range repos {
		repos[i].CheckedOut = []string{}
		names := checkedOut[repos[i].Name]
		if names == nil {
			continue
		}
		if o := originOf(repos[i].Name); o != nil && repoKey(o.Provider, o.FullName) != repoKey(repos[i].Provider, repos[i].FullName) {
			continue
		}
		repos[i].CheckedOut = names
		claimed[repos[i].Name] = true
	}

	// Local-only folders: on a daemon, but not a listed repository.
	seen := make(map[string]bool)
	for _, d := range daemons {
		for _, folder := range d.CheckedOutRepos() {
			if claimed[folder] || seen[folder] {
				continue
			}
			seen[folder] = true
			info := RepoInfo{
				Name:       folder,
				FullName:   folder,
				CheckedOut: checkedOut[folder],
				IsLocal:    true,
			}
			// A cluster session can only clone an owner/name, and the
			// folder's name need not be its remote's repository name — so
			// FullName comes from the daemon-reported origin, never a guess.
			// When the folder is unknown, or daemons holding a folder of this
			// name disagree about (or don't all know) its origin, it stays
			// bare and is flagged not cloneable, and the UI asks for one.
			o := originOf(folder)
			cloneable := o != nil
			if cloneable {
				info.FullName = o.FullName
				info.Remote = o.FullName
				info.Provider = o.Provider
			}
			info.Cloneable = &cloneable
			repos = append(repos, info)
		}
	}

	resp := reposResponse{Repos: repos}
	if staleAt != nil {
		s := staleAt.UTC().Format(time.RFC3339)
		resp.StaleAt = &s
	}
	writeJSON(w, http.StatusOK, resp)
}

// repoKey is a hosted repository's case-insensitive identity.
func repoKey(provider, fullName string) string {
	return strings.ToLower(provider + ":" + fullName)
}

// mergeHostedRepos concatenates hosted lists, dropping later duplicates of
// the same provider + owner/name. It copies: a source's slice may be its
// cache, which the caller then annotates.
func mergeHostedRepos(sources ...[]RepoInfo) []RepoInfo {
	out := []RepoInfo{}
	seen := make(map[string]bool)
	for _, src := range sources {
		for _, r := range src {
			k := repoKey(r.Provider, r.FullName)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

// agreedOrigin returns the hosted repository reported for folderName when
// every connected daemon that has a folder of that name reports the same one.
// Two machines' same-named folders can be different repositories; with no
// agreement there is nothing safe to show.
func agreedOrigin(daemons []*DaemonConn, folderName string) (protocol.RepoOrigin, bool) {
	var agreed protocol.RepoOrigin
	found := false
	for _, d := range daemons {
		if !slices.Contains(d.CheckedOutRepos(), folderName) {
			continue
		}
		o, ok := d.RepoOrigin(folderName)
		if !ok || (found && repoKey(o.Provider, o.FullName) != repoKey(agreed.Provider, agreed.FullName)) {
			return protocol.RepoOrigin{}, false
		}
		agreed, found = o, true
	}
	return agreed, found
}
