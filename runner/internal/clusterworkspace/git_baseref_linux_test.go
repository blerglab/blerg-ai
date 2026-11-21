//go:build linux

package clusterworkspace

import (
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiskGitPrepareClonesBaseRefNotDefaultBranch proves BLG-501's fix: a member carrying a
// BaseRef (a host Main's cluster worker, design §3.3) clones that branch's tip, not the
// remote's default branch — the gap a launch-time --base check alone cannot catch, since the
// check only verifies the branch exists upstream, never which ref the pod actually clones.
func TestDiskGitPrepareClonesBaseRefNotDefaultBranch(t *testing.T) {
	gitPath, e := exec.LookPath("git")
	if e != nil {
		t.Skip("git unavailable")
	}
	repos := t.TempDir()
	seed := t.TempDir()
	fixtureGit(t, seed, "init", "-q")
	fixtureGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "default tip")
	defaultBranch := fixtureGit(t, seed, "symbolic-ref", "--short", "HEAD")
	defaultSHA := fixtureGit(t, seed, "rev-parse", "HEAD")
	fixtureGit(t, seed, "checkout", "-qb", "alpha/0.6.0")
	fixtureGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "alpha tip")
	alphaSHA := fixtureGit(t, seed, "rev-parse", "HEAD")
	if defaultSHA == alphaSHA {
		t.Fatal("fixture branches must diverge")
	}
	fixtureGit(t, repos, "init", "--bare", "-q", "app.git")
	fixtureGit(t, seed, "push", filepath.Join(repos, "app.git"), defaultBranch+":refs/heads/"+defaultBranch, "alpha/0.6.0:refs/heads/alpha/0.6.0")
	fixtureGit(t, filepath.Join(repos, "app.git"), "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch)

	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	certPath := filepath.Join(t.TempDir(), "ca.pem")
	if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SSL_CERT_FILE", certPath)

	p := testPlan()
	host, _ := url.Parse(server.URL)
	for i := range p.Members {
		p.Members[i].CloneURL = server.URL + "/app.git"
		p.Members[i].PushURL = p.Members[i].CloneURL
		p.Members[i].CredentialHost = host.Host
		p.Members[i].PushAuthorized = false
		p.Members[i].Token = ""
		p.Members[i].DestinationIdentity = "app"
		p.Manifest.Members[i].Destination = "app"
	}
	// Only "one" carries the required --base; "two" (no BaseRef) clones the default branch
	// unchanged, proving this fix is additive and never touches an ordinary member.
	p.Members[0].BaseRef = "alpha/0.6.0"

	home := t.TempDir()
	disk, err := NewDiskGitStorage(filepath.Join(home, "projects"), filepath.Join(home, "private"), p.SessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	rt, err := New(p, disk)
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.Prepare(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	results := rt.Results()
	if len(results) != 2 {
		t.Fatalf("expected two results, got %d", len(results))
	}
	if got := fixtureGit(t, filepath.Join(home, "projects", p.Manifest.Members[0].Path), "rev-parse", "refs/blerg/result-base"); got != alphaSHA {
		t.Fatalf("bundle launch boundary = %s, want %s", got, alphaSHA)
	}
	if results[0].Head != alphaSHA {
		t.Fatalf("member with BaseRef cloned %s, want the base branch's tip %s (default branch's tip is %s)", results[0].Head, alphaSHA, defaultSHA)
	}
	if results[1].Head != defaultSHA {
		t.Fatalf("member without BaseRef cloned %s, want the default branch's tip %s (changed by an unrelated member's BaseRef)", results[1].Head, defaultSHA)
	}
}

// TestDiskGitPreparePinsBaseSHAEvenWhenBranchMovedSincLaunch proves the Codex-review fix to
// 1524a5d8: the launch verified one exact commit (BaseSHA); the pod must land on that commit
// even if the remote's branch has since moved further ahead — a branch name alone is mutable,
// and cloning --branch=<name> alone would otherwise silently pick up whatever is there by the
// time the pod actually runs, not what the launch checked.
func TestDiskGitPreparePinsBaseSHAEvenWhenBranchMovedSinceLaunch(t *testing.T) {
	gitPath, e := exec.LookPath("git")
	if e != nil {
		t.Skip("git unavailable")
	}
	repos := t.TempDir()
	seed := t.TempDir()
	fixtureGit(t, seed, "init", "-q")
	fixtureGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "base")
	fixtureGit(t, seed, "checkout", "-qb", "alpha/0.6.0")
	fixtureGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "verified tip")
	verifiedSHA := fixtureGit(t, seed, "rev-parse", "HEAD")
	fixtureGit(t, repos, "init", "--bare", "-q", "app.git")
	fixtureGit(t, seed, "push", filepath.Join(repos, "app.git"), "alpha/0.6.0:refs/heads/alpha/0.6.0")
	fixtureGit(t, filepath.Join(repos, "app.git"), "symbolic-ref", "HEAD", "refs/heads/alpha/0.6.0")
	// The branch moves further ahead after the launch verified verifiedSHA — exactly what a
	// second push between launch and pod-start would do.
	fixtureGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "pushed after launch")
	movedSHA := fixtureGit(t, seed, "rev-parse", "HEAD")
	fixtureGit(t, seed, "push", filepath.Join(repos, "app.git"), "alpha/0.6.0:refs/heads/alpha/0.6.0")
	if movedSHA == verifiedSHA {
		t.Fatal("fixture commits must differ")
	}

	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	certPath := filepath.Join(t.TempDir(), "ca.pem")
	if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SSL_CERT_FILE", certPath)

	p := testPlan()
	p.Members = p.Members[:1]
	p.Manifest.Members = p.Manifest.Members[:1]
	host, _ := url.Parse(server.URL)
	p.Members[0].CloneURL = server.URL + "/app.git"
	p.Members[0].PushURL = p.Members[0].CloneURL
	p.Members[0].CredentialHost = host.Host
	p.Members[0].PushAuthorized = false
	p.Members[0].Token = ""
	p.Members[0].DestinationIdentity = "app"
	p.Manifest.Members[0].Destination = "app"
	p.Members[0].BaseRef = "alpha/0.6.0"
	p.Members[0].BaseSHA = verifiedSHA

	home := t.TempDir()
	disk, err := NewDiskGitStorage(filepath.Join(home, "projects"), filepath.Join(home, "private"), p.SessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	rt, err := New(p, disk)
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.Prepare(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	results := rt.Results()
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].Head != verifiedSHA {
		t.Fatalf("landed on %s, want the verified pinned commit %s (the branch's current, unpinned tip is %s)", results[0].Head, verifiedSHA, movedSHA)
	}
}

