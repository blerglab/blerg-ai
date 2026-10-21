package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestClusterRepoResolution(t *testing.T) {
	a := &DaemonConn{ID: "desktop-a"}
	a.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	a.SetRepoRemotes(map[string]string{"app": "example-org/app"})
	b := &DaemonConn{ID: "desktop-b"}
	b.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	b.SetRepoRemotes(map[string]string{"app": "another-org/app"})
	lab := &DaemonConn{ID: "desktop-lab"}
	lab.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	lab.SetRepoOrigins(map[string]protocol.RepoOrigin{"app": {Provider: "gitlab", FullName: "example-group/app"}}, nil)
	for _, tc := range []struct {
		name       string
		host       *DaemonConn
		provider   string
		allowBare  bool
		want, kind string
		refuse     bool
	}{
		{name: "default org wins over the named desktop's origin", host: a, allowBare: true, want: "app"},
		{name: "named desktop's origin without a default org", host: a, want: "example-org/app", kind: "github"},
		{name: "another desktop's origin", host: b, want: "another-org/app", kind: "github"},
		{name: "provider retained", host: lab, want: "example-group/app", kind: "gitlab"},
		{name: "provider mismatch", host: lab, provider: "github", refuse: true},
		{name: "no desktop named needs a name", refuse: true},
		{name: "configured default remains valid", allowBare: true, want: "app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, kind, problem := resolveClusterRepoName("app", tc.provider, tc.host, tc.allowBare)
			if (problem != "") != tc.refuse {
				t.Fatalf("refusal=%v, want %v", problem != "", tc.refuse)
			}
			if !tc.refuse && (repo != tc.want || kind != tc.kind) {
				t.Fatalf("resolved %s on %s", repo, kind)
			}
		})
	}
}

func TestRepositoryHTTPReadCheck(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		body, contentType string
		want              error
	}{
		{name: "readable", status: 200, body: "001e# service=git-upload-pack\n00000000", contentType: "application/x-git-upload-pack-advertisement"},
		{name: "static HTTP", status: 200, body: strings.Repeat("a", 40) + "\trefs/heads/main\n", contentType: "text/plain"},
		{name: "empty static repository", status: 200, contentType: "text/plain"},
		{name: "login page is not a repository", status: 200, body: "<html>fixture-private-body</html>", contentType: "text/html", want: errRepositoryUnreadable},
		{name: "bad credential", status: 401, body: "fixture-private-body", want: errRepositoryUnreadable},
		{name: "scope missing", status: 403, body: "fixture-private-body", want: errRepositoryUnreadable},
		{name: "private or missing", status: 404, body: "fixture-private-body", want: errRepositoryUnreadable},
		{name: "gone", status: 410, want: errRepositoryUnreadable},
		{name: "rate limited", status: 429, want: errRepositoryCheckUnavailable},
		{name: "git service unavailable", status: 503, want: errRepositoryCheckUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, pass, ok := r.BasicAuth()
				if !ok || user != "x-access-token" || pass != "fixture-read-token" {
					t.Error("wrong authentication")
				}
				if r.URL.Path != "/example-org/app.git/info/refs" || r.URL.Query().Get("service") != "git-upload-pack" {
					t.Error("not a Git read advertisement request")
				}
				if strings.Contains(r.URL.String(), "fixture-read-token") {
					t.Error("credential entered the URL")
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			err := checkRepositoryRead(t.Context(), srv.Client(), srv.URL+"/example-org/app.git", "fixture-read-token")
			if !errors.Is(err, tc.want) {
				t.Fatalf("read result: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "fixture-private-body") {
				t.Fatal("upstream body leaked")
			}
		})
	}
}

