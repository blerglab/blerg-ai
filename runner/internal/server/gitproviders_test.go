package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Obviously-fake token values: nothing here resembles a real credential.
const (
	fakeGitHubTok = "FAKE-GH-TOKEN"
	fakeGitLabTok = "FAKE-GL-TOKEN"
)

// ─── daemon origin reports ───────────────────────────────────────────────────

func TestSetRepoOriginsSanitizesAcrossProviders(t *testing.T) {
	dc := &DaemonConn{}
	dc.SetRepoOrigins(map[string]protocol.RepoOrigin{
		"gh":         {Provider: "github", FullName: "example-org/example-repo"},
		"gl":         {Provider: "gitlab", FullName: "example-group/example-project"},
		"gl-nested":  {Provider: "gitlab", FullName: "example-group/sub/example-project"}, // one slash max, for now
		"gl-bad":     {Provider: "gitlab", FullName: "example-group/-bad"},
		"unknown":    {Provider: "bitbucket", FullName: "a/b"},
		"no-prov":    {Provider: "", FullName: "a/b"},
		"a/b":        {Provider: "github", FullName: "a/b"},
		"gh-bad-org": {Provider: "github", FullName: "bad_org/x"},
	}, map[string]string{"legacy": "example-org/ignored"})
	want := map[string]protocol.RepoOrigin{
		"gh": {Provider: "github", FullName: "example-org/example-repo"},
		"gl": {Provider: "gitlab", FullName: "example-group/example-project"},
	}
	dc.liveMu.RLock()
	got := dc.repoRemotes
	dc.liveMu.RUnlock()
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for k, v := range want {
		if o, ok := dc.RepoOrigin(k); !ok || o != v {
			t.Errorf("RepoOrigin(%q) = %+v,%v; want %+v", k, o, ok, v)
		}
	}
	// An older daemon (no repo_origins) is read as GitHub.
	dc.SetRepoOrigins(nil, map[string]string{"legacy": "example-org/legacy"})
	if o, ok := dc.RepoOrigin("legacy"); !ok || o.Provider != "github" || o.FullName != "example-org/legacy" {
		t.Errorf("legacy fallback = %+v,%v", o, ok)
	}
}

// ─── GET /api/repos ──────────────────────────────────────────────────────────

// A folder whose origin is on GitLab is listed as GitLab — not guessed as
// GitHub — and a same-named folder with a GitHub origin elsewhere is not
// merged with it.
func TestHandleGetReposCarriesProvider(t *testing.T) {
	hub := NewHub()
	d := &DaemonConn{ID: "d1", Name: "desk"}
	d.SetCheckedOutRepos([]string{"lab-proj", "hub-proj"})
	d.SetRepoOrigins(map[string]protocol.RepoOrigin{
		"lab-proj": {Provider: "gitlab", FullName: "example-group/lab-proj"},
		"hub-proj": {Provider: "github", FullName: "example-org/hub-proj"},
	}, nil)
	hub.Register(d)
	api := NewAPI(hub, nil, "", nil, "")
	r := getRepos(t, api)
	if got := r["lab-proj"]; got.Provider != "gitlab" || got.FullName != "example-group/lab-proj" || got.Cloneable == nil || !*got.Cloneable {
		t.Errorf("lab-proj = %+v", got)
	}
	if got := r["hub-proj"]; got.Provider != "github" || got.Remote != "example-org/hub-proj" {
		t.Errorf("hub-proj = %+v", got)
	}
}

// fakeProvider is a registrable Provider whose ListMine is scripted.
type fakeProvider struct {
	id, host string
	mu       sync.Mutex
	repos    []gitprovider.Repo
	err      error
	calls    atomic.Int32
	tokens   []string
}

func (f *fakeProvider) ID() string                                   { return f.id }
func (f *fakeProvider) Host() string                                 { return f.host }
func (f *fakeProvider) ParseRemoteURL(string) (string, string, bool) { return "", "", false }
func (f *fakeProvider) ValidOwnerName(s string) bool                 { return s != "" }
func (f *fakeProvider) ValidRepoName(s string) bool                  { return s != "" }
func (f *fakeProvider) TokenUser() string                            { return "u" }
func (f *fakeProvider) CloneURL(o, n, _ string) string {
	return "https://" + f.host + "/" + o + "/" + n + ".git"
}
func (f *fakeProvider) ListMine(_ context.Context, token string) ([]gitprovider.Repo, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, token)
	if f.err != nil {
		return nil, f.err
	}
	return append([]gitprovider.Repo(nil), f.repos...), nil
}
func (f *fakeProvider) Private(context.Context, string, string, string) (bool, error) {
	return false, errors.New("not used")
}
func (f *fakeProvider) set(repos []gitprovider.Repo, err error) {
	f.mu.Lock()
	f.repos, f.err = repos, err
	f.mu.Unlock()
}

