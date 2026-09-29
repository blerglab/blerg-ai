package server

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

// UserRepoLister lists, per calling account, the repositories that account's
// own git-provider tokens can see — the personal tokens stored in blerg-core's
// vault under each registered provider's credential kind ("github",
// "gitlab", …). One account never sees another's list: the cache is keyed by
// account, and only the caller's own tokens are ever fetched.
//
// Cost control: which kinds an account holds is asked of core (names only,
// no plaintext, no audit row) at most every kindsTTL; a provider's list is
// refetched — its token fetched from core (one audit row) and the provider's
// API called — at most every ttl, and after a failure at most every
// retryAfter. A failing provider degrades to its last good list (with
// stale_at) or to nothing, never failing the others. Tokens are never cached:
// only the resulting repository list is.
type UserRepoLister struct {
	registry *gitprovider.Registry
	// listKinds reports the credential kinds accountID holds. ok=false means
	// "couldn't tell" (core unconfigured/unreachable, no live session).
	listKinds func(ctx context.Context, accountID string) (kinds []string, ok bool)
	// fetchToken returns accountID's personal token for kind.
	fetchToken func(ctx context.Context, accountID, kind string) (token []byte, found bool)

	ttl        time.Duration
	kindsTTL   time.Duration
	retryAfter time.Duration
	idleEvict  time.Duration
	now        func() time.Time

	mu    sync.Mutex
	cache map[string]*userRepoAccount // account id → its entries
}

type userRepoAccount struct {
	kinds          map[string]bool // credential kinds held, as of kindsCheckedAt
	kindsKnown     bool
	kindsCheckedAt time.Time
	lastUsed       time.Time
	providers      map[string]*userRepoEntry // provider id → its list
}

type userRepoEntry struct {
	repos     []RepoInfo
	fetchedAt time.Time  // last success (zero: never)
	failedAt  time.Time  // last failure since that success (zero: none)
	staleAt   *time.Time // set while the list is a failed refresh's leftover
}

// NewUserRepoLister wires a lister to the given core lookups.
func NewUserRepoLister(registry *gitprovider.Registry,
	listKinds func(ctx context.Context, accountID string) ([]string, bool),
	fetchToken func(ctx context.Context, accountID, kind string) ([]byte, bool)) *UserRepoLister {
	return &UserRepoLister{
		registry:   registry,
		listKinds:  listKinds,
		fetchToken: fetchToken,
		ttl:        5 * time.Minute,
		kindsTTL:   time.Minute,
		retryAfter: 30 * time.Second,
		idleEvict:  time.Hour,
		now:        time.Now,
		cache:      map[string]*userRepoAccount{},
	}
}

// List returns accountID's own repositories across every provider it holds
// a token for, and the earliest stale_at among lists that are a failed
// refresh's leftovers (nil when all are fresh).
func (l *UserRepoLister) List(ctx context.Context, accountID string) ([]RepoInfo, *time.Time) {
	if l == nil || accountID == "" {
		return nil, nil
	}
	now := l.now()

	l.mu.Lock()
	l.evictIdleLocked(now)
	acct := l.cache[accountID]
	if acct == nil {
		acct = &userRepoAccount{providers: map[string]*userRepoEntry{}}
		l.cache[accountID] = acct
	}
	acct.lastUsed = now
	needKinds := !acct.kindsKnown || now.Sub(acct.kindsCheckedAt) >= l.kindsTTL
	l.mu.Unlock()

	if needKinds {
		kinds, ok := l.listKinds(ctx, accountID)
		l.mu.Lock()
		if ok {
			acct.kinds = make(map[string]bool, len(kinds))
			for _, k := range kinds {
				acct.kinds[k] = true
			}
			acct.kindsKnown = true
			acct.kindsCheckedAt = now
			// A token that is gone takes its list with it: repositories
			// seen through a deleted credential must not linger.
			for id := range acct.providers {
				if !acct.kinds[id] {
					delete(acct.providers, id)
				}
			}
		}
		// !ok: core couldn't say. Keep whatever was known and retry on the
		// next call rather than dropping lists the account may still hold.
		l.mu.Unlock()
	}

	// Which providers need a refetch — decided under the lock, fetched
	// outside it so one slow provider never blocks another account.
	type job struct{ p gitprovider.Provider }
	var jobs []job
	l.mu.Lock()
	for _, p := range l.registry.Providers() {
		if !acct.kinds[p.ID()] {
			continue
		}
		e := acct.providers[p.ID()]
		if e == nil {
			e = &userRepoEntry{}
			acct.providers[p.ID()] = e
		}
		fresh := !e.fetchedAt.IsZero() && now.Sub(e.fetchedAt) < l.ttl
		backingOff := !e.failedAt.IsZero() && now.Sub(e.failedAt) < l.retryAfter
		if !fresh && !backingOff {
			jobs = append(jobs, job{p})
		}
	}
	l.mu.Unlock()

	if len(jobs) > 0 {
		var wg sync.WaitGroup
		for _, j := range jobs {
			wg.Add(1)
			go func(p gitprovider.Provider) {
				defer wg.Done()
				repos, err := l.fetchProvider(ctx, accountID, p)
				l.mu.Lock()
				defer l.mu.Unlock()
				e := acct.providers[p.ID()]
				if e == nil { // the kind vanished meanwhile
					return
				}
				if err != nil {
					t := l.now()
					e.failedAt = t
					if e.staleAt == nil {
						e.staleAt = &t
					}
					return
				}
				e.repos, e.fetchedAt, e.failedAt, e.staleAt = repos, l.now(), time.Time{}, nil
			}(j.p)
		}
		wg.Wait()
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	var out []RepoInfo
	var staleAt *time.Time
	for _, p := range l.registry.Providers() {
		e := acct.providers[p.ID()]
		if e == nil || !acct.kinds[p.ID()] {
			continue
		}
		for _, r := range e.repos {
			r.CheckedOut = []string{}
			out = append(out, r)
		}
		if e.staleAt != nil && (staleAt == nil || e.staleAt.Before(*staleAt)) {
			t := *e.staleAt
			staleAt = &t
		}
	}
	return out, staleAt
}

// errNoToken marks a kind core listed but then had no token for.
type errNoToken struct{}

func (errNoToken) Error() string { return "no token" }

// fetchProvider fetches accountID's token for p and lists p's repositories
// with it. Errors are logged by provider and status only.
func (l *UserRepoLister) fetchProvider(ctx context.Context, accountID string, p gitprovider.Provider) ([]RepoInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, found := l.fetchToken(ctx, accountID, p.ID())
	tok := strings.TrimSpace(string(token))
	if !found || tok == "" {
		return nil, errNoToken{}
	}
	listed, err := p.ListMine(ctx, tok)
	if err != nil {
		log.Printf("repos: listing %s repositories for account=%s failed: %v", p.ID(), accountID, err)
		return nil, err
	}
	out := make([]RepoInfo, 0, len(listed))
	for _, r := range listed {
		full := r.FullName()
		// The rest of the runner addresses a repository as at most
		// "owner/name" (validateRepoName: workspace paths, board repo
		// columns), so a GitLab subgroup project cannot be launched yet —
		// leave it out rather than list something that would be refused.
		if validateRepoName(full) != nil {
			continue
		}
		out = append(out, RepoInfo{Name: r.Name, FullName: full, Provider: r.Provider, CheckedOut: []string{}})
	}
	return out, nil
}

func (l *UserRepoLister) evictIdleLocked(now time.Time) {
	for id, a := range l.cache {
		if now.Sub(a.lastUsed) > l.idleEvict {
			delete(l.cache, id)
		}
	}
}
