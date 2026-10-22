package hostgit

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit/hostgittest"
)

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repoWithHistory: two commits (one signed-looking, so a signature check would run gpg), files the
// planted drivers would apply to.
func repoWithHistory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "init", "-q", "--initial-branch=main")
	hostgittest.Attributes(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "add", ".")
	fixtureGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-qm", "One")
	tree := fixtureGit(t, dir, "write-tree")
	parent := fixtureGit(t, dir, "rev-parse", "HEAD")
	body := "tree " + tree + "\nparent " + parent + "\nauthor Fixture <f@example.test> 1700000000 +0000\ncommitter Fixture <f@example.test> 1700000000 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n iQEzBAABCAAdFiEE\n -----END PGP SIGNATURE-----\n\nSigned\n"
	f := filepath.Join(t.TempDir(), "commit")
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	signed := fixtureGit(t, dir, "hash-object", "-t", "commit", "-w", f)
	fixtureGit(t, dir, "update-ref", "refs/heads/main", signed)
	return dir
}

// Every kind of call the daemon makes in a repository whose configuration someone else could have
// written, with every planted attack: nothing runs and no named file is read. (No call site runs a
// command that reads the work tree through attributes, status, add or checkout, in such a
// repository: a filter driver's name is the repository's choice, so it cannot be overridden by
// name. The one checkout, of a sandbox clone, runs in a clone whose configuration is new.)
func TestRunIgnoresPlantedConfiguration(t *testing.T) {
	dir := repoWithHistory(t)
	probe := hostgittest.Plant(t, filepath.Join(dir, ".git"))
	o := Options{Dir: dir, SafeDirectory: dir, Timeout: 20 * time.Second}
	for _, args := range [][]string{
		{"rev-parse", "--verify", "HEAD"}, {"rev-parse", "--show-toplevel", "--absolute-git-dir"},
		{"show", "--patch", "--no-ext-diff", "--no-textconv", "HEAD"}, {"log", "-p", "--no-ext-diff", "--no-textconv", "-2"},
		{"show", "--numstat", "--no-ext-diff", "--no-textconv", "HEAD"}, {"diff", "--no-ext-diff", "--no-textconv", "HEAD~1", "HEAD"}, {"for-each-ref"}, {"worktree", "list", "--porcelain"},
		{"update-ref", "refs/heads/other", "HEAD"}, {"update-ref", "-d", "refs/heads/other"},
		{"symbolic-ref", "HEAD", "refs/heads/main"},
	} {
		if out, err := Output(t.Context(), o, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out[max(0, len(out)-1200):])
		}
	}
	// A local clone and fetch (the file transport allowed).
	clone := filepath.Join(t.TempDir(), "clone")
	if out, err := Output(t.Context(), Options{Dir: filepath.Dir(clone), Protocols: []string{"file"}, Timeout: 20 * time.Second}, "clone", "-q", dir, clone); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	if out, err := Output(t.Context(), Options{Dir: clone, Protocols: []string{"file"}, Timeout: 20 * time.Second}, "fetch", "-q", dir, "+refs/heads/main:refs/heads/fetched"); err != nil {
		t.Fatalf("fetch: %v: %s", err, out)
	}
	probe.Check(t)
}

// Planted protocol keys and a partial clone's lazy fetch: no transport runs, with or without
// GIT_NO_LAZY_FETCH.
func TestRunAllowsNoTransportByDefault(t *testing.T) {
	dir := repoWithHistory(t)
	marker := filepath.Join(t.TempDir(), "fetched")
	blob := fixtureGit(t, dir, "rev-parse", "HEAD:a.txt")
	if err := os.Remove(filepath.Join(dir, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"core.repositoryformatversion", "1"}, {"extensions.partialClone", "origin"},
		{"remote.origin.url", "ext::sh -c touch% " + marker}, {"remote.origin.promisor", "true"},
		{"protocol.ext.allow", "always"}, {"protocol.allow", "always"}, {"remote.origin.uploadpack", "touch " + marker},
	} {
		hostgittest.Config(t, filepath.Join(dir, ".git"), kv[0], kv[1])
	}
	for _, without := range []bool{false, true} {
		restore := SetWithoutLazyFetchVarForTest(without)
		_, _ = Output(t.Context(), Options{Dir: dir, Timeout: 10 * time.Second}, "show", "--patch", "HEAD~1")
		_, _ = Output(t.Context(), Options{Dir: dir, Timeout: 10 * time.Second}, "fetch", "origin")
		restore()
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("a transport ran (GIT_NO_LAZY_FETCH left out: %t)", without)
		}
	}
}

func TestEnvironmentIsGitsOwn(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere")
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "secret")
	t.Setenv("HTTPS_PROXY", "http://proxy.example.test:3128")
	env := Environ(Options{Protocols: []string{"file"}, PassEnv: []string{"HTTPS_PROXY", "GIT_DIR"}}, "/usr/bin/git")
	for _, want := range []string{"HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ALLOW_PROTOCOL=file",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=/dev/null", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0", "HTTPS_PROXY=http://proxy.example.test:3128"} {
		if !slices.Contains(env, want) {
			t.Errorf("environment lacks %s: %v", want, env)
		}
	}
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_DIR=") || strings.Contains(e, "secret") {
			t.Errorf("inherited %s", e)
		}
	}
	if env := Environ(Options{}, "/usr/bin/git"); !slices.Contains(env, "GIT_ALLOW_PROTOCOL=none") {
		t.Errorf("no transport is GIT_ALLOW_PROTOCOL=none: %v", env)
	}
	user := Environ(Options{UserConfig: true, UserEnv: []string{"HOME=/home/person", "GIT_DIR=/elsewhere", "PATH=/opt/bin"}, Protocols: []string{"https"}}, "/usr/bin/git")
	if !slices.Contains(user, "HOME=/home/person") || slices.Contains(user, "GIT_DIR=/elsewhere") || slices.Contains(user, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Errorf("a remote clone keeps the person's configuration, never an inherited GIT_*: %v", user)
	}
	cfg := strings.Join(Config(Options{Protocols: []string{"https"}}), " ")
	for _, want := range []string{"protocol.https.allow=always", "protocol.ext.allow=never", "protocol.file.allow=never", "core.sshCommand=false", "credential.helper=", "diff.orderFile=/dev/null", "core.hooksPath=/dev/null"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %s: %s", want, cfg)
		}
	}
}

