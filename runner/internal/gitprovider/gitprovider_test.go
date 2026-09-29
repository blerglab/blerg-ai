package gitprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeToken is deliberately nothing like a real GitHub/GitLab token.
const fakeToken = "FAKE-TOKEN-not-a-secret"

func TestGitHubParseRemoteURL(t *testing.T) {
	g := NewGitHub(nil)
	good := map[string]string{
		"https://github.com/example-org/example-repo":            "example-org/example-repo",
		"https://github.com/example-org/example-repo.git/":       "example-org/example-repo",
		"http://GitHub.com/example-org/example-repo":             "example-org/example-repo",
		"https://user:secret@github.com/example-org/example.app": "example-org/example.app",
		"git@github.com:example-org/example-repo.git":            "example-org/example-repo",
		"github.com:example-org/example_repo":                    "example-org/example_repo",
		"ssh://git@github.com/example-org/example-repo":          "example-org/example-repo",
	}
	for in, want := range good {
		o, n, ok := g.ParseRemoteURL(in)
		if !ok || o+"/"+n != want {
			t.Errorf("ParseRemoteURL(%q) = %q/%q,%v; want %q", in, o, n, ok, want)
		}
	}
	bad := []string{
		"", "example-repo", "https://gitlab.com/example-org/example-repo",
		"https://github.com.evil.example/a/b", "https://github.com@evil.example/a/b",
		"git@github.com@evil.example:a/b.git", "https://github.com:8443/a/b",
		"git://github.com/a/b", "https://github.com/a", "https://github.com/a/b/c",
		"https://github.com/a/b?x=1", "https://github.com/a/b#f", "https://github.com/a/%2e%2e",
		"https://github.com/a/..", "https://github.com/a/.hidden", "https://github.com/bad_org/b",
		"https://github.com/-a/b", "git@github.com:/a/b.git", "https://github.com/a/b c",
		"https://github.com/" + strings.Repeat("o", 40) + "/b",
	}
	for _, in := range bad {
		if o, n, ok := g.ParseRemoteURL(in); ok {
			t.Errorf("ParseRemoteURL(%q) = %q/%q; want rejected", in, o, n)
		}
	}
}

func TestGitLabParseRemoteURL(t *testing.T) {
	g := NewGitLab(nil)
	good := map[string][2]string{
		"https://gitlab.com/example-group/example-project":            {"example-group", "example-project"},
		"https://gitlab.com/example-group/example-project.git":        {"example-group", "example-project"},
		"git@gitlab.com:example-group/example-project.git":            {"example-group", "example-project"},
		"ssh://git@gitlab.com/example-group/example-project":          {"example-group", "example-project"},
		"https://gitlab.com/example-group/sub-group/example.project":  {"example-group/sub-group", "example.project"},
		"git@gitlab.com:a/b/c/d.git":                                  {"a/b/c", "d"},
		"https://oauth2:secret@gitlab.com/example-group/example_proj": {"example-group", "example_proj"},
		"https://gitlab.com/Group_1/Project-2":                        {"Group_1", "Project-2"},
	}
	for in, want := range good {
		o, n, ok := g.ParseRemoteURL(in)
		if !ok || o != want[0] || n != want[1] {
			t.Errorf("ParseRemoteURL(%q) = %q,%q,%v; want %q,%q", in, o, n, ok, want[0], want[1])
		}
	}
	deep := "https://gitlab.com/" + strings.Repeat("g/", 21) + "p"
	bad := []string{
		"", "https://github.com/a/b", "https://gitlab.com/a", "https://gitlab.com/a/-b",
		"https://gitlab.com/a/b-", "https://gitlab.com/a/b..c", "https://gitlab.com/a/b_-c",
		"https://gitlab.com/a/.b", "https://gitlab.com/_a/b", "https://gitlab.com/a/b.atom",
		"https://gitlab.com/a//b", "https://gitlab.com:8443/a/b", "https://gitlab.com/a/b?x",
		"https://gitlab.com.evil.example/a/b", "git@gitlab.com@evil.example:a/b",
		"https://gitlab.com/a/b$c", "https://gitlab.com/../b", deep,
	}
	for _, in := range bad {
		if o, n, ok := g.ParseRemoteURL(in); ok {
			t.Errorf("ParseRemoteURL(%q) = %q,%q; want rejected", in, o, n)
		}
	}
}

