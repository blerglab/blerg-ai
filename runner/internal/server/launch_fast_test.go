package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

// What the New session sheet asks for when it opens must answer from cache:
// an expired repository list is served at once while ONE background refresh
// runs; only a list never fetched, or past maxStale, makes a request wait.

// listWithin runs l.List and fails the test if it does not answer within d:
// a stale list must never wait on the provider.
func listWithin(t *testing.T, d time.Duration, l *UserRepoLister, account string) []RepoInfo {
	t.Helper()
	got := make(chan []RepoInfo, 1)
	go func() {
		repos, _ := l.List(context.Background(), account)
		got <- repos
	}()
	select {
	case repos := <-got:
		return repos
	case <-time.After(d):
		t.Fatalf("List waited on the provider for a list it could serve stale")
		return nil
	}
}

// eventually waits (up to 2 s) for cond, which a background goroutine makes true.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUserRepoListerServesStaleWhileOneRefreshRuns(t *testing.T) {
	l, gh, _, vault, now := newFakeUserLister(t)
	vault.tokens["alice:github"] = fakeGitHubTok
	gh.set([]gitprovider.Repo{{Provider: "github", Owner: "alice", Name: "old"}}, nil)
	if repos, _ := l.List(context.Background(), "alice"); names(repos) != "github:alice/old" {
		t.Fatalf("first = %s", names(repos))
	}

	// Past the TTL, the provider now slow and with a new list.
	*now = now.Add(6 * time.Minute)
	block := make(chan struct{})
	gh.mu.Lock()
	gh.block = block
	gh.repos = []gitprovider.Repo{{Provider: "github", Owner: "alice", Name: "new"}}
	gh.mu.Unlock()

	// Two opens while the refresh is in flight: both get the old list at
	// once, and only one refresh is started between them.
	for i := 0; i < 2; i++ {
		if repos := listWithin(t, 2*time.Second, l, "alice"); names(repos) != "github:alice/old" {
			t.Fatalf("stale serve %d = %s", i, names(repos))
		}
	}
	// The background refresh reaches the provider in its own time.
	eventually(t, func() bool { return gh.calls.Load() >= 2 })
	if c := gh.calls.Load(); c != 2 {
		t.Errorf("provider calls = %d, want 2 (the first fetch and ONE refresh)", c)
	}
	if f := vault.fetches.Load(); f != 2 {
		t.Errorf("token fetches = %d, want 2", f)
	}

	close(block)
	l.bg.Wait()
	repos, stale := l.List(context.Background(), "alice")
	if names(repos) != "github:alice/new" || stale != nil {
		t.Errorf("after refresh = %s stale=%v", names(repos), stale)
	}
	if c := gh.calls.Load(); c != 2 {
		t.Errorf("a fresh list was refetched: calls = %d", c)
	}
}

// Past maxStale the list is too old to show: the request waits for the
// refresh — and concurrent requests share that one refresh.
func TestUserRepoListerWaitsPastMaxStaleOnOneRefresh(t *testing.T) {
	l, gh, _, vault, now := newFakeUserLister(t)
	vault.tokens["alice:github"] = fakeGitHubTok
	gh.set([]gitprovider.Repo{{Provider: "github", Owner: "alice", Name: "old"}}, nil)
	l.List(context.Background(), "alice")

	*now = now.Add(31 * time.Minute)
	block := make(chan struct{})
	gh.mu.Lock()
	gh.block = block
	gh.repos = []gitprovider.Repo{{Provider: "github", Owner: "alice", Name: "new"}}
	gh.mu.Unlock()

	var wg sync.WaitGroup
	var answered atomic.Int32
	results := make([]string, 3)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			repos, _ := l.List(context.Background(), "alice")
			answered.Add(1)
			results[i] = names(repos)
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	if n := answered.Load(); n != 0 {
		t.Fatalf("%d requests answered with a list past maxStale", n)
	}
	close(block)
	wg.Wait()
	for i, r := range results {
		if r != "github:alice/new" {
			t.Errorf("request %d = %s, want the refreshed list", i, r)
		}
	}
	if c := gh.calls.Load(); c != 2 {
		t.Errorf("provider calls = %d, want 2 (concurrent waiters share one refresh)", c)
	}
}

