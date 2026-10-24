//go:build linux

package clusterworkspace

import (
	"context"
	"encoding/pem"
	"errors"
	"golang.org/x/sys/unix"
	"io"
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
)

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("fixture git: %v: %s", e, out)
	}
	return strings.TrimSpace(string(out))
}
func TestDiskGitTLSIsolationRecoveryAndTampering(t *testing.T) {
	gitPath, e := exec.LookPath("git")
	if e != nil {
		t.Skip("git unavailable")
	}
	repos := t.TempDir()
	seed := t.TempDir()
	fixtureGit(t, seed, "init", "-q")
	fixtureGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "base")
	for _, name := range []string{"one", "two"} {
		fixtureGit(t, repos, "init", "--bare", "-q", name+".git")
		fixtureGit(t, seed, "push", filepath.Join(repos, name+".git"), "HEAD:refs/heads/main")
		fixtureGit(t, filepath.Join(repos, name+".git"), "symbolic-ref", "HEAD", "refs/heads/main")
		fixtureGit(t, filepath.Join(repos, name+".git"), "config", "http.receivepack", "true")
	}
	var wrongHost atomic.Int32
	var denySecondClone atomic.Bool
	var publicOne atomic.Bool
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, token, ok := r.BasicAuth()
		name := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), ".")[0]
		anonymousRead := publicOne.Load() && name == "one" && (r.URL.Query().Get("service") == "git-upload-pack" || strings.HasSuffix(r.URL.Path, "/git-upload-pack"))
		if (!ok || token != "fixture-token-"+name) && !anonymousRead {
			wrongHost.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if denySecondClone.Load() && name == "two" {
			http.NotFound(w, r)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	certPath := filepath.Join(t.TempDir(), "ca.pem")
	if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SSL_CERT_FILE", certPath)
	home := t.TempDir()
	public := filepath.Join(home, "projects")
	private := filepath.Join(home, ".runtime")
	if e = os.WriteFile(filepath.Join(home, "credential-fixture"), []byte("home-private"), 0600); e != nil {
		t.Fatal(e)
	}
	p := testPlan()
	host, _ := url.Parse(server.URL)
	for i := range p.Members {
		m := &p.Members[i]
		m.CloneURL = server.URL + "/" + m.ID + ".git"
		m.PushURL = m.CloneURL
		m.CredentialHost = host.Host
		m.Token = "fixture-token-" + m.ID
		m.DestinationIdentity = m.ID
		p.Manifest.Members[i].Destination = m.ID
	}
	p.Members[1].Provider = "gitlab"
	p.Manifest.Members[1].Provider = "gitlab"
	publicPlan := p
	publicPlan.Members = append([]Member(nil), p.Members...)
	publicPlan.Members[0].Token = ""
	publicPlan.Members[0].PushAuthorized = false
	publicHome := t.TempDir()
	publicDisk, err := newDiskGit(filepath.Join(publicHome, "projects"), filepath.Join(publicHome, "private"), p.SessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	publicRuntime, err := New(publicPlan, publicDisk)
	if err != nil {
		t.Fatal(err)
	}
	publicOne.Store(true)
	if err = publicRuntime.Prepare(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	publicResults := publicRuntime.PushRound(context.Background())
	if !publicResults[0].Pending || publicResults[0].PushError == "" {
		t.Fatal("public clone granted push permission")
	}
	publicDisk.Close()
	publicOne.Store(false)
	failedHome := t.TempDir()
	failedDisk, err := newDiskGit(filepath.Join(failedHome, "projects"), filepath.Join(failedHome, "private"), p.SessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	deniedRuntime, err := New(p, failedDisk)
	if err != nil {
		t.Fatal(err)
	}
	denySecondClone.Store(true)
	if err = deniedRuntime.Prepare(context.Background(), false); err == nil {
		t.Fatal("partial clone started a runtime")
	}
	if _, err = os.Stat(filepath.Join(failedHome, "projects", "one")); !os.IsNotExist(err) {
		t.Fatal("proven unused clone not rolled back")
	}
	failedDisk.Close()
	denySecondClone.Store(false)
	quota := os.Getenv("P3_TEST_QUOTA") == "1"
	if quota {
		if e = os.MkdirAll(public, 0700); e != nil {
			t.Fatal(e)
		}
		if e = unix.Mount("tmpfs", public, "tmpfs", 0, "size=32m,nr_inodes=4096"); e != nil {
			t.Fatal(e)
		}
		defer func() {
			if e := unix.Unmount(public, 0); e != nil {
				t.Error(e)
			}
		}()
	}
	disk, e := newDiskGit(public, private, p.SessionID, quota)
	if e != nil {
		t.Fatal(e)
	}
	runtime, e := New(p, disk)
	if e != nil {
		t.Fatal(e)
	}
	if e = runtime.Prepare(context.Background(), false); e != nil {
		disk.Close()
		t.Fatal(e)
	}
	if wrongHost.Load() != 0 {
		t.Fatal("credential crossed member scopes")
	}
	if _, e = os.Stat(filepath.Join(public, "credential-fixture")); !os.IsNotExist(e) {
		t.Fatal("HOME entered public root")
	}
	for _, m := range p.Members {
		dir := filepath.Join(public, m.ID)
		config, e := os.ReadFile(filepath.Join(dir, ".git", "config"))
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(config), m.Token) {
			t.Fatal("token stored in clone config")
		}
		fixtureGit(t, dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "agent-work")
		// Every network-steering setting in the live metadata is ignored by push.
		fixtureGit(t, dir, "config", "remote.origin.url", "https://untrusted.example/elsewhere")
		fixtureGit(t, dir, "config", "url.https://untrusted.example/.insteadOf", server.URL)
		fixtureGit(t, dir, "config", "http.extraHeader", "Authorization: private-metadata-header")
		fixtureGit(t, dir, "config", "include.path", "/unavailable/agent-include")
		if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("hostile-index"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, filepath.Join(repos, "two.git"), "config", "http.receivepack", "false")
	results := runtime.PushRound(context.Background())
	if results[0].Pending || !results[1].Pending {
		t.Fatal("partial push not isolated", results)
	}
	if wrongHost.Load() != 0 {
		t.Fatal("credential crossed scopes on push")
	}
	p.PreviousResults = results
	p.Identities = disk.Identities()
	p.Runtime.Generation++
	disk.Close()
	fixtureGit(t, filepath.Join(repos, "two.git"), "config", "http.receivepack", "true")
	disk, e = newDiskGit(public, private, p.SessionID, quota, p.Identities...)
	if e != nil {
		t.Fatal(e)
	}
	defer disk.Close()
	runtime, e = New(p, disk)
	if e != nil {
		t.Fatal(e)
	}
	if e = runtime.Prepare(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	results = runtime.PushRound(context.Background())
	if results[0].Pending || results[1].Pending {
		t.Fatal("failed push did not resume", results)
	}
	for _, m := range p.Members {
		head := fixtureGit(t, filepath.Join(repos, m.ID+".git"), "rev-parse", "refs/heads/wip/"+p.SessionID)
		if len(head) != 40 {
			t.Fatal(head)
		}
	}
	t.Run("deep member refusal preserves unrelated push", func(t *testing.T) {
		deep := filepath.Join(public, "two", "deep")
		path := deep
		for range 130 {
			path = filepath.Join(path, "d")
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(deep)
		dir := filepath.Join(public, "one")
		tree := fixtureGit(t, dir, "rev-parse", "HEAD^{tree}")
		head := fixtureGit(t, dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit-tree", tree, "-p", "HEAD", "-m", "independent")
		fixtureGit(t, dir, "update-ref", "HEAD", head)
		out := runtime.PushRound(context.Background())
		if out[0].Pending || !out[1].Pending || out[1].SyncError == "" {
			t.Fatal("member-local traversal blocked unrelated push", out)
		}
		if got := fixtureGit(t, filepath.Join(repos, "one.git"), "rev-parse", "refs/heads/wip/"+p.SessionID); got != head {
			t.Fatal("unrelated member was not pushed")
		}

		// Combine the local refusal with exhaustion hidden below its depth.
		// A valid new A commit must not publish using B's partial accounting.
		newer := fixtureGit(t, dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit-tree", tree, "-p", "HEAD", "-m", "budget blocked")
		fixtureGit(t, dir, "update-ref", "HEAD", newer)
		huge := filepath.Join(path, "hidden-budget")
		file, err := os.Create(huge)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(11 << 30); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		out = runtime.PushRound(context.Background())
		if !out[0].Pending || !out[1].Pending {
			t.Fatal("refused tree hid aggregate exhaustion", out)
		}
		if got := fixtureGit(t, filepath.Join(repos, "one.git"), "rev-parse", "refs/heads/wip/"+p.SessionID); got != head {
			t.Fatal("published despite hidden exhaustion", got)
		}
		if err := os.Remove(huge); err != nil {
			t.Fatal(err)
		}
		out = runtime.PushRound(context.Background())
		if out[0].Pending || !out[1].Pending {
			t.Fatal("bounded accounting did not restore independent push", out)
		}
	})
	t.Run("aggregate exhaustion blocks every member", func(t *testing.T) {
		huge := filepath.Join(public, "budget-fixture")
		file, err := os.Create(huge)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(11 << 30); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		out := runtime.PushRound(context.Background())
		if !out[0].Pending || !out[1].Pending {
			t.Fatal("aggregate exhaustion was only member-local", out)
		}
		if err := os.Remove(huge); err != nil {
			t.Fatal(err)
		}
		runtime.PushRound(context.Background())
	})
	// Cross-member objects metadata redirection is a member-local refusal.
	if e = os.WriteFile(filepath.Join(public, "two", ".git", "objects", "info", "alternates"), []byte(filepath.Join(public, "one", ".git", "objects")), 0600); e != nil {
		t.Fatal(e)
	}
	results = runtime.PushRound(context.Background())
	if results[0].Pending || !results[1].Pending || results[1].SyncError == "" {
		t.Fatal("alternates tampering accepted", results)
	}
	if quota {
		f, err := os.Create(filepath.Join(public, "quota-fixture"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.CopyN(f, zeroReader{}, 40<<20)
		_ = f.Close()
		_ = os.Remove(f.Name())
		if !errors.Is(err, unix.ENOSPC) {
			t.Fatal("filesystem did not enforce physical byte ceiling", err)
		}
	}
	// A member-local switched gitdir must not prevent another member's push.
	if e = os.Rename(filepath.Join(public, "two", ".git"), filepath.Join(public, "two", ".git-held")); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join(public, "one", ".git"), filepath.Join(public, "two", ".git")); e != nil {
		t.Fatal(e)
	}
	firstDir := filepath.Join(public, "one")
	nextHead := fixtureGit(t, firstDir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "more-agent-work")
	if e = os.WriteFile(filepath.Join(firstDir, ".git", "refs", "heads", "wip", p.SessionID), []byte(nextHead+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	results = runtime.PushRound(context.Background())
	if results[0].Pending || !results[1].Pending {
		t.Fatal("member-local gitdir refusal blocked independent push", results)
	}
	// Replacing a member .git cannot be adopted on resume.
	if e = os.Rename(filepath.Join(public, "one", ".git"), filepath.Join(public, "one", ".git-old")); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(public, "one", ".git"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = disk.Head(context.Background(), p.Members[0]); e == nil {
		t.Fatal("replaced git metadata accepted")
	}
	if e = os.CopyFS(filepath.Join(public, "one", ".git"), os.DirFS(filepath.Join(public, "one", ".git-old"))); e != nil {
		t.Fatal(e)
	}
	resumedDisk, e := newDiskGit(public, private, p.SessionID, quota, p.Identities...)
	if e != nil {
		t.Fatal(e)
	}
	defer resumedDisk.Close()
	if _, _, e = resumedDisk.Prepare(context.Background(), p.Members[0], "wip/"+p.SessionID, true); e == nil {
		t.Fatal("resume adopted copied Git metadata with a new identity")
	}

}

func TestUnboundedStorageFailsClosed(t *testing.T) {
	home := t.TempDir()
	if disk, e := NewDiskGit(filepath.Join(home, "projects"), filepath.Join(home, "private"), "session"); e == nil {
		disk.Close()
		t.Fatal("unbounded shared filesystem accepted as per-session quota")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
