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
//
// Latency: a list past ttl but younger than maxStale is served at once while
// one background refresh per account and provider brings it up to date
// (stale-while-revalidate), so opening the launch sheet never waits on a
// token fetch and a paged provider listing. Only a list that was never
// fetched, or is older than maxStale, makes the request wait — and then on
// the one refresh already in flight, never a second. stale_at keeps its
// meaning: a refresh FAILED and this is its leftover; a list being refreshed
// in the background is not flagged.
type UserRepoLister struct {
	registry *gitprovider.Registry
	// listKinds reports the credential kinds accountID holds. ok=false means
	// "couldn't tell" (core unconfigured/unreachable, no live session).
	listKinds func(ctx context.Context, accountID string) (kinds []string, ok bool)
	// fetchToken returns accountID's personal token for kind.
	fetchToken func(ctx context.Context, accountID, kind string) (token []byte, found bool)

	ttl        time.Duration
	maxStale   time.Duration
	kindsTTL   time.Duration
	retryAfter time.Duration
	idleEvict  time.Duration
	now        func() time.Time

	mu    sync.Mutex
	cache map[string]*userRepoAccount // account id → its entries
	// bg counts background refreshes still running (tests wait on it).
	bg sync.WaitGroup
}

type userRepoAccount struct {
	kinds          map[string]bool // credential kinds held, as of kindsCheckedAt
	kindList       []string        // the same kinds, in core's order
	kindsKnown     bool
	kindsCheckedAt time.Time
	// kindsRefreshing is non-nil while core is being asked and is closed when
	// it answers: concurrent callers (the sheet's /api/repos and
	// /api/me/credentials arrive together) wait on that one ask.
	kindsRefreshing chan struct{}
	lastUsed        time.Time
	providers       map[string]*userRepoEntry // provider id → its list
}

type userRepoEntry struct {
	repos     []RepoInfo
	fetchedAt time.Time  // last success (zero: never)
	failedAt  time.Time  // last failure since that success (zero: none)
	staleAt   *time.Time // set while the list is a failed refresh's leftover
	// refreshing is non-nil while a refresh is in flight and is closed when
	// it ends: the single flight every caller that must wait waits on.
	refreshing chan struct{}
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
		maxStale:   30 * time.Minute,
		kindsTTL:   time.Minute,
		retryAfter: 30 * time.Second,
		idleEvict:  time.Hour,
		now:        time.Now,
		cache:      map[string]*userRepoAccount{},
	}
}

// account returns accountID's cache entry with its credential kinds checked
// against core when the last check is older than kindsTTL. known reports
// whether the kinds are known as of this call (cached and fresh, or just
// answered); when core couldn't say, the entry keeps whatever it last knew.
// One ask per account at a time: a caller arriving while core is being asked
// waits for that answer instead of asking again.
func (l *UserRepoLister) account(ctx context.Context, accountID string, now time.Time) (acct *userRepoAccount, known bool) {
	l.mu.Lock()
	l.evictIdleLocked(now)
	acct = l.cache[accountID]
	if acct == nil {
		acct = &userRepoAccount{providers: map[string]*userRepoEntry{}}
		l.cache[accountID] = acct
	}
	acct.lastUsed = now
	fresh := func() bool { return acct.kindsKnown && now.Sub(acct.kindsCheckedAt) < l.kindsTTL }
	if fresh() {
		l.mu.Unlock()
		return acct, true
	}
	if wait := acct.kindsRefreshing; wait != nil {
		l.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		// The ask this caller waited on failed (or it gave up): "couldn't tell",
		// exactly as for the caller that asked.
		return acct, fresh()
	}
	done := make(chan struct{})
	acct.kindsRefreshing = done
	l.mu.Unlock()

	kinds, ok := l.listKinds(ctx, accountID)
	l.mu.Lock()
	defer l.mu.Unlock()
	close(done)
	acct.kindsRefreshing = nil
	if !ok {
		// Core couldn't say. Keep whatever was known and retry on the next
		// call rather than dropping lists the account may still hold.
		return acct, false
	}
	acct.kinds = make(map[string]bool, len(kinds))
	acct.kindList = append([]string(nil), kinds...)
	for _, k := range kinds {
		acct.kinds[k] = true
	}
	acct.kindsKnown = true
	acct.kindsCheckedAt = now
	// A token that is gone takes its list with it: repositories seen
	// through a deleted credential must not linger.
	for id := range acct.providers {
		if !acct.kinds[id] {
			delete(acct.providers, id)
		}
	}
	return acct, true
}

// Kinds reports the credential kinds accountID holds, through the same
// kindsTTL cache List uses — so GET /api/me/credentials and GET /api/repos,
// asked together when the launch sheet opens, ask core once between them.
// ok=false means "couldn't tell", exactly as listKinds.
func (l *UserRepoLister) Kinds(ctx context.Context, accountID string) ([]string, bool) {
	if l == nil || accountID == "" {
		return nil, false
	}
	acct, known := l.account(ctx, accountID, l.now())
	if !known {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, acct.kindList...), true
}

// List returns accountID's own repositories across every provider it holds
// a token for, and the earliest stale_at among lists that are a failed
// refresh's leftovers (nil when all are fresh).
func (l *UserRepoLister) List(ctx context.Context, accountID string) ([]RepoInfo, *time.Time) {
	if l == nil || accountID == "" {
		return nil, nil
	}
	now := l.now()
	acct, _ := l.account(ctx, accountID, now)

	// Which providers need a refresh — decided under the lock, fetched
	// outside it so one slow provider never blocks another account. A
	// refresh outlives this request (its caller may go away, or never have
	// waited for it), so it runs on a context that is never cancelled but
	// keeps the request's values: the core proof rides on them.
	refreshCtx := context.WithoutCancel(ctx)
	var waits []chan struct{}
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
		if !e.fetchedAt.IsZero() && now.Sub(e.fetchedAt) < l.ttl {
			continue // fresh
		}
		// Old enough to refresh. Served meanwhile unless it was never
		// fetched or is past maxStale — then this request waits.
		servable := !e.fetchedAt.IsZero() && now.Sub(e.fetchedAt) < l.maxStale
		if e.refreshing == nil {
			if !e.failedAt.IsZero() && now.Sub(e.failedAt) < l.retryAfter {
				continue // backing off: leftovers (or nothing) until it may retry
			}
			e.refreshing = make(chan struct{})
			l.bg.Add(1)
			go l.refresh(refreshCtx, accountID, acct, p, e)
		}
		if !servable {
			waits = append(waits, e.refreshing)
		}
	}
	l.mu.Unlock()

	for _, w := range waits {
		select {
		case <-w:
		case <-ctx.Done():
		}
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

// refresh fetches p's list for accountID into e and ends e's flight.
func (l *UserRepoLister) refresh(ctx context.Context, accountID string, acct *userRepoAccount, p gitprovider.Provider, e *userRepoEntry) {
	defer l.bg.Done()
	repos, err := l.fetchProvider(ctx, accountID, p)
	l.mu.Lock()
	defer l.mu.Unlock()
	close(e.refreshing)
	e.refreshing = nil
	if acct.providers[p.ID()] != e { // the kind vanished meanwhile
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
