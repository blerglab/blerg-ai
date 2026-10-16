package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

// RepoInfo describes a repository (hosted, or a local folder) and which
// daemons have it.
type RepoInfo struct {
	Name       string   `json:"name"`
	FullName   string   `json:"full_name"`
	CheckedOut []string `json:"checked_out"`        // daemon names that have this repo on disk
	IsLocal    bool     `json:"is_local,omitempty"` // true for local-only folders not in a hosted list
	// Provider is the git provider FullName lives on — a gitprovider ID
	// ("github", "gitlab", …). Set on every hosted-list entry and on a local
	// folder whose origin is known; empty only on an unresolved local folder.
	Provider string `json:"provider,omitempty"`
	// Remote is the "owner/name" a local-only folder's origin points at on
	// Provider, as reported by the daemon(s) that have it — the folder's name
	// need not match it. Empty when unknown, and on hosted-list entries.
	Remote string `json:"remote,omitempty"`
	// Cloneable is set on local-only folders only: true when FullName is a
	// real owner/name a cluster pod can clone (Remote is known), false when
	// FullName is just the bare folder name. Nil on hosted-list entries,
	// whose FullName is always owner/name.
	Cloneable *bool `json:"cloneable,omitempty"`
}

// RepoLister fetches repositories for one operator-configured GitHub
// organisation (BLERG_RUNNER_GITHUB_ORG/_TOKEN) — a shared, curated list shown
// to everyone, alongside each person's own repositories (UserRepoLister). It
// caches the results for ttl, and returns stale results with a stale_at
// timestamp on API error rather than failing.
//
// Past ttl the cached list is still served at once while one background
// refresh brings it up to date (stale-while-revalidate); only an empty cache,
// or one older than maxStale, makes a request wait — on the refresh already
// in flight, if there is one.
type RepoLister struct {
	org        string
	token      string
	hub        *Hub
	ttl        time.Duration
	maxStale   time.Duration
	httpClient http.Client
	now        func() time.Time
	// fetch lists the organisation's repositories (fetchFromGitHub; tests
	// swap it).
	fetch      func(ctx context.Context) ([]githubRepo, error)
	mu         sync.Mutex
	cached     []RepoInfo
	cachedAt   time.Time
	staleAt    *time.Time
	refreshing chan struct{} // non-nil while a refresh is in flight
	bg         sync.WaitGroup
}

// NewRepoLister creates a RepoLister.  token may be empty for public orgs.
func NewRepoLister(org, token string, hub *Hub) *RepoLister {
	rl := &RepoLister{
		org:        org,
		token:      token,
		hub:        hub,
		ttl:        5 * time.Minute,
		maxStale:   30 * time.Minute,
		httpClient: http.Client{Timeout: 10 * time.Second},
		now:        time.Now,
	}
	rl.fetch = rl.fetchFromGitHub
	return rl
}

// githubRepo is the minimal subset of the GitHub repo object we care about.
type githubRepo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
}

// List returns the cached repo list, refreshing from the GitHub API when the
// cache is past its ttl — in the background while the cached list is younger
// than maxStale, otherwise before answering. On API failure it returns the
// last-known results together with a non-nil stale_at timestamp.
func (rl *RepoLister) List(ctx context.Context) ([]RepoInfo, *time.Time) {
	rl.mu.Lock()
	age := rl.now().Sub(rl.cachedAt)
	if rl.cached != nil && age < rl.ttl {
		defer rl.mu.Unlock()
		return rl.cached, rl.staleAt
	}
	servable := rl.cached != nil && age < rl.maxStale
	if rl.refreshing == nil {
		rl.refreshing = make(chan struct{})
		rl.bg.Add(1)
		// The refresh outlives this request: whoever waits for it, and
		// whoever was served the old list meanwhile, all want its answer.
		go rl.refresh(context.WithoutCancel(ctx))
	}
	wait := rl.refreshing
	if servable {
		defer rl.mu.Unlock()
		return rl.cached, rl.staleAt
	}
	rl.mu.Unlock()

	select {
	case <-wait:
	case <-ctx.Done():
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.cached, rl.staleAt
}

// refresh fetches the organisation's list into the cache and ends the flight.
func (rl *RepoLister) refresh(ctx context.Context) {
	defer rl.bg.Done()
	repos, err := rl.fetch(ctx)

	rl.mu.Lock()
	defer rl.mu.Unlock()
	close(rl.refreshing)
	rl.refreshing = nil
	if err != nil {
		// Keep the stale data (possibly nil on first call) and mark stale_at.
		now := rl.now()
		rl.staleAt = &now
		return
	}
	result := make([]RepoInfo, len(repos))
	for i, r := range repos {
		result[i] = RepoInfo{
			Name:       r.Name,
			FullName:   r.FullName,
			Provider:   gitprovider.GitHubID,
			CheckedOut: []string{},
		}
	}
	rl.cached = result
	rl.cachedAt = rl.now()
	rl.staleAt = nil
}

func (rl *RepoLister) fetchFromGitHub(ctx context.Context) ([]githubRepo, error) {
	url := fmt.Sprintf("https://api.github.com/orgs/%s/repos?per_page=100&type=all", rl.org)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if rl.token != "" {
		req.Header.Set("Authorization", "Bearer "+rl.token)
	}

	resp, err := rl.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: status %d", resp.StatusCode)
	}

	var repos []githubRepo
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, err
	}
	return repos, nil
}
