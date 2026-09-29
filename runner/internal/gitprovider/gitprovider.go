// Package gitprovider is the one place blerg-runner knows anything specific
// about a git hosting service (GitHub, GitLab, …): how its remote URLs look,
// what its owner/repository names may contain, how an authenticated HTTPS
// clone URL is spelled, and how to list the repositories a user's token can
// see.
//
// Every caller goes through a Registry — never through a hardcoded "github"
// branch — so adding a third service (Bitbucket, a self-hosted GitLab or
// Gitea instance, …) is one more Provider implementation plus one Register
// call, and nothing else changes:
//
//	gitprovider.Default.Register(gitprovider.NewGitLabAt("gitlab-acme", "git.acme.example", "https://git.acme.example", nil))
//
// A provider's ID is also the credential kind a user's personal token for it
// is stored under in blerg-core's vault (core/internal/api/credential_handlers.go
// gitCredentialKinds) and the "provider" value in the runner's JSON.
package gitprovider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Provider is one git hosting service.
type Provider interface {
	// ID is the stable identifier: the credential kind a personal token for
	// this service is stored under in core's vault, and the "provider" value
	// in API responses ("github", "gitlab").
	ID() string
	// Host is the (lower-case) host the service is known by — the host its
	// remote URLs, clone URLs and web pages use ("github.com").
	Host() string
	// ParseRemoteURL extracts owner and repository name from a git remote URL
	// on this provider's host (https/http, ssh://, and scp-like forms, with or
	// without .git). Anything else — another host, a port, a query, extra
	// path segments, names outside the provider's charset — is ok=false.
	ParseRemoteURL(raw string) (owner, name string, ok bool)
	// ValidOwnerName / ValidRepoName are the provider's own naming rules.
	// An owner may contain "/" where the provider has nested namespaces
	// (GitLab subgroups).
	ValidOwnerName(owner string) bool
	ValidRepoName(name string) bool
	// TokenUser is the HTTPS username that accompanies a token in a clone URL.
	TokenUser() string
	// CloneURL is the HTTPS clone URL for owner/name; with a non-empty token
	// it carries TokenUser():token as userinfo, without one it has none
	// (public repositories, or a token injected later). The result with a
	// token is a secret: never log it — see RedactURL.
	CloneURL(owner, name, token string) string
	// ListMine lists the repositories token's owner can see on this service,
	// most recently active first, capped at MaxListedRepos.
	ListMine(ctx context.Context, token string) ([]Repo, error)
	// Private reports whether owner/name needs authentication to clone,
	// asked with token (the caller's own). A repository the token cannot
	// see, or any failure, is an error: "can't tell", never "public".
	Private(ctx context.Context, token, owner, name string) (bool, error)
}

// Repo is a repository a Provider listed.
type Repo struct {
	Provider string // Provider.ID()
	Owner    string // may contain "/" (GitLab subgroups)
	Name     string
	Private  bool
}

// FullName is "owner/name".
func (r Repo) FullName() string { return r.Owner + "/" + r.Name }

// Ref identifies a repository on a provider.
type Ref struct {
	Provider string `json:"provider"`
	Owner    string `json:"owner"`
	Name     string `json:"name"`
}

// FullName is "owner/name".
func (r Ref) FullName() string { return r.Owner + "/" + r.Name }

// Key is a case-insensitive identity for dedupe: the same repository spelt
// with different case, or reached through https and ssh remotes, is one key.
// (Both GitHub and GitLab treat paths case-insensitively.)
func (r Ref) Key() string {
	return strings.ToLower(r.Provider + ":" + r.Owner + "/" + r.Name)
}

// Registry maps hosts and credential kinds to Providers. Safe for concurrent
// use.
type Registry struct {
	mu     sync.RWMutex
	byID   map[string]Provider
	byHost map[string]Provider
}

// NewRegistry builds a registry of ps; it panics on a duplicate id or host,
// which is a programming error in the static list.
func NewRegistry(ps ...Provider) *Registry {
	r := &Registry{byID: map[string]Provider{}, byHost: map[string]Provider{}}
	for _, p := range ps {
		if err := r.Register(p); err != nil {
			panic(err)
		}
	}
	return r
}