// fakeVault stands in for core: which kinds each account holds and the
// tokens themselves.
type fakeVault struct {
	mu        sync.Mutex
	tokens    map[string]string // "<account>:<kind>" → token
	listCalls atomic.Int32
	fetches   atomic.Int32
}

func (v *fakeVault) kinds(_ context.Context, account string) ([]string, bool) {
	v.listCalls.Add(1)
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for k := range v.tokens {
		if a, kind, _ := strings.Cut(k, ":"); a == account {
			out = append(out, kind)
		}
	}
	return out, true
}

func (v *fakeVault) fetch(_ context.Context, account, kind string) ([]byte, bool) {
	v.fetches.Add(1)
	v.mu.Lock()
	defer v.mu.Unlock()
	t, ok := v.tokens[account+":"+kind]
	return []byte(t), ok
}

func newFakeUserLister(t *testing.T) (*UserRepoLister, *fakeProvider, *fakeProvider, *fakeVault, *time.Time) {
	t.Helper()
	gh := &fakeProvider{id: "github", host: "github.example.test"}
	gl := &fakeProvider{id: "gitlab", host: "gitlab.example.test"}
	vault := &fakeVault{tokens: map[string]string{}}
	l := NewUserRepoLister(gitprovider.NewRegistry(gh, gl), vault.kinds, vault.fetch)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	return l, gh, gl, vault, &now
}

func names(repos []RepoInfo) string {
	var s []string
	for _, r := range repos {
		s = append(s, r.Provider+":"+r.FullName)
	}
	return strings.Join(s, ",")
}

func TestUserRepoListerPerAccountAndCached(t *testing.T) {
	l, gh, gl, vault, now := newFakeUserLister(t)
	gh.set([]gitprovider.Repo{{Provider: "github", Owner: "alice", Name: "tool"}}, nil)
	gl.set([]gitprovider.Repo{
		{Provider: "gitlab", Owner: "alice-group", Name: "tool"},
		{Provider: "gitlab", Owner: "alice-group/sub", Name: "nested"}, // not launchable yet: dropped
	}, nil)
	vault.tokens["alice:github"] = fakeGitHubTok
	vault.tokens["alice:gitlab"] = fakeGitLabTok

	repos, stale := l.List(context.Background(), "alice")
	if names(repos) != "github:alice/tool,gitlab:alice-group/tool" || stale != nil {
		t.Fatalf("alice = %s stale=%v", names(repos), stale)
	}
	if gh.tokens[0] != fakeGitHubTok || gl.tokens[0] != fakeGitLabTok {
		t.Error("each provider must be called with its own token")
	}
	// bob holds nothing: sees nothing, and alice's cached lists stay hers.
	if repos, _ := l.List(context.Background(), "bob"); len(repos) != 0 {
		t.Errorf("bob sees %s", names(repos))
	}
	// Within the TTL: no refetch of tokens or lists.
	*now = now.Add(2 * time.Minute)
	l.List(context.Background(), "alice")
	if gh.calls.Load() != 1 || gl.calls.Load() != 1 || vault.fetches.Load() != 2 {
		t.Errorf("cached call refetched: gh=%d gl=%d fetches=%d", gh.calls.Load(), gl.calls.Load(), vault.fetches.Load())
	}
	// After the TTL: refetched.
	*now = now.Add(4 * time.Minute)
	l.List(context.Background(), "alice")
	if gh.calls.Load() != 2 || gl.calls.Load() != 2 {
		t.Errorf("expired cache not refreshed: gh=%d gl=%d", gh.calls.Load(), gl.calls.Load())
	}
}