func TestProtocolsFor(t *testing.T) {
	for u, want := range map[string]string{
		"https://example.test/o/r.git": "https", "http://example.test/r": "http", "ssh://git@example.test/r": "ssh",
		"git@example.test:o/r.git": "ssh", "/srv/repos/r.git": "file", "file:///srv/r": "file", "ext::sh -c x": "", "fd::5": "",
	} {
		if got := strings.Join(ProtocolsFor(u), ","); got != want {
			t.Errorf("%s: %q, want %q", u, got, want)
		}
	}
}

// The whole process group dies on a timeout: a helper git started does not outlive the call.
func TestRunKillsTheProcessGroup(t *testing.T) {
	bin := t.TempDir()
	pids := filepath.Join(bin, "pids")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nsleep 300 &\necho $! > '"+pids+"'\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	start := time.Now()
	err := Run(context.Background(), Options{Timeout: 300 * time.Millisecond}, "status")
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout not enforced: %v after %s", err, time.Since(start))
	}
	raw, _ := os.ReadFile(pids)
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if convErr != nil {
		t.Fatal("helper never started")
	}
	time.Sleep(100 * time.Millisecond)
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("git's helper outlived the call")
	}
}

func TestMemoryLimitStopsAnUnboundedReader(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the limit is Linux only")
	}
	dir := repoWithHistory(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	err := Run(ctx, Options{Dir: dir, MemoryLimit: true, Config: []string{"diff.orderFile=/dev/zero"}, Stdout: io.Discard}, "show", "--numstat", "HEAD~1")
	if err == nil || ctx.Err() != nil || time.Since(start) > 10*time.Second {
		t.Fatalf("not stopped by the limit: %v after %s", err, time.Since(start))
	}
}

// Security round 1 (#136, I1a): a repository someone else writes is served and fetched without its
// optional binary indexes being parsed: a garbage commit-graph and multi-pack index are never read.
func TestUntrustedFetchParsesNoOptionalIndexes(t *testing.T) {
	src := repoWithHistory(t)
	for _, name := range []string{"objects/info/commit-graph", "objects/pack/multi-pack-index"} {
		garbage := make([]byte, 4096)
		for i := range garbage {
			garbage[i] = byte(i*7 + 3)
		}
		if err := os.WriteFile(filepath.Join(src, ".git", filepath.FromSlash(name)), garbage, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	serve, err := ServeCommand("upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	fixtureGit(t, filepath.Dir(dst), "init", "-q", dst)
	var stderr strings.Builder
	o := Options{Dir: dst, Protocols: []string{"file"}, Untrusted: true, Timeout: 20 * time.Second, Stderr: &stderr}
	if err := Run(t.Context(), o, "fetch", "--no-auto-maintenance", "--no-write-commit-graph", "--upload-pack="+serve, filepath.Join(src, ".git"), "+refs/heads/main:refs/heads/fetched"); err != nil {
		t.Fatalf("fetch: %v: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "commit-graph") || strings.Contains(stderr.String(), "multi-pack-index") {
		t.Fatalf("an optional index was parsed:\n%s", stderr.String())
	}
	cfg := strings.Join(Config(Options{Untrusted: true}), " ")
	for _, want := range append(UntrustedConfig, "gc.auto=0", "maintenance.auto=false", "fetch.writeCommitGraph=false", "advice.graftFileDeprecated=false") {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %s", want)
		}
	}
	if !strings.Contains(serve, "'core.commitGraph=false'") || !strings.Contains(serve, "'upload-pack' '--strict'") || !strings.Contains(serve, "'--no-replace-objects'") {
		t.Errorf("serving command: %s", serve)
	}
}

// M6: no argument of a serving command may hold a quote; one that does is refused, not escaped.
func TestServeCommandRefusesAQuote(t *testing.T) {
	for _, service := range []string{"upload-pack", "receive-pack"} {
		cmd, err := ServeCommand(service)
		if err != nil || strings.Count(cmd, "'")%2 != 0 {
			t.Fatalf("%s: %q %v", service, cmd, err)
		}
	}
	if _, err := shellQuote([]string{"git", "-c", "x.y=it's"}); err == nil {
		t.Fatal("a quote was accepted")
	}
	for _, a := range Config(Options{Untrusted: true}) {
		if strings.ContainsRune(a, '\'') {
			t.Fatalf("a shared override holds a quote: %s", a)
		}
	}
	if cfg := strings.Join(Config(Options{Protocols: []string{"ssh"}}), " "); !strings.Contains(cfg, "core.sshCommand=ssh") {
		t.Errorf("an ssh call pins the ssh command: %s", cfg)
	}
}