// Register adds p. An id or host that is already registered is an error:
// two providers must never claim the same remote or the same credential.
func (r *Registry) Register(p Provider) error {
	id, host := p.ID(), strings.ToLower(p.Host())
	if id == "" || host == "" {
		return errors.New("gitprovider: provider needs an id and a host")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[id]; dup {
		return fmt.Errorf("gitprovider: id %q already registered", id)
	}
	if _, dup := r.byHost[host]; dup {
		return fmt.Errorf("gitprovider: host %q already registered", host)
	}
	r.byID[id] = p
	r.byHost[host] = p
	return nil
}

// ProviderForHost returns the provider known by host (case-insensitive).
func (r *Registry) ProviderForHost(host string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byHost[strings.ToLower(host)]
	return p, ok
}

// ProviderForCredentialKind returns the provider whose personal token is
// stored in core's vault under kind (= its ID).
func (r *Registry) ProviderForCredentialKind(kind string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[kind]
	return p, ok
}

// Providers returns every registered provider, sorted by ID.
func (r *Registry) Providers() []Provider {
	r.mu.RLock()
	out := make([]Provider, 0, len(r.byID))
	for _, p := range r.byID {
		out = append(out, p)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// CredentialKinds returns every registered provider's ID, sorted.
func (r *Registry) CredentialKinds() []string {
	ps := r.Providers()
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID()
	}
	return out
}

// ParseRemoteURL resolves a remote URL against every registered provider.
// Hosts are unique per registry, so at most one provider can match.
func (r *Registry) ParseRemoteURL(raw string) (Ref, bool) {
	host, ok := remoteHost(raw)
	if !ok {
		return Ref{}, false
	}
	p, ok := r.ProviderForHost(host)
	if !ok {
		return Ref{}, false
	}
	owner, name, ok := p.ParseRemoteURL(raw)
	if !ok {
		return Ref{}, false
	}
	return Ref{Provider: p.ID(), Owner: owner, Name: name}, true
}

// ParseFullName splits "owner/name" for the provider with id providerID and
// checks it against that provider's naming rules. The split is at the LAST
// "/", so a nested GitLab namespace stays in owner.
func (r *Registry) ParseFullName(providerID, full string) (Ref, bool) {
	p, ok := r.ProviderForCredentialKind(providerID)
	if !ok {
		return Ref{}, false
	}
	i := strings.LastIndex(full, "/")
	if i <= 0 || i == len(full)-1 {
		return Ref{}, false
	}
	owner, name := full[:i], full[i+1:]
	if !p.ValidOwnerName(owner) || !p.ValidRepoName(name) {
		return Ref{}, false
	}
	return Ref{Provider: p.ID(), Owner: owner, Name: name}, true
}

// TokenUserForHost is the HTTPS username to pair with a token for a clone URL
// on host: the registered provider's own, else GitHub's historical
// "x-access-token" (what the runner always used before providers existed).
func (r *Registry) TokenUserForHost(host string) string {
	if p, ok := r.ProviderForHost(host); ok {
		return p.TokenUser()
	}
	return githubTokenUser
}

// Default is the process-wide registry every runner component uses.
var Default = NewRegistry(NewGitHub(nil), NewGitLab(nil))

// GitHubID is the default provider's ID — the provider a bare "org/name"
// with no provider named means, for compatibility with every caller that
// predates providers.
const GitHubID = "github"

// RedactURL returns raw with any userinfo password replaced by "REDACTED",
// for logs and error messages. Unparseable input is returned as a fixed
// placeholder rather than echoed, since it may still hold a secret.
func RedactURL(raw string) string {
	if !strings.Contains(raw, "://") {
		return raw // scp-like remotes carry no password
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable url>"
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "REDACTED")
		} else if u.User.Username() != "" {
			// A bare token in the username slot (https://<token>@host/…).
			u.User = url.User("REDACTED")
		}
	}
	return u.String()
}

// remoteHost is the host part of a remote URL in any of the accepted forms,
// lower-cased; ok=false when there is none.
func remoteHost(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00\\") {
		return "", false
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", false
		}
		return strings.ToLower(u.Hostname()), true
	}
	hostPart, _, ok := strings.Cut(raw, ":")
	if !ok {
		return "", false
	}
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	if hostPart == "" {
		return "", false
	}
	return strings.ToLower(hostPart), true
}

// remotePath extracts the repository path from a remote URL on host, with
// leading/trailing slashes and one ".git" suffix removed. Accepted forms:
//
//	https://host/path(.git)(/)   (http:// too; userinfo ignored)
//	ssh://[user@]host/path(.git)(/)
//	[user@]host:path(.git)(/)     (scp-like)
//
// A different host, any port, a query, a fragment, a percent-encoded path, or
// an scp path starting with "/" is ok=false. The segments are NOT validated
// here — each provider applies its own naming rules to them.
func remotePath(raw, host string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00\\") {
		return "", false
	}
	var path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", false
		}
		switch strings.ToLower(u.Scheme) {
		case "https", "http", "ssh":
		default:
			return "", false
		}
		if u.Port() != "" || !strings.EqualFold(u.Hostname(), host) {
			return "", false
		}
		if u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.Contains(raw, "?") || strings.Contains(raw, "#") {
			return "", false
		}
		if u.RawPath != "" { // percent-encoded path: never a plain owner/name
			return "", false
		}
		path = u.Path
	} else {
		hostPart, p, ok := strings.Cut(raw, ":")
		if !ok {
			return "", false
		}
		if at := strings.LastIndex(hostPart, "@"); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		if !strings.EqualFold(hostPart, host) || strings.HasPrefix(p, "/") {
			return "", false
		}
		path = p
	}
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimRight(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" {
		return "", false
	}
	return path, true
}

// httpsCloneURL is the shared shape of every provider's clone URL.
func httpsCloneURL(host, owner, name, user, token string) string {
	u := url.URL{Scheme: "https", Host: host, Path: "/" + owner + "/" + name + ".git"}
	if token != "" {
		u.User = url.UserPassword(user, token)
	}
	return u.String()
}