// One provider failing degrades to its last good list, flagged stale, and
// never takes the other provider's list down; a provider that has never
// succeeded contributes nothing.
func TestUserRepoListerDegradesPerProvider(t *testing.T) {
	l, gh, gl, vault, now := newFakeUserLister(t)
	vault.tokens["alice:github"] = fakeGitHubTok
	vault.tokens["alice:gitlab"] = fakeGitLabTok
	gh.set([]gitprovider.Repo{{Provider: "github", Owner: "alice", Name: "tool"}}, nil)
	gl.set(nil, errors.New("gitlab api: status 503"))

	repos, stale := l.List(context.Background(), "alice")
	if names(repos) != "github:alice/tool" || stale == nil {
		t.Fatalf("first = %s stale=%v", names(repos), stale)
	}
	// Backing off: an immediate retry does not hammer the failing provider.
	l.List(context.Background(), "alice")
	if gl.calls.Load() != 1 {
		t.Errorf("failing provider retried inside the back-off: %d", gl.calls.Load())
	}
	// GitLab recovers; then GitHub fails after its TTL: GitHub's last good
	// list is kept (stale), GitLab's fresh list is shown.
	*now = now.Add(6 * time.Minute)
	gl.set([]gitprovider.Repo{{Provider: "gitlab", Owner: "grp", Name: "proj"}}, nil)
	gh.set(nil, errors.New("github api: status 502"))
	repos, stale = l.List(context.Background(), "alice")
	if names(repos) != "github:alice/tool,gitlab:grp/proj" || stale == nil {
		t.Fatalf("degraded = %s stale=%v", names(repos), stale)
	}
}

// Deleting a token in Settings takes that provider's repositories with it on
// the next kinds check.
func TestUserRepoListerForgetsDeletedToken(t *testing.T) {
	l, gh, _, vault, now := newFakeUserLister(t)
	vault.tokens["alice:github"] = fakeGitHubTok
	gh.set([]gitprovider.Repo{{Provider: "github", Owner: "alice", Name: "tool"}}, nil)
	if repos, _ := l.List(context.Background(), "alice"); len(repos) != 1 {
		t.Fatalf("repos = %s", names(repos))
	}
	vault.mu.Lock()
	delete(vault.tokens, "alice:github")
	vault.mu.Unlock()
	*now = now.Add(2 * time.Minute) // past kindsTTL, inside the list TTL
	if repos, _ := l.List(context.Background(), "alice"); len(repos) != 0 {
		t.Errorf("deleted token's repos still listed: %s", names(repos))
	}
}

// The merged list dedupes by provider + owner/name, keeping same-named
// repositories on different providers apart, and folds the operator's shared
// org list in.
func TestHandleGetReposMergesUserListsByProvider(t *testing.T) {
	l, gh, gl, vault, _ := newFakeUserLister(t)
	vault.tokens["user-1:github"] = fakeGitHubTok
	vault.tokens["user-1:gitlab"] = fakeGitLabTok
	gh.set([]gitprovider.Repo{
		{Provider: "github", Owner: "example-org", Name: "tool"},
		{Provider: "github", Owner: "Example-Org", Name: "Tool"}, // same repo, other case
	}, nil)
	gl.set([]gitprovider.Repo{{Provider: "gitlab", Owner: "example-org", Name: "tool"}}, nil)

	hub := NewHub()
	d := &DaemonConn{ID: "d1", Name: "desk"}
	d.SetCheckedOutRepos([]string{"tool"})
	d.SetRepoOrigins(map[string]protocol.RepoOrigin{"tool": {Provider: "gitlab", FullName: "example-org/tool"}}, nil)
	hub.Register(d)
	api := NewAPI(hub, nil, "", nil, "")
	api.userRepos = l

	tok := enableBrowserAuth(t, api)("session.start")
	req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	api.HandleGetRepos(rec, req)
	var resp reposResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]RepoInfo{}
	for _, r := range resp.Repos {
		got[r.Provider+":"+strings.ToLower(r.FullName)] = r
	}
	if len(resp.Repos) != 2 {
		t.Fatalf("repos = %s, want the GitHub and the GitLab tool once each", names(resp.Repos))
	}
	// The desk folder's origin is the GitLab one: only it is checked out.
	if g := got["gitlab:example-org/tool"]; len(g.CheckedOut) != 1 || g.CheckedOut[0] != "desk" {
		t.Errorf("gitlab tool = %+v", g)
	}
	if g := got["github:example-org/tool"]; len(g.CheckedOut) != 0 {
		t.Errorf("github tool must not claim the GitLab checkout: %+v", g)
	}
}

// ─── GET /api/me/credentials ─────────────────────────────────────────────────

