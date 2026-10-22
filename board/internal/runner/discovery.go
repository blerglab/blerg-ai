package runner

import (
	"context"
	"net/url"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engines"
)

// Typed discovery deliberately excludes credentials and arbitrary upstream
// fields. The board returns this metadata, never the bearer used to request it.
type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Section       string   `json:"section"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort,omitempty"`
	EffortKind    string   `json:"effort_kind,omitempty"`
}
type ModelList struct {
	Refreshing    bool       `json:"refreshing,omitempty"`
	Error         string     `json:"error,omitempty"`
	Models        []Model    `json:"models"`
	Source        string     `json:"source"`
	FetchedAt     *time.Time `json:"fetched_at"`
	DaemonID      string     `json:"daemon_id,omitempty"`
	EngineEfforts []string   `json:"engine_efforts"`
}
type LaunchEngine struct {
	engines.Descriptor
	Models   ModelList `json:"models"`
	Runtime  string    `json:"runtime"`
	DaemonID string    `json:"daemon_id,omitempty"`
}
type LaunchOptions struct {
	State      string         `json:"state"`
	Refreshing bool           `json:"refreshing,omitempty"`
	Runtime    string         `json:"runtime"`
	Engines    []LaunchEngine `json:"engines"`
	Error      string         `json:"error,omitempty"`
	// Targets are where a session can run: the cluster and each connected
	// daemon with the runtimes it offers.
	Targets []LaunchTarget `json:"targets,omitempty"`
}

// LaunchTarget is one execution target the runner offers: a daemon (by id)
// or, without one, the cluster.
type LaunchTarget struct {
	DaemonID string   `json:"daemon_id,omitempty"`
	Name     string   `json:"name"`
	Runtimes []string `json:"runtimes"`
	// HasRepo (a desktop, when discovery named a repo): whether it has a folder for that repo.
	// nil when the runner did not say (no repo asked, or an older runner).
	HasRepo *bool `json:"has_repo,omitempty"`
	// RepoOrigin (only the desktop a repo-origin discovery names, for a bare folder it has): the
	// hosted repository that folder's origin remote points at; nil when it has none.
	RepoOrigin *RepoOrigin `json:"repo_origin,omitempty"`
	// ResolvesBareRepos (the cluster): the runner's install resolves a bare repo name itself.
	ResolvesBareRepos bool `json:"resolves_bare_repos,omitempty"`
}

// RepoOrigin is a hosted repository: its provider ("github", …) and owner/name.
type RepoOrigin struct {
	Provider string `json:"provider"`
	FullName string `json:"full_name"`
}
type Discoverer interface {
	Discover(context.Context, string, string, bool) (LaunchOptions, error)
}

func (k *BlergRunner) Discover(ctx context.Context, token, repo string, refresh bool) (LaunchOptions, error) {
	q := url.Values{"kind": {"agent"}}
	if k.runtime != "" {
		q.Set("runtime", k.runtime)
	}
	if repo != "" {
		q.Set("repo", repo)
	}
	if refresh {
		q.Set("refresh", "1")
	}
	var out LaunchOptions
	_, err := k.doAs(ctx, token, "GET", "/api/runner/launch-options?"+q.Encode(), nil, &out)
	if out.Engines == nil {
		out.Engines = []LaunchEngine{}
	}
	return out, err
}

type SessionDiscoverer interface {
	DiscoverSession(context.Context, string, string, bool) (LaunchOptions, error)
}

func (k *BlergRunner) DiscoverSession(ctx context.Context, token, sessionID string, refresh bool) (LaunchOptions, error) {
	q := url.Values{"session_id": {sessionID}}
	if refresh {
		q.Set("refresh", "1")
	}
	var out LaunchOptions
	_, err := k.doAs(ctx, token, "GET", "/api/runner/launch-options?"+q.Encode(), nil, &out)
	if out.Engines == nil {
		out.Engines = []LaunchEngine{}
	}
	return out, err
}

// TargetDiscoverer lists placement independently of the install-wide runtime default.
type TargetDiscoverer interface {
	DiscoverTargets(context.Context, string, string) (LaunchOptions, error)
}

func (k *BlergRunner) DiscoverTargets(ctx context.Context, token, repo string) (LaunchOptions, error) {
	q := url.Values{"kind": {"agent"}}
	if repo != "" {
		q.Set("repo", repo)
	}
	var out LaunchOptions
	_, err := k.doAs(ctx, token, "GET", "/api/runner/launch-options?"+q.Encode(), nil, &out)
	return out, err
}

// PlacementDiscoverer reads the engine catalog for the board's selected execution target.
type PlacementDiscoverer interface {
	DiscoverAt(context.Context, string, string, string, string, bool) (LaunchOptions, error)
}

func (k *BlergRunner) DiscoverAt(ctx context.Context, token, repo, runtime, daemon string, refresh bool) (LaunchOptions, error) {
	q := url.Values{"kind": {"agent"}, "runtime": {runtime}}
	if repo != "" {
		q.Set("repo", repo)
	}
	if daemon != "" {
		q.Set("daemon_id", daemon)
	}
	if refresh {
		q.Set("refresh", "1")
	}
	var out LaunchOptions
	_, err := k.doAs(ctx, token, "GET", "/api/runner/launch-options?"+q.Encode(), nil, &out)
	return out, err
}

// RepoOriginDiscoverer asks the runner where one desktop's folder's remote points. Only the named
// desktop answers (the runner reports no other desktop's remote).
type RepoOriginDiscoverer interface {
	DiscoverRepoOrigin(ctx context.Context, token, repo, daemon string) (LaunchOptions, error)
}

func (k *BlergRunner) DiscoverRepoOrigin(ctx context.Context, token, repo, daemon string) (LaunchOptions, error) {
	q := url.Values{"kind": {"agent"}, "runtime": {"daemon"}, "repo": {repo}, "daemon_id": {daemon}}
	var out LaunchOptions
	_, err := k.doAs(ctx, token, "GET", "/api/runner/launch-options?"+q.Encode(), nil, &out)
	return out, err
}

// RepoChoice is one repository a connected desktop has checked out, as the New board form offers
// it: Repo is what the board stores (owner/name when the desktops agree on its remote, else the
// folder name), Folder the folder, Desktops the desktops holding it.
type RepoChoice struct {
	Repo     string   `json:"repo"`
	Folder   string   `json:"folder"`
	Desktops []string `json:"desktops"`
}

// RepoChoiceLister lists the repositories the connected desktops know.
type RepoChoiceLister interface {
	RepoChoices(context.Context) ([]RepoChoice, error)
}

// RepoChoices asks the runner, with the board's own runner credential, which repositories its
// connected desktops have checked out (GET /api/board-repo-choices). Nothing else is returned.
func (k *BlergRunner) RepoChoices(ctx context.Context) ([]RepoChoice, error) {
	var out struct {
		Repos []RepoChoice `json:"repos"`
	}
	if _, err := k.do(ctx, "GET", "/api/board-repo-choices", nil, &out); err != nil {
		return nil, err
	}
	if out.Repos == nil {
		out.Repos = []RepoChoice{}
	}
	return out.Repos, nil
}
