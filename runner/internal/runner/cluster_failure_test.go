//go:build linux

package runner

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Also runs in the Docker pod entry, using real Git and retained files.
func TestClusterWorkspaceFailureRecovery(t *testing.T) { testClusterWorkspaceFailureRecovery(t) }
func testClusterWorkspaceFailureRecovery(t *testing.T) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("fixture requires git")
	}
	command := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("fixture Git failed: %v", e)
		}
		return strings.TrimSpace(string(out))
	}
	repos, seed := t.TempDir(), t.TempDir()
	command(seed, "init", "-q")
	command(seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "base")
	command(repos, "clone", "--bare", seed, "repo.git")
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}}
	var refuse atomic.Int32
	var hits atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if status := refuse.Load(); status != 0 {
			w.WriteHeader(int(status))
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	u, _ := url.Parse(endpoint.URL)
	plan := func() clusterworkspace.Plan {
		return clusterworkspace.Plan{SessionID: "failure-fixture", Manifest: protocol.WorkspaceManifest{Version: 2, Layout: protocol.WorkspaceLayoutNames, Kind: "repo", OwnerID: "owner", Runtime: "cluster", LaunchGeneration: "launch", Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "member", Name: "repo", Path: "repo", SourceIdentity: "source", CommonDirectoryIdentity: "common", Provider: "github", Destination: "repo"}}}, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, Members: []clusterworkspace.Member{{ID: "member", Provider: "github", CloneURL: endpoint.URL + "/repo.git", PushURL: endpoint.URL + "/repo.git", CredentialHost: u.Host, DestinationIdentity: "repo"}}}
	}
	disk := func(home string, ids ...clusterworkspace.MemberIdentity) *clusterworkspace.DiskGit {
		t.Helper()
		d, e := clusterworkspace.NewDiskGitStorage(filepath.Join(home, "projects"), filepath.Join(home, "private"), "failure-fixture", false, ids...)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(d.Close)
		return d
	}
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			refuse.Store(int32(status))
			defer refuse.Store(0)
			rt, e := clusterworkspace.New(plan(), disk(t.TempDir()))
			if e != nil {
				t.Fatal(e)
			}
			if e = rt.Prepare(t.Context(), false); e == nil || !strings.Contains(e.Error(), "repository clone failed") {
				t.Fatalf("missing clone cause: %v", e)
			}
			detail, _ := classifyWorkspaceError(e)
			if detail != "Repository clone failed" {
				t.Fatal("clone cause lost at launch boundary", detail)
			}
			before := hits.Load()
			for range 5 {
				rt.PushRound(t.Context())
			}
			rt.Stop(t.Context())
			if hits.Load() != before {
				t.Fatal("failed preparation queued network retries")
			}
			if !rt.Results()[0].Pending {
				t.Fatal("failed member lost recovery state")
			}
		})
	}
	t.Run("credential host refusal makes no transport calls", func(t *testing.T) {
		p := plan()
		p.Members[0].CredentialHost = "refused.example.com"
		before := hits.Load()
		if _, e := clusterworkspace.New(p, disk(t.TempDir())); e == nil || !strings.Contains(e.Error(), "credential scope refused") {
			t.Fatalf("credential refusal lost its named cause: %v", e)
		}
		if hits.Load() != before {
			t.Fatal("credential refusal reached transport")
		}
	})
	t.Run("restart retains unpublished work and storage loss refuses resume", func(t *testing.T) {
		home := t.TempDir()
		p := plan()
		d := disk(home)
		rt, e := clusterworkspace.New(p, d)
		if e != nil {
			t.Fatal(e)
		}
		if e = rt.Prepare(t.Context(), false); e != nil {
			t.Fatal(e)
		}
		checkout := filepath.Join(home, "projects", "repo")
		command(checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "retained commit")
		head := command(checkout, "rev-parse", "HEAD")
		if e = os.WriteFile(filepath.Join(checkout, "uncommitted"), []byte("retained work"), 0600); e != nil {
			t.Fatal(e)
		}
		p.Identities = d.Identities()
		p.PreviousResults = rt.Results()
		p.Runtime.Generation++
		rt.Stop(t.Context())
		d.Close()
		before := hits.Load()
		resumed, e := clusterworkspace.New(p, disk(home, p.Identities...))
		if e != nil {
			t.Fatal(e)
		}
		if e = resumed.Prepare(t.Context(), true); e != nil {
			t.Fatal(e)
		}
		if resumed.Results()[0].Head != head {
			t.Fatal("resume lost unpublished commit")
		}
		if data, e := os.ReadFile(filepath.Join(checkout, "uncommitted")); e != nil || string(data) != "retained work" {
			t.Fatal("resume lost uncommitted work")
		}
		resumed.Stop(t.Context())
		if e = os.RemoveAll(filepath.Join(home, "projects")); e != nil {
			t.Fatal(e)
		}
		lost, e := clusterworkspace.New(p, disk(home, p.Identities...))
		if e != nil {
			t.Fatal(e)
		}
		if e = lost.Prepare(t.Context(), true); e == nil || !strings.Contains(e.Error(), "retained repository storage is missing") {
			t.Fatalf("missing storage loss cause: %v", e)
		}
		detail, _ := classifyWorkspaceError(e)
		if !strings.Contains(detail, "Retained repository storage is missing") {
			t.Fatal("storage cause lost at launch boundary", detail)
		}
		for range 5 {
			lost.PushRound(t.Context())
		}
		lost.Stop(t.Context())
		if hits.Load() != before {
			t.Fatal("resume or storage loss recloned retained work")
		}
	})
}