func TestMyCredentialsReportsEveryGitProvider(t *testing.T) {
	api, token, coreSrv := newCredentialsAPI(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string][]string{"engines": {"claude", "github", "gitlab"}})
	})
	defer coreSrv.Close()
	_, out := callMyCredentials(t, api, token)
	if !out.Git || strings.Join(out.GitProviders, ",") != "github,gitlab" || strings.Join(out.Engines, ",") != "claude" {
		t.Errorf("got %+v", out)
	}
}

// ─── cluster clone: URL and which token goes where ───────────────────────────

func TestCloneURLFor(t *testing.T) {
	jm := &JobManager{GitURLBase: "https://github.com/acme"}
	cases := []struct{ provider, repo, want string }{
		{"", "widget", "https://github.com/acme/widget.git"},
		// owner/name with only the per-org default base: not appended to it.
		{"", "other-org/widget", "https://github.com/other-org/widget.git"},
		{"github", "other-org/widget", "https://github.com/other-org/widget.git"},
		{"gitlab", "example-group/widget", "https://gitlab.com/example-group/widget.git"},
	}
	for _, tc := range cases {
		if got := jm.CloneURLFor(tc.provider, tc.repo); got != tc.want {
			t.Errorf("CloneURLFor(%q,%q) = %q, want %q", tc.provider, tc.repo, got, tc.want)
		}
	}
	// An explicit operator base keeps its historical meaning for GitHub…
	jm = &JobManager{GitURLBase: "https://git.example.test/team", ExplicitGitURLBase: "https://git.example.test/team"}
	if got := jm.CloneURLFor("", "a/b"); got != "https://git.example.test/team/a/b.git" {
		t.Errorf("explicit base = %q", got)
	}
	// …and never captures another provider's repositories.
	if got := jm.CloneURLFor("gitlab", "grp/b"); got != "https://gitlab.com/grp/b.git" {
		t.Errorf("gitlab under explicit base = %q", got)
	}
}

func TestGitCredentialFor(t *testing.T) {
	jm := &JobManager{GitURLBase: "https://github.com/acme"}
	cases := []struct {
		url, kind string
		operator  bool
	}{
		{"https://github.com/acme/w.git", "github", true},
		{"https://gitlab.com/g/w.git", "gitlab", false},
		{"https://evil.example/g/w.git", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		kind, op := jm.gitCredentialFor(tc.url)
		if kind != tc.kind || op != tc.operator {
			t.Errorf("gitCredentialFor(%q) = %q,%v; want %q,%v", tc.url, kind, op, tc.kind, tc.operator)
		}
	}
	// An operator base on an unregistered host gets the operator's own
	// token there — and no personal token of any provider.
	jm = &JobManager{GitURLBase: "https://git.example.test/team"}
	if kind, op := jm.gitCredentialFor("https://git.example.test/team/w.git"); kind != "" || !op {
		t.Errorf("operator host = %q,%v; want no personal kind, operator ok", kind, op)
	}
}

// The reviewer's case: operator base on an unregistered host, a launcher
// holding a personal GitHub token. The GitHub token must not be fetched or
// shipped for that host; only the operator Secret may supply the git token.
func TestCreateSessionJobUnregisteredOperatorHostGetsNoPersonalToken(t *testing.T) {
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{
		"acct-1:github": []byte(fakeGitHubTok),
		"acct-1:claude": []byte("sk-ant-api-FAKE"),
	}}
	jm := newTestJobManager(t, f)
	jm.GitURLBase = "https://git.acme.example/org"
	jm.ExplicitGitURLBase = "https://git.acme.example/org"
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-acme", Repo: "proj", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
	}); err != nil {
		t.Fatal(err)
	}
	secretRaw, _ := json.Marshal(f.createdSecrets)
	body, _ := json.Marshal(f.created[0])
	if strings.Contains(string(secretRaw), fakeGitHubTok) || strings.Contains(string(secretRaw), "BLERG_RUNNER_GIT_TOKEN") {
		t.Errorf("personal GitHub token shipped for an unregistered host: %s", secretRaw)
	}
	if !strings.Contains(string(body), `"BLERG_RUNNER_GIT_URL","value":"https://git.acme.example/org/proj.git"`) {
		t.Errorf("clone URL: %s", body)
	}
	if !strings.Contains(string(body), `"key":"BLERG_RUNNER_GIT_TOKEN","name":"blerg-runner-agent"`) {
		t.Errorf("the operator token should still be offered for the operator's own host: %s", body)
	}
}