func TestRegistryParseRemoteURLAcrossProviders(t *testing.T) {
	r := Default
	cases := map[string]Ref{
		"git@github.com:example-org/example-repo.git":   {Provider: "github", Owner: "example-org", Name: "example-repo"},
		"https://gitlab.com/example-group/example-proj": {Provider: "gitlab", Owner: "example-group", Name: "example-proj"},
	}
	for in, want := range cases {
		got, ok := r.ParseRemoteURL(in)
		if !ok || got != want {
			t.Errorf("ParseRemoteURL(%q) = %+v,%v; want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"https://bitbucket.org/a/b", "deploy@203.0.113.7:team/project.git", "/srv/git/x", ""} {
		if got, ok := r.ParseRemoteURL(in); ok {
			t.Errorf("ParseRemoteURL(%q) = %+v; want unresolved", in, got)
		}
	}
}

// A third provider is a registration, nothing else: once registered, its
// host parses, its kind resolves, and its token user is used.
func TestRegistryThirdProviderIsARegistration(t *testing.T) {
	r := NewRegistry(NewGitHub(nil), NewGitLab(nil))
	if err := r.Register(NewGitLabAt("gitlab-acme", "git.acme.example", "https://git.acme.example", nil)); err != nil {
		t.Fatal(err)
	}
	ref, ok := r.ParseRemoteURL("git@git.acme.example:platform/tools/deployer.git")
	if !ok || ref.Provider != "gitlab-acme" || ref.Owner != "platform/tools" || ref.Name != "deployer" {
		t.Errorf("third provider remote = %+v,%v", ref, ok)
	}
	if p, ok := r.ProviderForCredentialKind("gitlab-acme"); !ok || p.Host() != "git.acme.example" {
		t.Errorf("ProviderForCredentialKind(gitlab-acme) = %v,%v", p, ok)
	}
	if got := r.CredentialKinds(); strings.Join(got, ",") != "github,gitlab,gitlab-acme" {
		t.Errorf("CredentialKinds = %v", got)
	}
	if err := r.Register(NewGitHubAt("other", "GitHub.com", "https://api.github.com", nil)); err == nil {
		t.Error("a second provider for an already-registered host must be refused")
	}
	if err := r.Register(NewGitLabAt("gitlab", "x.example", "https://x.example", nil)); err == nil {
		t.Error("a second provider for an already-registered id must be refused")
	}
}

func TestParseFullName(t *testing.T) {
	r := Default
	if ref, ok := r.ParseFullName("gitlab", "grp/sub/proj"); !ok || ref.Owner != "grp/sub" || ref.Name != "proj" {
		t.Errorf("gitlab nested = %+v,%v", ref, ok)
	}
	if _, ok := r.ParseFullName("github", "grp/sub/proj"); ok {
		t.Error("github has no nested owners")
	}
	for _, bad := range []string{"", "a", "a/", "/b", "a/..", "a/.b"} {
		if _, ok := r.ParseFullName("github", bad); ok {
			t.Errorf("ParseFullName(github, %q) accepted", bad)
		}
	}
	if _, ok := r.ParseFullName("nope", "a/b"); ok {
		t.Error("unknown provider accepted")
	}
}

func TestCloneURL(t *testing.T) {
	gh, gl := NewGitHub(nil), NewGitLab(nil)
	if got := gh.CloneURL("example-org", "example-repo", ""); got != "https://github.com/example-org/example-repo.git" {
		t.Errorf("github public = %q", got)
	}
	if got := gl.CloneURL("grp/sub", "proj", ""); got != "https://gitlab.com/grp/sub/proj.git" {
		t.Errorf("gitlab public = %q", got)
	}
	// Token conventions: GitHub x-access-token, GitLab oauth2 (both
	// documented by the services). Compared structurally so the fake token
	// never appears in a failure message.
	if got := gh.CloneURL("o", "r", fakeToken); got != "https://x-access-token:"+fakeToken+"@github.com/o/r.git" {
		t.Errorf("github token URL has the wrong shape: %q", RedactURL(got))
	}
	if got := gl.CloneURL("o", "r", fakeToken); got != "https://oauth2:"+fakeToken+"@gitlab.com/o/r.git" {
		t.Errorf("gitlab token URL has the wrong shape: %q", RedactURL(got))
	}
	// A token with URL-special characters is escaped, not spliced.
	odd := "FAKE/with@odd:chars"
	got := gh.CloneURL("o", "r", odd)
	if strings.Contains(got, odd) || strings.Count(got, "@") != 1 {
		t.Errorf("special characters in a token were not escaped: %q", RedactURL(got))
	}
	if Default.TokenUserForHost("gitlab.com") != "oauth2" || Default.TokenUserForHost("git.unknown.example") != "x-access-token" {
		t.Error("TokenUserForHost picked the wrong user")
	}
}

func TestRedactURL(t *testing.T) {
	for _, u := range []string{
		NewGitHub(nil).CloneURL("o", "r", fakeToken),
		NewGitLab(nil).CloneURL("g/s", "p", fakeToken),
		"https://" + fakeToken + "@github.com/o/r.git",
	} {
		red := RedactURL(u)
		if strings.Contains(red, fakeToken) || !strings.Contains(red, "REDACTED") {
			t.Errorf("RedactURL left the token in (len %d)", len(red))
		}
	}
	if got := RedactURL("git@github.com:o/r.git"); got != "git@github.com:o/r.git" {
		t.Errorf("scp form changed: %q", got)
	}
	if got := RedactURL("https://github.com/o/r.git"); got != "https://github.com/o/r.git" {
		t.Errorf("token-less URL changed: %q", got)
	}
}

func TestSameOriginNext(t *testing.T) {
	base := mustParse(t, "https://api.example.test/user/repos")
	cases := map[string]string{
		`<https://api.example.test/user/repos?page=2>; rel="next", <https://api.example.test/user/repos?page=5>; rel="last"`: "https://api.example.test/user/repos?page=2",
		`<https://api.example.test/x?page=5>; rel="last"`:                                                                    "",
		`<https://evil.example/x?page=2>; rel="next"`:                                                                        "",
		`<http://api.example.test/x?page=2>; rel="next"`:                                                                     "",
		`<https://u:p@api.example.test/x?page=2>; rel="next"`:                                                                "",
		`garbage`: "",
		``:        "",
	}
	for h, want := range cases {
		if got := sameOriginNext(base, h); got != want {
			t.Errorf("sameOriginNext(%q) = %q, want %q", h, got, want)
		}
	}
}

func TestGitHubListMinePaginates(t *testing.T) {
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+fakeToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/user/repos" || (r.URL.Query().Get("page") == "" && !strings.Contains(r.URL.RawQuery, "affiliation=owner,collaborator,organization_member")) {
			t.Errorf("unexpected request %s", r.URL)
		}
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/user/repos?page=2>; rel="next"`, srv.URL))
			fmt.Fprint(w, `[{"full_name":"example-org/one","private":true},{"full_name":"bad_org/skipped"}]`)
			return
		}
		fmt.Fprint(w, `[{"full_name":"someone/two","private":false}]`)
	}))
	defer srv.Close()
	g := NewGitHubAt("github", "github.com", srv.URL, srv.Client())
	repos, err := g.ListMine(context.Background(), fakeToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].FullName() != "example-org/one" || !repos[0].Private || repos[1].FullName() != "someone/two" || repos[1].Provider != "github" {
		t.Errorf("repos = %+v", repos)
	}
	if hits.Load() != 2 {
		t.Errorf("hits = %d, want 2", hits.Load())
	}
}

func TestGitLabListMine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects" || r.URL.Query().Get("membership") != "true" {
			t.Errorf("unexpected request %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer "+fakeToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `[{"path_with_namespace":"grp/sub/proj","visibility":"private"},{"path_with_namespace":"me/pub","visibility":"public"},{"path_with_namespace":"nonamespace"}]`)
	}))
	defer srv.Close()
	g := NewGitLabAt("gitlab", "gitlab.com", srv.URL, srv.Client())
	repos, err := g.ListMine(context.Background(), fakeToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].Owner != "grp/sub" || repos[0].Name != "proj" || !repos[0].Private || repos[1].Private {
		t.Errorf("repos = %+v", repos)
	}
	if _, err := g.ListMine(context.Background(), "wrong"); err == nil {
		t.Error("a 401 must be an error")
	} else {
		var se *StatusError
		if !errors.As(err, &se) || se.Status != http.StatusUnauthorized || strings.Contains(err.Error(), "wrong") {
			t.Errorf("err = %v", err)
		}
	}
}

// A next link to another host must not receive the token.
func TestListMineNeverFollowsOffOrigin(t *testing.T) {
	var evilHits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilHits.Add(1)
		fmt.Fprint(w, `[]`)
	}))
	defer evil.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", fmt.Sprintf(`<%s/user/repos?page=2>; rel="next"`, evil.URL))
		fmt.Fprint(w, `[{"full_name":"o/r"}]`)
	}))
	defer srv.Close()
	g := NewGitHubAt("github", "github.com", srv.URL, srv.Client())
	if _, err := g.ListMine(context.Background(), fakeToken); err != nil {
		t.Fatal(err)
	}
	if evilHits.Load() != 0 {
		t.Error("followed a next link to another origin")
	}
}

func TestListMineCapped(t *testing.T) {
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Link", fmt.Sprintf(`<%s/user/repos?page=%d>; rel="next"`, srv.URL, n+1))
		var b strings.Builder
		b.WriteString("[")
		for i := 0; i < perPage; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"full_name":"o/r%d-%d"}`, n, i)
		}
		b.WriteString("]")
		fmt.Fprint(w, b.String())
	}))
	defer srv.Close()
	g := NewGitHubAt("github", "github.com", srv.URL, srv.Client())
	repos, err := g.ListMine(context.Background(), fakeToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != MaxListedRepos || int(hits.Load()) != maxListPages {
		t.Errorf("len = %d hits = %d, want %d / %d", len(repos), hits.Load(), MaxListedRepos, maxListPages)
	}
}

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
