package gitprovider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// gitlabTokenUser: GitLab does not check the username sent with a personal,
// project or group access token over HTTPS ("any non-empty string"), and
// requires "oauth2" for an OAuth token — so "oauth2" works for all of them.
const gitlabTokenUser = "oauth2"

// maxGitLabDepth bounds namespace nesting: GitLab allows subgroups up to 20
// levels deep, so a project path has at most 21 segments.
const maxGitLabDepth = 21

// gitlabSegmentRe is one GitLab path segment (a user, group, subgroup or
// project path): letters, digits, '_', '.', '-', starting and ending with a
// letter or digit (docs.gitlab.com "reserved names" / path rules). Consecutive
// special characters and a .git/.atom suffix are rejected separately.
var (
	gitlabSegmentRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,253}[A-Za-z0-9])?$`)
	gitlabDoubleRe  = regexp.MustCompile(`[_.-]{2}`)
)

func validGitLabSegment(s string) bool {
	if !gitlabSegmentRe.MatchString(s) || gitlabDoubleRe.MatchString(s) {
		return false
	}
	l := strings.ToLower(s)
	return !strings.HasSuffix(l, ".git") && !strings.HasSuffix(l, ".atom")
}

// GitLab is gitlab.com (or, via NewGitLabAt, a self-managed instance).
type GitLab struct {
	id, host, apiBase string
	client            *http.Client
}

// NewGitLab is the gitlab.com provider. client may be nil.
func NewGitLab(client *http.Client) *GitLab {
	return NewGitLabAt("gitlab", "gitlab.com", "https://gitlab.com", client)
}

// NewGitLabAt is a GitLab instance at host whose API is at
// <baseURL>/api/v4. id must be unique in the registry it joins and is the
// credential kind its tokens are stored under.
func NewGitLabAt(id, host, baseURL string, client *http.Client) *GitLab {
	return &GitLab{id: id, host: strings.ToLower(host), apiBase: strings.TrimRight(baseURL, "/") + "/api/v4", client: client}
}

func (g *GitLab) ID() string        { return g.id }
func (g *GitLab) Host() string      { return g.host }
func (g *GitLab) TokenUser() string { return gitlabTokenUser }

// ValidOwnerName reports whether s is a GitLab namespace path: a user or
// group, optionally followed by subgroups ("group/sub/sub2").
func (g *GitLab) ValidOwnerName(s string) bool {
	segs := strings.Split(s, "/")
	if len(segs) > maxGitLabDepth-1 {
		return false
	}
	for _, seg := range segs {
		if !validGitLabSegment(seg) {
			return false
		}
	}
	return true
}

// ValidRepoName reports whether s is a GitLab project path.
func (g *GitLab) ValidRepoName(s string) bool { return validGitLabSegment(s) }

// ParseRemoteURL accepts namespace/project on g's host, where namespace may
// be nested (group/subgroup/project → owner "group/subgroup").
func (g *GitLab) ParseRemoteURL(raw string) (string, string, bool) {
	path, ok := remotePath(raw, g.host)
	if !ok {
		return "", "", false
	}
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "", "", false
	}
	owner, name := path[:i], path[i+1:]
	if !g.ValidOwnerName(owner) || !g.ValidRepoName(name) {
		return "", "", false
	}
	return owner, name, true
}

// CloneURL is https://gitlab.com/owner/name.git, with oauth2:<token>@ when a
// token is given.
func (g *GitLab) CloneURL(owner, name, token string) string {
	return httpsCloneURL(g.host, owner, name, gitlabTokenUser, token)
}

// gitlabProject is the subset of GitLab's (simple) project object ListMine
// reads.
type gitlabProject struct {
	PathWithNamespace string `json:"path_with_namespace"`
	Visibility        string `json:"visibility"`
}

// Private is GET /api/v4/projects/{owner%2Fname}'s visibility: anything but
// "public" ("internal", "private") needs authentication to clone.
func (g *GitLab) Private(ctx context.Context, token, owner, name string) (bool, error) {
	var p gitlabProject
	err := getJSON(ctx, g.client, g.id, g.apiBase+"/projects/"+url.PathEscape(owner+"/"+name),
		func(req *http.Request) {
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
		}, &p)
	if err == nil && p.Visibility == "" {
		return false, fmt.Errorf("%s api: no visibility", g.id)
	}
	return p.Visibility != "public", err
}

// ListMine is GET /api/v4/projects?membership=true — every project the
// token's user is a member of (directly or through a group), most recently
// active first, following Link headers. Needs a token with read_api (or api).
func (g *GitLab) ListMine(ctx context.Context, token string) ([]Repo, error) {
	first := g.apiBase + "/projects?membership=true&simple=true&archived=false&order_by=last_activity_at&sort=desc&per_page=100"
	var out []Repo
	err := listPages(ctx, g.client, g.id, first,
		func(req *http.Request) {
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
		},
		func(r io.Reader) (int, error) {
			var page []gitlabProject
			if err := decodeJSONArray(r, &page); err != nil {
				return 0, err
			}
			n := 0
			for _, p := range page {
				if len(out) >= MaxListedRepos {
					break
				}
				i := strings.LastIndex(p.PathWithNamespace, "/")
				if i <= 0 {
					continue
				}
				owner, name := p.PathWithNamespace[:i], p.PathWithNamespace[i+1:]
				if !g.ValidOwnerName(owner) || !g.ValidRepoName(name) {
					continue
				}
				out = append(out, Repo{Provider: g.id, Owner: owner, Name: name, Private: p.Visibility != "public"})
				n++
			}
			return n, nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}