// A GitLab clone carries the launcher's GitLab token — never their GitHub
// token, and never the operator's shared token, which belongs to GitHub.
func TestCreateSessionJobGitLabToken(t *testing.T) {
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{
		"acct-1:github": []byte(fakeGitHubTok),
		"acct-1:gitlab": []byte(fakeGitLabTok),
	}}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-gl", Repo: "grp/proj", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
		GitURL: jm.CloneURLFor("gitlab", "grp/proj"),
	}); err != nil {
		t.Fatal(err)
	}
	secretRaw, _ := json.Marshal(f.createdSecrets)
	if !strings.Contains(string(secretRaw), `"BLERG_RUNNER_GIT_TOKEN":"`+fakeGitLabTok+`"`) || strings.Contains(string(secretRaw), fakeGitHubTok) {
		t.Errorf("per-session secret must carry the GitLab token only: %s", secretRaw)
	}
	body, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(body), `"BLERG_RUNNER_GIT_URL","value":"https://gitlab.com/grp/proj.git"`) {
		t.Errorf("clone URL: %s", body)
	}
	if strings.Contains(string(body), `"key":"BLERG_RUNNER_GIT_TOKEN","name":"blerg-runner-agent"`) {
		t.Errorf("operator git token must not be offered to a GitLab clone: %s", body)
	}
}

func TestCreateSessionJobUnknownHostGetsNoToken(t *testing.T) {
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{"acct-1:github": []byte(fakeGitHubTok)}}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(SessionJobSpec{
		SessionID: "s-x", Repo: "a/b", Engine: "claude", SpawningAccountID: "acct-1", AuthSessionID: testSID,
		GitURL: "https://git.unvouched.example/a/b.git",
	}); err != nil {
		t.Fatal(err)
	}
	secretRaw, _ := json.Marshal(f.createdSecrets)
	body, _ := json.Marshal(f.created[0])
	if strings.Contains(string(secretRaw), "BLERG_RUNNER_GIT_TOKEN") || strings.Contains(string(body), `"key":"BLERG_RUNNER_GIT_TOKEN"`) {
		t.Errorf("no git token may go to an unregistered host: secret=%s job=%s", secretRaw, body)
	}
}

// A resume clones from the URL recorded at start — a GitLab repository
// resumes from gitlab.com with the launcher's GitLab token, not from the
// operator's GitHub base.
func TestResumeClonesFromTheRecordedURL(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration test")
	}
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000a1"
	sessionID := "00000000-0000-0000-0000-0000000000a2"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "disconnected", "/workspace/grp/proj", "grp/proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionSpawningAccount(ctx, pool, sessionID, "acct-1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionGitURL(ctx, pool, sessionID, "https://gitlab.com/grp/proj.git"); err != nil {
		t.Fatal(err)
	}
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{"acct-1:gitlab": []byte(fakeGitLabTok)}}
	hub := NewHub()
	hub.SetJobManager(newTestJobManager(t, f))
	resumeClusterSession(ctx, hub, pool, sessionID, "continue", "acct-1", testSID)
	if len(f.created) != 1 {
		t.Fatalf("created = %d Jobs", len(f.created))
	}
	body, _ := json.Marshal(f.created[0])
	if !strings.Contains(string(body), `"BLERG_RUNNER_GIT_URL","value":"https://gitlab.com/grp/proj.git"`) {
		t.Errorf("resume must clone the recorded URL: %s", body)
	}
	secretRaw, _ := json.Marshal(f.createdSecrets)
	if !strings.Contains(string(secretRaw), fakeGitLabTok) {
		t.Errorf("resume must carry the launcher's GitLab token: %s", secretRaw)
	}
}

