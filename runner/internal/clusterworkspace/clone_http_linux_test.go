//go:build linux

package clusterworkspace

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiskGitTransientHTTPClones(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	seed, repos := t.TempDir(), t.TempDir()
	fixtureGit(t, seed, "init", "-q")
	fixtureGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "base")
	fixtureGit(t, repos, "clone", "--bare", seed, "repo.git")
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}}
	for _, status := range []int{429, 503, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var failures atomic.Int32
			failures.Store(1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failures.CompareAndSwap(1, 0) {
					w.Header().Set("Content-Type", "text/plain")
					w.WriteHeader(status)
					if status == 403 {
						_, _ = w.Write([]byte("You have exceeded a secondary rate limit."))
					}
					return
				}
				backend.ServeHTTP(w, r)
			}))
			defer server.Close()
			p := testPlan()
			p.Manifest.Kind = "repo"
			p.Members = p.Members[:1]
			p.Manifest.Members = p.Manifest.Members[:1]
			host, _ := url.Parse(server.URL)
			m := &p.Members[0]
			m.CloneURL = server.URL + "/repo.git"
			m.PushURL = m.CloneURL
			m.Token = ""
			m.CredentialHost = host.Host
			m.PushAuthorized = false
			m.DestinationIdentity = "repo"
			p.Manifest.Members[0].Destination = "repo"
			root := t.TempDir()
			disk, e := NewDiskGitStorage(filepath.Join(root, "projects"), filepath.Join(root, "private"), p.SessionID, false)
			if e != nil {
				t.Fatal(e)
			}
			defer disk.Close()
			waits := 0
			disk.cloneWait = func(context.Context, time.Duration) error { waits++; return nil }
			rt, e := New(p, disk)
			if e != nil {
				t.Fatal(e)
			}
			if e = rt.Prepare(t.Context(), false); e != nil {
				t.Fatalf("HTTP %d did not recover: %v", status, e)
			}
			if waits != 1 {
				t.Fatalf("backoff waits=%d", waits)
			}
		})
	}
}