func TestRepositoryReadRedirectsKeepCredentialOnOriginalOrigin(t *testing.T) {
	var escaped atomic.Int64
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { escaped.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/same/") {
			http.Redirect(w, r, "/renamed/info/refs?service=git-upload-pack", http.StatusMovedPermanently)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/cross/") {
			http.Redirect(w, r, other.URL+"/info/refs", http.StatusFound)
			return
		}
		if _, pass, _ := r.BasicAuth(); pass != "fixture-read-token" {
			t.Error("same-origin credential missing")
		}
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n0000"))
	}))
	defer origin.Close()
	if err := checkRepositoryRead(t.Context(), origin.Client(), origin.URL+"/same", "fixture-read-token"); err != nil {
		t.Fatal(err)
	}
	if err := checkRepositoryRead(t.Context(), origin.Client(), origin.URL+"/cross", "fixture-read-token"); !errors.Is(err, errRepositoryUnreadable) {
		t.Fatal("cross-origin redirect accepted")
	}
	if escaped.Load() != 0 {
		t.Fatal("followed a credential-bearing redirect to another origin")
	}
}

// The pod puts the token into a clone URL only for https (authURL); the precheck must never send it
// over plain http, where it would cross the wire in cleartext.
func TestRepositoryReadNeverSendsCredentialOverHTTP(t *testing.T) {
	var sawAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n0000"))
	}))
	defer srv.Close()
	if err := checkRepositoryRead(t.Context(), srv.Client(), srv.URL+"/example-org/app.git", "fixture-read-token"); err != nil {
		t.Fatal(err)
	}
	if sawAuth.Load() {
		t.Fatal("credential sent over plain http")
	}
}

func TestRepositoryReadUsesSelectedPodCredential(t *testing.T) {
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{"owner-fixture:github": []byte("fixture-personal-read")}, secrets: map[string]map[string]string{"blerg-runner-agent": {gitTokenKey: b64("fixture-operator-read")}}}
	jm := newTestJobManager(t, f)
	var observed bool
	jm.repositoryRead = func(_ context.Context, repo, token string) error {
		observed = true
		if repo != "https://github.com/example-org/private.git" || token != "fixture-personal-read" {
			t.Error("the precheck did not use the pod's selected credential")
		}
		return errRepositoryUnreadable
	}
	err := jm.CreateSessionJob(SessionJobSpec{SessionID: newUUID(), Repo: "example-org/private", SpawningAccountID: "owner-fixture", TokenID: newUUID()})
	if !observed || !errors.Is(err, errRepositoryUnreadable) {
		t.Fatal("personal repository read was not checked")
	}
	if len(f.created) != 0 || len(f.createdSecrets) != 0 {
		t.Fatal("refused repository created a Job or Secret")
	}
}

func TestRepositoryReadDoesNotSendCredentialsToUnapprovedInternalAddress(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { received.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	jm := newTestJobManager(t, &fakeK8s{})
	jm.repositoryRead = nil
	err := jm.precheckRepository(SessionJobSpec{}, target.URL+"/repo.git", map[string][]byte{gitTokenKey: []byte("fixture-read-token")}, map[string]bool{gitTokenKey: true}, false)
	if !errors.Is(err, errRepositoryAddress) || received.Load() != 0 {
		t.Fatal("internal repository URL was not refused before connecting")
	}
}

func TestRepositoryReadUsesThePodsOperatorSecret(t *testing.T) {
	f := &fakeK8s{secrets: map[string]map[string]string{
		"blerg-runner-agent": {gitTokenKey: b64("fixture-old-read")},
		"fixture-oauth":      {gitTokenKey: b64("fixture-current-read")},
	}}
	jm := newTestJobManager(t, f)
	jm.OAuthSecretName = "fixture-oauth"
	jm.repositoryRead = func(_ context.Context, _, token string) error {
		if token != "fixture-current-read" {
			t.Error("precheck used a different operator Secret from the pod")
		}
		return nil
	}
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: newUUID(), Repo: "example-org/repo"}); err != nil {
		t.Fatal(err)
	}
}
func TestRepositoryReadCancellation(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := checkRepositoryRead(ctx, srv.Client(), srv.URL+"/repo.git", ""); !errors.Is(err, errRepositoryCheckUnavailable) {
		t.Fatal("canceled read did not return a temporary failure")
	}
	if calls.Load() != 0 {
		t.Fatal("canceled read contacted the server")
	}
}