// The launch request's provider reaches the Job, and a daemon-reported GitLab
// origin resolves a bare folder name to GitLab.
func TestPostSessionsClusterProvider(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.GitHubOrg = ""
	jm.GitURLBase = "https://github.com"
	hub.SetJobManager(jm)
	d := &DaemonConn{ID: "d1", Name: "desk", send: make(chan []byte, 8)}
	d.SetCheckedOutRepos([]string{"lab-folder"})
	d.SetRepoOrigins(map[string]protocol.RepoOrigin{"lab-folder": {Provider: "gitlab", FullName: "example-group/lab-proj"}}, nil)
	hub.Register(d)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")

	for i, body := range []map[string]any{
		{"repo": "example-group/typed", "provider": "gitlab", "runtime": "cluster", "kind": "agent"},
		{"repo": "lab-folder", "runtime": "cluster", "kind": "agent", "daemon_id": "d1"},
	} {
		rec := postSpawn(t, api, body)
		if rec.Code != 202 {
			t.Fatalf("%v: status %d: %s", body, rec.Code, rec.Body.String())
		}
		raw, _ := json.Marshal(f.created[i])
		want := []string{"https://gitlab.com/example-group/typed.git", "https://gitlab.com/example-group/lab-proj.git"}[i]
		if !strings.Contains(string(raw), `"BLERG_RUNNER_GIT_URL","value":"`+want+`"`) {
			t.Errorf("%v: Job should clone %s: %s", body, want, raw)
		}
	}
	for _, body := range []map[string]any{
		{"repo": "a/b", "provider": "bitbucket", "runtime": "cluster", "kind": "agent"},
		{"repo": "bare", "provider": "gitlab", "runtime": "cluster", "kind": "agent"},
		{"repo": "grp/-bad", "provider": "gitlab", "runtime": "cluster", "kind": "agent"},
	} {
		if rec := postSpawn(t, api, body); rec.Code != 422 {
			t.Errorf("%v: status %d, want 422", body, rec.Code)
		}
	}
}

// ─── a named provider is never read as GitHub ────────────────────────────────

// Daemon runtime: a GitLab-shaped owner/name from a caller that sends
// "gitlab" travels to the daemon with its provider, and is refused outright
// for a daemon too old to honour it (it would clone github.com/grp/tool).
func TestPostSessionsDaemonCarriesProvider(t *testing.T) {
	api, dc := runtimeTestAPI(t)
	body := map[string]any{"daemon_id": "d1", "repo": "grp/tool", "provider": "gitlab", "kind": "agent", "runtime": "daemon"}

	// dc said nothing about providers: an older daemon.
	if rec := postSpawn(t, api, body); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("older daemon: status %d, want 422: %s", rec.Code, rec.Body.String())
	}
	select {
	case raw := <-dc.send:
		t.Fatalf("nothing may reach an older daemon: %s", raw)
	default:
	}

	dc.SetGitProviders([]string{"github", "gitlab"})
	rec := postSpawn(t, api, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var msg protocol.SpawnSession
	if err := json.Unmarshal(<-dc.send, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Provider != "gitlab" || msg.Repo != "grp/tool" {
		t.Errorf("spawn = provider %q repo %q, want gitlab grp/tool", msg.Provider, msg.Repo)
	}

	for _, bad := range []map[string]any{
		{"daemon_id": "d1", "repo": "grp/tool", "provider": "bitbucket", "kind": "agent", "runtime": "daemon"},
		{"daemon_id": "d1", "repo": "grp/-bad", "provider": "gitlab", "kind": "agent", "runtime": "daemon"},
	} {
		if rec := postSpawn(t, api, bad); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%v: status %d, want 422", bad, rec.Code)
		}
	}
}

// Runner contract (board cards, MCP, agent tokens): "provider" is honoured
// for a cluster clone, and a git_url on another host than the provider's is
// refused rather than cloned with the wrong token.
func TestRunnerStartHonoursProvider(t *testing.T) {
	f := &fakeK8s{}
	api, _, pool := clusterRunnerAPI(t, f)
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	resp := startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "grp/tool", "provider": "gitlab"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d (%s)", resp.StatusCode, readAllString(t, resp.Body))
	}
	sessionID := decodeSessionID(t, resp)
	resp.Body.Close()
	var stored *string
	if err := pool.QueryRow(context.Background(), `SELECT git_url FROM sessions WHERE id = $1`, sessionID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if derefOrEmpty(stored) != "https://gitlab.com/grp/tool.git" {
		t.Errorf("git_url = %q, want the GitLab URL", derefOrEmpty(stored))
	}

	for _, body := range []map[string]any{
		{"repo": "grp/tool", "provider": "gitlab", "git_url": "https://github.com/grp/tool.git"},
		{"repo": "grp/tool", "provider": "bitbucket"},
	} {
		resp := startReq(t, srv, runnerTestKey, "", body)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%v: status %d, want 422", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