// Resume retains unpublished work even when a read-only launch pins a base.

func TestDiskGitPrepareResumeRetainsUnpublishedWorkFromPinnedBase(t *testing.T) {
	gitPath, e := exec.LookPath("git")
	if e != nil {
		t.Skip("git unavailable")
	}
	repos := t.TempDir()
	seed := t.TempDir()
	fixtureGit(t, seed, "init", "-q")
	fixtureGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "base")
	fixtureGit(t, seed, "checkout", "-qb", "alpha/0.6.0")
	fixtureGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "first verified tip")
	firstSHA := fixtureGit(t, seed, "rev-parse", "HEAD")
	fixtureGit(t, repos, "init", "--bare", "-q", "app.git")
	fixtureGit(t, seed, "push", filepath.Join(repos, "app.git"), "alpha/0.6.0:refs/heads/alpha/0.6.0")
	fixtureGit(t, filepath.Join(repos, "app.git"), "symbolic-ref", "HEAD", "refs/heads/alpha/0.6.0")

	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { backend.ServeHTTP(w, r) }))
	defer server.Close()
	certPath := filepath.Join(t.TempDir(), "ca.pem")
	if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SSL_CERT_FILE", certPath)

	p := testPlan()
	p.Members = p.Members[:1]
	p.Manifest.Members = p.Manifest.Members[:1]
	host, _ := url.Parse(server.URL)
	p.Members[0].CloneURL = server.URL + "/app.git"
	p.Members[0].PushURL = p.Members[0].CloneURL
	p.Members[0].CredentialHost = host.Host
	p.Members[0].PushAuthorized = false
	p.Members[0].Token = ""
	p.Members[0].DestinationIdentity = "app"
	p.Manifest.Members[0].Destination = "app"
	p.Members[0].BaseRef = "alpha/0.6.0"
	p.Members[0].BaseSHA = firstSHA

	home := t.TempDir()
	public := filepath.Join(home, "projects")
	disk, err := NewDiskGitStorage(public, filepath.Join(home, "private"), p.SessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(p, disk)
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.Prepare(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if got := rt.Results()[0].Head; got != firstSHA {
		t.Fatalf("first prepare landed on %s, want %s", got, firstSHA)
	}
	// Simulate eviction mid-turn, before any result bundle was exported.
	checkout := filepath.Join(public, p.Manifest.Members[0].Path)
	fixtureGit(t, checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "pre-resume local work")
	preResumeSHA := fixtureGit(t, checkout, "rev-parse", "HEAD")
	sessionBranch := fixtureGit(t, checkout, "symbolic-ref", "--short", "HEAD")
	if err := os.WriteFile(filepath.Join(checkout, "unfinished"), []byte("work"), 0600); err != nil {
		t.Fatal(err)
	}
	identities := disk.Identities()
	disk.Close()

	// Main advances the remote base while the pod is gone. That must not replace
	// the worker's local session branch or its unpublished commits.
	fixtureGit(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "second verified tip")
	secondSHA := fixtureGit(t, seed, "rev-parse", "HEAD")
	fixtureGit(t, seed, "push", filepath.Join(repos, "app.git"), "alpha/0.6.0:refs/heads/alpha/0.6.0")
	p.Members[0].BaseSHA = secondSHA

	resumedDisk, err := newDiskGit(public, filepath.Join(home, "private"), p.SessionID, false, identities...)
	if err != nil {
		t.Fatal(err)
	}
	defer resumedDisk.Close()
	resumedRt, err := New(p, resumedDisk)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumedRt.Prepare(t.Context(), true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	results := resumedRt.Results()
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].Head != preResumeSHA {
		t.Fatalf("resume lost unpublished work: got %s, refreshed base %s, retained tip %s", results[0].Head, secondSHA, preResumeSHA)
	}
	if got := fixtureGit(t, checkout, "rev-parse", sessionBranch); got != preResumeSHA {
		t.Fatal("session branch lost unpublished commit")
	}
	if data, err := os.ReadFile(filepath.Join(checkout, "unfinished")); err != nil || string(data) != "work" {
		t.Fatal("resume lost unfinished files", err)
	}

	// Missing storage must refuse recovery, never substitute a fresh clone.
	resumedDisk.Close()
	saved := filepath.Join(home, "saved-checkout")
	if err := os.Rename(checkout, saved); err != nil {
		t.Fatal(err)
	}
	lostDisk, err := newDiskGit(public, filepath.Join(home, "private"), p.SessionID, false, identities...)
	if err != nil {
		t.Fatal(err)
	}
	defer lostDisk.Close()
	lostRuntime, err := New(p, lostDisk)
	if err != nil {
		t.Fatal(err)
	}
	if err := lostRuntime.Prepare(t.Context(), true); err == nil || !strings.Contains(err.Error(), "retained repository storage is missing") {
		t.Fatal("missing pinned-base checkout did not refuse resume", err)
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatal("missing checkout was replaced", err)
	}
	if got := fixtureGit(t, saved, "rev-parse", sessionBranch); got != preResumeSHA {
		t.Fatal("saved objects lost unpublished work")
	}
}