// The org list: served stale past its TTL with a single background refresh;
// an empty cache waits for the first fetch.
func TestRepoListerServesStaleWhileOneRefreshRuns(t *testing.T) {
	rl := NewRepoLister("example-org", "", nil)
	clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	rl.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	var calls atomic.Int32
	var block chan struct{}
	name := "old"
	rl.fetch = func(context.Context) ([]githubRepo, error) {
		calls.Add(1)
		clockMu.Lock()
		b, n := block, name
		clockMu.Unlock()
		if b != nil {
			<-b
		}
		return []githubRepo{{Name: n, FullName: "example-org/" + n}}, nil
	}

	if repos, stale := rl.List(context.Background()); len(repos) != 1 || repos[0].Name != "old" || stale != nil {
		t.Fatalf("first = %+v stale=%v", repos, stale)
	}
	clockMu.Lock()
	clock = clock.Add(6 * time.Minute)
	block = make(chan struct{})
	name = "new"
	clockMu.Unlock()

	for i := 0; i < 2; i++ {
		got := make(chan []RepoInfo, 1)
		go func() { repos, _ := rl.List(context.Background()); got <- repos }()
		select {
		case repos := <-got:
			if len(repos) != 1 || repos[0].Name != "old" {
				t.Fatalf("stale serve %d = %+v", i, repos)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("List waited on GitHub for a list it could serve stale")
		}
	}
	eventually(t, func() bool { return calls.Load() >= 2 })
	if c := calls.Load(); c != 2 {
		t.Errorf("fetches = %d, want 2 (the first and ONE refresh)", c)
	}
	close(block)
	rl.bg.Wait()
	if repos, _ := rl.List(context.Background()); len(repos) != 1 || repos[0].Name != "new" {
		t.Errorf("after refresh = %+v", repos)
	}

	// A failing refresh keeps the list and flags it stale.
	clockMu.Lock()
	clock = clock.Add(6 * time.Minute)
	block = nil
	clockMu.Unlock()
	rl.fetch = func(context.Context) ([]githubRepo, error) { return nil, errors.New("github api: status 502") }
	rl.List(context.Background())
	rl.bg.Wait()
	if repos, stale := rl.List(context.Background()); len(repos) != 1 || stale == nil {
		t.Errorf("failed refresh = %+v stale=%v, want the old list flagged stale", repos, stale)
	}
}

// The kinds check is one ask per account at a time: the sheet's /api/repos and
// /api/me/credentials arrive together, and the second waits on the first's.
func TestUserRepoListerKindsSingleFlight(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	l := NewUserRepoLister(gitprovider.NewRegistry(&fakeProvider{id: "github", host: "github.example.test"}),
		func(context.Context, string) ([]string, bool) {
			calls.Add(1)
			<-release
			return []string{"claude"}, true
		},
		func(context.Context, string, string) ([]byte, bool) { return nil, false })

	results := make(chan string, 2)
	ask := func() {
		kinds, ok := l.Kinds(context.Background(), "alice")
		if !ok || len(kinds) != 1 {
			results <- "bad"
			return
		}
		results <- kinds[0]
	}
	go ask()
	eventually(t, func() bool { return calls.Load() == 1 })
	go ask()
	time.Sleep(50 * time.Millisecond) // the second caller is waiting by now
	close(release)
	for i := 0; i < 2; i++ {
		if r := <-results; r != "claude" {
			t.Errorf("caller %d got %q", i, r)
		}
	}
	if c := calls.Load(); c != 1 {
		t.Errorf("core asked %d times by two concurrent callers, want 1", c)
	}
}

// GET /api/repos names its body with an ETag and answers an If-None-Match
// that still matches with 304 and no body.
func TestHandleGetReposETag(t *testing.T) {
	hub := NewHub()
	d := &DaemonConn{ID: "d1", Name: "desk"}
	d.SetCheckedOutRepos([]string{"tool"})
	hub.Register(d)
	api := NewAPI(hub, nil, "", nil, "")
	tok := enableBrowserAuth(t, api)("session.start")

	get := func(ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		api.HandleGetRepos(rec, req)
		return rec
	}

	first := get("")
	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || etag == "" {
		t.Fatalf("first: code=%d etag=%q", first.Code, etag)
	}
	if cc := first.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	var resp reposResponse
	if err := json.Unmarshal(first.Body.Bytes(), &resp); err != nil || len(resp.Repos) != 1 {
		t.Fatalf("body = %s (%v)", first.Body.String(), err)
	}

	same := get(etag)
	if same.Code != http.StatusNotModified || same.Body.Len() != 0 {
		t.Errorf("unchanged: code=%d body=%q, want 304 and no body", same.Code, same.Body.String())
	}
	if weak := get(`"other", W/` + etag); weak.Code != http.StatusNotModified {
		t.Errorf("a list naming it (weakly) = %d, want 304", weak.Code)
	}

	// The list changes: a new ETag and the whole body.
	d.SetCheckedOutRepos([]string{"tool", "other"})
	changed := get(etag)
	if changed.Code != http.StatusOK || changed.Header().Get("ETag") == etag {
		t.Errorf("changed: code=%d etag=%q", changed.Code, changed.Header().Get("ETag"))
	}
}

// GET /api/me/credentials reads the repository lister's per-account kinds
// cache: the sheet's two requests ask core once a minute between them.
func TestMyCredentialsSharesTheListersKindsCache(t *testing.T) {
	var coreCalls atomic.Int32
	api, token, coreSrv := newCredentialsAPI(t, func(w http.ResponseWriter, r *http.Request) {
		coreCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string][]string{"engines": {"claude", "github"}})
	})
	defer coreSrv.Close()
	if api.userRepos == nil {
		t.Fatal("core credentials wired but no repository lister")
	}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	api.userRepos.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		_, out := callMyCredentials(t, api, token)
		if out.Unavailable || !out.Git || len(out.Engines) != 1 || out.Engines[0] != "claude" {
			t.Fatalf("call %d = %+v", i, out)
		}
	}
	if c := coreCalls.Load(); c != 1 {
		t.Errorf("core asked %d times within the kinds TTL, want 1", c)
	}
	now = now.Add(2 * time.Minute)
	callMyCredentials(t, api, token)
	if c := coreCalls.Load(); c != 2 {
		t.Errorf("core asked %d times after the kinds TTL, want 2", c)
	}
}

// Status reads the live Job count at most every activeJobsCacheTTL; the
// capacity check (ActiveSessionJobs) always counts live.
func TestJobManagerStatusCachesActiveJobs(t *testing.T) {
	f := &fakeK8s{active: 1}
	jm := newTestJobManager(t, f)
	if s := jm.Status(); s.ActiveSessions != 1 {
		t.Fatalf("ActiveSessions = %d, want 1", s.ActiveSessions)
	}
	f.mu.Lock()
	f.active = 2
	f.mu.Unlock()
	if s := jm.Status(); s.ActiveSessions != 1 {
		t.Errorf("within the TTL: ActiveSessions = %d, want the cached 1", s.ActiveSessions)
	}
	if n, err := jm.ActiveSessionJobs(); err != nil || n != 2 {
		t.Errorf("capacity count = %d, %v; want the live 2", n, err)
	}
	jm.activeCacheMu.Lock()
	jm.activeCacheAt = jm.activeCacheAt.Add(-activeJobsCacheTTL)
	jm.activeCacheMu.Unlock()
	if s := jm.Status(); s.ActiveSessions != 2 {
		t.Errorf("after the TTL: ActiveSessions = %d, want 2", s.ActiveSessions)
	}
}
