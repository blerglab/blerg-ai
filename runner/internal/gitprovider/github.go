package gitprovider

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// githubTokenUser is the username GitHub documents for token-over-HTTPS
// ("x-access-token" for installation tokens; GitHub ignores the username for
// personal access tokens, so the same value serves both). It is what the
// runner used before providers existed.
const githubTokenUser = "x-access-token"

var (
	// GitHub owner: alphanumerics and hyphens, no leading hyphen, at most
	// 39 characters. (Newer accounts also can't end in or double a hyphen;
	// older ones can, so that is not enforced.)
	githubOwnerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	// GitHub repository name: alphanumerics, '.', '_', '-', at most 100.
	githubNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// GitHub is github.com (or, via NewGitHubAt, a GitHub Enterprise Server).
type GitHub struct {
	id, host, apiBase string
	client            *http.Client
}

// NewGitHub is the github.com provider. client may be nil.
func NewGitHub(client *http.Client) *GitHub {
	return NewGitHubAt(GitHubID, "github.com", "https://api.github.com", client)
}

// NewGitHubAt is a GitHub-API-compatible provider at another host — a GitHub
// Enterprise Server, whose REST API lives at https://<host>/api/v3 — or, in
// tests, at a fake API. id must be unique in the registry it joins and is
// the credential kind its tokens are stored under.
func NewGitHubAt(id, host, apiBase string, client *http.Client) *GitHub {
	return &GitHub{id: id, host: strings.ToLower(host), apiBase: strings.TrimRight(apiBase, "/"), client: client}
}

func (g *GitHub) ID() string        { return g.id }
func (g *GitHub) Host() string      { return g.host }
func (g *GitHub) TokenUser() string { return githubTokenUser }

// ValidOwnerName reports whether s is a GitHub user/organisation name.
func (g *GitHub) ValidOwnerName(s string) bool { return githubOwnerRe.MatchString(s) }

// ValidRepoName reports whether s is a GitHub repository name that is also a
// safe directory name: no leading dot, so never "." / ".." or hidden.
func (g *GitHub) ValidRepoName(s string) bool {
	return githubNameRe.MatchString(s) && !strings.HasPrefix(s, ".")
}

// ParseRemoteURL accepts exactly owner/name on g's host.
func (g *GitHub) ParseRemoteURL(raw string) (string, string, bool) {
	path, ok := remotePath(raw, g.host)
	if !ok {
		return "", "", false
	}
	owner, name, ok := strings.Cut(path, "/")
	if !ok || !g.ValidOwnerName(owner) || !g.ValidRepoName(name) {
		return "", "", false
	}
	return owner, name, true
}

// CloneURL is https://github.com/owner/name.git, with
// x-access-token:<token>@ when a token is given.
func (g *GitHub) CloneURL(owner, name, token string) string {
	return httpsCloneURL(g.host, owner, name, githubTokenUser, token)
}

// githubRepo is the subset of GitHub's repository object ListMine reads.
type githubRepo struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

// Private is GET /repos/{owner}/{name}'s "private".
func (g *GitHub) Private(ctx context.Context, token, owner, name string) (bool, error) {
	var r githubRepo
	err := getJSON(ctx, g.client, g.id, g.apiBase+"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name),
		func(req *http.Request) {
			req.Header.Set("Accept", "application/vnd.github+json")
			req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
			req.Header.Set("Authorization", "Bearer "+token)
		}, &r)
	return r.Private, err
}

// ListMine is GET /user/repos with every affiliation (owned, collaborator,
// organisation member), most recently pushed first, following Link headers.
// A fine-grained token lists only the repositories it was granted.
func (g *GitHub) ListMine(ctx context.Context, token string) ([]Repo, error) {
	first := g.apiBase + "/user/repos?affiliation=owner,collaborator,organization_member&sort=pushed&per_page=100"
	var out []Repo
	err := listPages(ctx, g.client, g.id, first,
		func(req *http.Request) {
			req.Header.Set("Accept", "application/vnd.github+json")
			req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
			req.Header.Set("Authorization", "Bearer "+token)
		},
		func(r io.Reader) (int, error) {
			var page []githubRepo
			if err := decodeJSONArray(r, &page); err != nil {
				return 0, err
			}
			n := 0
			for _, gr := range page {
				if len(out) >= MaxListedRepos {
					break
				}
				owner, name, ok := strings.Cut(gr.FullName, "/")
				if !ok || !g.ValidOwnerName(owner) || !g.ValidRepoName(name) {
					continue // never pass on a name we would refuse elsewhere
				}
				out = append(out, Repo{Provider: g.id, Owner: owner, Name: name, Private: gr.Private})
				n++
			}
			return n, nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}
